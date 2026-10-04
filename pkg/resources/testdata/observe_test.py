"""Tests for the observe extension.

Covers the two responsibilities of observe.py:
1. Sidecar lifecycle — write at session_start, update on lifecycle events,
   keep but mark 'ended' on session_shutdown.
2. Socket — bind on session_start, accept connections, parse NDJSON lines,
   forward {content, deliver_as} to send_user_message; clean up on shutdown.

These are unit tests with the SDK's run() patched out — we drive lifecycle
events directly via the registered handlers.
"""

import json
import os
import shutil
import socket
import sys
import tempfile
import threading
import time
import unittest
from contextlib import suppress
from datetime import datetime, timezone
from unittest import mock
from unittest.mock import MagicMock

_sdk_path = os.path.join(
    os.path.dirname(__file__),
    "..",
    "..",
    "extension",
    "sdk",
    "python",
)
sys.path.insert(0, _sdk_path)
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "builtin_extensions"))

import fir_ext

with mock.patch.object(fir_ext, "run"):
    import observe


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _make_ctx(session_file: str = "/tmp/test-session.jsonl", session_name: str = "") -> MagicMock:
    """Construct a fake fir_ext.Context with the bridge calls observe.py uses."""
    ctx = MagicMock(spec=fir_ext.Context)
    ctx.get_session_file.return_value = session_file
    ctx.get_session_name.return_value = session_name
    return ctx


def _reset_observe_state(state_dir: str, sock_dir: str) -> None:
    """Reset observe.py module-level state and point its dirs at tmp."""
    # Point both dirs at the tmp roots
    os.environ["XDG_STATE_HOME"] = state_dir
    os.environ["FIR_OBSERVE_DIR"] = sock_dir
    # Reset module state
    observe._state.update(
        {
            "session_id": "",
            "pid": os.getpid(),
            "host_pid": observe._host_pid(),
            "socket_path": "",
            "store_path": "",
            "cwd": "",
            "started_at": "",
            "status": "running",
            "session_name": "",
            "schema": 1,
        }
    )
    if observe._socket is not None:
        with suppress(Exception):
            observe._socket.close()
        observe._socket = None
    observe._shutdown.clear()


# ---------------------------------------------------------------------------
# Sidecar tests
# ---------------------------------------------------------------------------


class TestSidecar(unittest.TestCase):
    def setUp(self) -> None:
        self.state_dir = tempfile.mkdtemp(prefix="observe-test-state-")
        # Same short-path trick as TestSocket (see comment there).
        self.sock_dir = f"/tmp/o{os.getpid()}-{int(time.time() * 1000) % 10000}"
        os.makedirs(self.sock_dir, exist_ok=True)
        _reset_observe_state(self.state_dir, self.sock_dir)
        # Use a short, predictable session id so the socket-name prefix is
        # exercised but paths stay short on macOS.
        self.session_id = "abcd1234ef567890" + "0" * 20  # 36 chars total

    def tearDown(self) -> None:
        observe.on_session_shutdown({}, _make_ctx())

    def _sidecar_path(self) -> str:
        return os.path.join(
            self.state_dir,
            "fir",
            "agents",
            f"{self.session_id}.json",
        )

    def _read_sidecar(self) -> dict:
        with open(self._sidecar_path()) as f:
            return json.load(f)

    def test_session_start_writes_sidecar_with_required_fields(self) -> None:
        ctx = _make_ctx(session_file="/tmp/foo.jsonl", session_name="my-feature")
        observe.on_session_start({"session_id": self.session_id}, ctx)

        self.assertTrue(os.path.exists(self._sidecar_path()))
        d = self._read_sidecar()
        self.assertEqual(d["session_id"], self.session_id)
        self.assertEqual(d["store_path"], "/tmp/foo.jsonl")
        self.assertEqual(d["session_name"], "my-feature")
        self.assertEqual(d["status"], "running")
        self.assertEqual(d["schema"], 1)
        self.assertEqual(d["pid"], os.getpid())
        self.assertEqual(d["host_pid"], observe._host_pid())
        self.assertTrue(d["socket_path"].endswith(".sock"))
        self.assertIn(self.session_id[:16], d["socket_path"])
        self.assertTrue(d["started_at"])  # non-empty timestamp

    def test_host_pid_from_env_var(self) -> None:
        """host_pid must come from FIR_HOST_PID (the fir binary), not getppid().

        Under the forkserver architecture os.getppid() returns the python
        forkserver pid, not the fir host, so signaling it does not stop the
        session. The fir host exports FIR_HOST_PID; the sidecar must record it.
        """
        # A pid distinct from getppid() so we prove the env var wins.
        fake_host = os.getppid() + 100000
        prev = os.environ.get("FIR_HOST_PID")
        os.environ["FIR_HOST_PID"] = str(fake_host)
        try:
            self.assertEqual(observe._host_pid(), fake_host)
            # Re-seed _state so the sidecar picks up the env-derived value.
            observe._state["host_pid"] = observe._host_pid()
            ctx = _make_ctx(session_file="/tmp/foo.jsonl")
            observe.on_session_start({"session_id": self.session_id}, ctx)
            d = self._read_sidecar()
            self.assertEqual(d["host_pid"], fake_host)
            self.assertNotEqual(d["host_pid"], os.getppid())
        finally:
            if prev is None:
                del os.environ["FIR_HOST_PID"]
            else:
                os.environ["FIR_HOST_PID"] = prev

    def test_host_pid_falls_back_to_getppid(self) -> None:
        """Without FIR_HOST_PID (old host / no SDK), fall back to getppid()."""
        prev = os.environ.pop("FIR_HOST_PID", None)
        try:
            self.assertEqual(observe._host_pid(), os.getppid())
        finally:
            if prev is not None:
                os.environ["FIR_HOST_PID"] = prev

    def test_session_start_skips_when_no_session_file(self) -> None:
        """In-memory sessions have no transcript; observe.py should bail."""
        ctx = _make_ctx(session_file="")
        observe.on_session_start({"session_id": self.session_id}, ctx)
        # No sidecar should be written.
        self.assertFalse(os.path.exists(self._sidecar_path()))

    def test_session_start_skips_when_no_session_id(self) -> None:
        ctx = _make_ctx()
        observe.on_session_start({}, ctx)
        # No sidecar written; session_id remains empty.
        self.assertEqual(observe._state["session_id"], "")

    def test_session_start_rejects_unsafe_session_id(self) -> None:
        """Defensive: path-traversal characters in session_id must be refused."""
        ctx = _make_ctx()
        for bad in ["../etc/passwd", "/abs/path", "with/slash", "with\x00null", ""]:
            with self.subTest(bad=bad):
                observe.on_session_start({"session_id": bad}, ctx)
                self.assertEqual(
                    observe._state["session_id"],
                    "",
                    f"unsafe session_id {bad!r} should be rejected",
                )

    def test_is_safe_session_id_accepts_uuids_and_alnum(self) -> None:
        for good in [
            "abc123",
            "abcd1234ef567890",
            "550e8400-e29b-41d4-a716-446655440000",  # canonical UUID
            "with_underscore",
            "MixedCase123",
        ]:
            self.assertTrue(observe._is_safe_session_id(good), good)

    def test_session_named_updates_sidecar(self) -> None:
        ctx = _make_ctx()
        observe.on_session_start({"session_id": self.session_id}, ctx)
        observe.on_session_named({"name": "renamed"}, ctx)
        self.assertEqual(self._read_sidecar()["session_name"], "renamed")

    def test_agent_lifecycle_toggles_status(self) -> None:
        ctx = _make_ctx()
        observe.on_session_start({"session_id": self.session_id}, ctx)
        observe.on_agent_end({}, ctx)
        self.assertEqual(self._read_sidecar()["status"], "idle")
        observe.on_agent_start({}, ctx)
        self.assertEqual(self._read_sidecar()["status"], "running")

    def test_session_shutdown_marks_ended_but_keeps_file(self) -> None:
        ctx = _make_ctx()
        observe.on_session_start({"session_id": self.session_id}, ctx)
        path = self._sidecar_path()
        observe.on_session_shutdown({}, ctx)
        # File still present (post-mortem); status flipped.
        self.assertTrue(os.path.exists(path))
        self.assertEqual(self._read_sidecar()["status"], "ended")

    def test_ended_is_terminal_and_survives_a_late_event(self) -> None:
        """A late handler must not resurrect a session that has already ended.

        Every event runs in its own SDK worker thread, so `agent_end` can land
        after `session_shutdown`. If it were allowed to rewrite status back to
        `idle`, observe would then classify the exited session as *crashed*
        (pid gone + non-terminal status).
        """
        ctx = _make_ctx()
        observe.on_session_start({"session_id": self.session_id}, ctx)
        observe.on_session_shutdown({}, ctx)
        self.assertEqual(self._read_sidecar()["status"], "ended")

        # Out-of-order arrivals, each of which used to clobber the terminal state.
        observe.on_agent_end({}, ctx)
        observe.on_agent_start({}, ctx)
        self.assertEqual(self._read_sidecar()["status"], "ended")

        # Non-status fields still land — late activity counters stay useful.
        observe.on_turn_end({}, ctx)
        d = self._read_sidecar()
        self.assertEqual(d["status"], "ended")
        self.assertEqual(d["activity"]["last_event_type"], "turn_end")

    def test_sidecar_is_atomic(self) -> None:
        """Writes go through tmp-rename so readers never see partial JSON."""
        ctx = _make_ctx()
        observe.on_session_start({"session_id": self.session_id}, ctx)
        # Sidecar should always be valid JSON.
        for _ in range(20):
            d = self._read_sidecar()
            self.assertIn("session_id", d)
            observe.on_agent_end({}, ctx)


# ---------------------------------------------------------------------------
# Socket tests
# ---------------------------------------------------------------------------


class TestSocket(unittest.TestCase):
    def setUp(self) -> None:
        self.state_dir = tempfile.mkdtemp(prefix="observe-test-state-")
        # Socket dir must be very short — Unix-domain socket paths cap at
        # ~104 bytes on macOS (sun_path), and tempfile.mkdtemp's default
        # location ($TMPDIR on macOS = /var/folders/..../T/) adds ~50 chars
        # of prefix before we even start. Use /tmp/o<pid> for safety;
        # production resolution falls back to $HOME-based paths.
        self.sock_dir = f"/tmp/o{os.getpid()}-{int(time.time() * 1000) % 10000}"
        os.makedirs(self.sock_dir, exist_ok=True)
        _reset_observe_state(self.state_dir, self.sock_dir)
        self.session_id = "abcd1234ef567890" + "0" * 20  # 36 chars

    def tearDown(self) -> None:
        observe.on_session_shutdown({}, _make_ctx())

    def _socket_path(self) -> str:
        return os.path.join(
            self.sock_dir,
            "fir",
            "observe",
            f"{self.session_id[:16]}.sock",
        )

    def _connect(self, timeout: float = 10.0) -> socket.socket:
        # Wait briefly for the accept thread to be up.
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if os.path.exists(self._socket_path()):
                break
            time.sleep(0.01)
        c = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        c.connect(self._socket_path())
        return c

    def test_socket_file_created_with_0600_perms(self) -> None:
        ctx = _make_ctx()
        observe.on_session_start({"session_id": self.session_id}, ctx)
        # Wait for bind.
        deadline = time.monotonic() + 10.0
        while time.monotonic() < deadline:
            if os.path.exists(self._socket_path()):
                break
            time.sleep(0.01)
        self.assertTrue(os.path.exists(self._socket_path()))
        mode = os.stat(self._socket_path()).st_mode & 0o777
        self.assertEqual(mode, 0o600, f"expected 0600, got {oct(mode)}")

    def test_ndjson_line_forwards_to_send_user_message(self) -> None:
        ctx = _make_ctx()
        observe.on_session_start({"session_id": self.session_id}, ctx)
        c = self._connect()
        try:
            payload = json.dumps({"deliver_as": "", "content": "hello agent"}) + "\n"
            c.sendall(payload.encode())
            # Give the accept/handler threads a moment.
            deadline = time.monotonic() + 10.0
            while time.monotonic() < deadline:
                if ctx.send_user_message.called:
                    break
                time.sleep(0.01)
        finally:
            c.close()
        ctx.send_user_message.assert_called_with("hello agent", deliver_as="")

    def test_steer_sigil_passes_through(self) -> None:
        ctx = _make_ctx()
        observe.on_session_start({"session_id": self.session_id}, ctx)
        c = self._connect()
        try:
            c.sendall(
                (
                    json.dumps({"deliver_as": "steer", "content": "stop, look at foo.go"}) + "\n"
                ).encode()
            )
            deadline = time.monotonic() + 10.0
            while time.monotonic() < deadline:
                if ctx.send_user_message.called:
                    break
                time.sleep(0.01)
        finally:
            c.close()
        ctx.send_user_message.assert_called_with(
            "stop, look at foo.go",
            deliver_as="steer",
        )

    def test_followup_sigil_passes_through(self) -> None:
        ctx = _make_ctx()
        observe.on_session_start({"session_id": self.session_id}, ctx)
        c = self._connect()
        try:
            c.sendall(
                (
                    json.dumps({"deliver_as": "followUp", "content": "and update CHANGELOG"}) + "\n"
                ).encode()
            )
            deadline = time.monotonic() + 10.0
            while time.monotonic() < deadline:
                if ctx.send_user_message.called:
                    break
                time.sleep(0.01)
        finally:
            c.close()
        ctx.send_user_message.assert_called_with(
            "and update CHANGELOG",
            deliver_as="followUp",
        )

    def test_abort_deliver_as_passes_through_with_empty_content(self) -> None:
        ctx = _make_ctx()
        observe.on_session_start({"session_id": self.session_id}, ctx)
        c = self._connect()
        try:
            # Abort carries no content — it cancels the current turn.
            c.sendall((json.dumps({"deliver_as": "abort", "content": ""}) + "\n").encode())
            deadline = time.monotonic() + 10.0
            while time.monotonic() < deadline:
                if ctx.send_user_message.called:
                    break
                time.sleep(0.01)
        finally:
            c.close()
        ctx.send_user_message.assert_called_with("", deliver_as="abort")

    def test_unknown_deliver_as_normalized_to_empty(self) -> None:
        ctx = _make_ctx()
        observe.on_session_start({"session_id": self.session_id}, ctx)
        c = self._connect()
        try:
            c.sendall((json.dumps({"deliver_as": "garbage", "content": "x"}) + "\n").encode())
            deadline = time.monotonic() + 10.0
            while time.monotonic() < deadline:
                if ctx.send_user_message.called:
                    break
                time.sleep(0.01)
        finally:
            c.close()
        # Unknown deliver_as values are normalized to "" (default Prompt path).
        ctx.send_user_message.assert_called_with("x", deliver_as="")

    def test_empty_lines_are_skipped(self) -> None:
        ctx = _make_ctx()
        observe.on_session_start({"session_id": self.session_id}, ctx)
        c = self._connect()
        try:
            # A blank line, then a malformed JSON line, then a valid one.
            c.sendall(b"\n")
            c.sendall(b"not json\n")
            c.sendall((json.dumps({"content": "real message"}) + "\n").encode())
            deadline = time.monotonic() + 10.0
            while time.monotonic() < deadline:
                if ctx.send_user_message.called:
                    break
                time.sleep(0.01)
        finally:
            c.close()
        # Only the valid line should have been forwarded.
        self.assertEqual(ctx.send_user_message.call_count, 1)
        ctx.send_user_message.assert_called_with("real message", deliver_as="")

    def test_socket_unlinked_on_shutdown(self) -> None:
        ctx = _make_ctx()
        observe.on_session_start({"session_id": self.session_id}, ctx)
        path = self._socket_path()
        # Wait for bind.
        deadline = time.monotonic() + 10.0
        while time.monotonic() < deadline:
            if os.path.exists(path):
                break
            time.sleep(0.01)
        self.assertTrue(os.path.exists(path))
        observe.on_session_shutdown({}, ctx)
        self.assertFalse(os.path.exists(path), "socket should be unlinked on shutdown")

    def test_concurrent_connections(self) -> None:
        """Two simultaneous observers can both inject messages."""
        ctx = _make_ctx()
        observe.on_session_start({"session_id": self.session_id}, ctx)
        # Wait for bind.
        deadline = time.monotonic() + 10.0
        while time.monotonic() < deadline:
            if os.path.exists(self._socket_path()):
                break
            time.sleep(0.01)

        def _client(text: str) -> None:
            c = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            c.connect(self._socket_path())
            c.sendall((json.dumps({"content": text}) + "\n").encode())
            c.close()

        threads = [threading.Thread(target=_client, args=(f"msg-{i}",)) for i in range(5)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()

        # Wait for handlers to drain.
        deadline = time.monotonic() + 10.0
        while time.monotonic() < deadline:
            if ctx.send_user_message.call_count >= 5:
                break
            time.sleep(0.01)
        self.assertEqual(ctx.send_user_message.call_count, 5)


class TestMetering(unittest.TestCase):
    """Verify the activity/usage/model fields observe.py writes to the sidecar."""

    def setUp(self) -> None:
        self.state_dir = tempfile.mkdtemp(prefix="observe-meter-state-")
        self.sock_dir = f"/tmp/om{os.getpid()}-{int(time.time() * 1000) % 10000}"
        os.makedirs(self.sock_dir, exist_ok=True)
        _reset_observe_state(self.state_dir, self.sock_dir)
        # Reset the nested counters too so each test starts clean.
        observe._state["activity"] = {
            "last_event": "",
            "last_event_type": "",
            "turns": 0,
            "messages": 0,
            "assistant_messages": 0,
            "tool_calls": 0,
            "tool_errors": 0,
        }
        observe._state["model"] = {"provider": "", "id": ""}
        observe._state["usage"] = {
            "input": 0,
            "output": 0,
            "cache_read": 0,
            "cache_write": 0,
            "total_tokens": 0,
            "requests": 0,
            "cost": {
                "input": 0.0,
                "output": 0.0,
                "cache_read": 0.0,
                "cache_write": 0.0,
                "total": 0.0,
            },
        }
        self.session_id = "meter1234ef567890" + "0" * 19  # 36 chars

    def tearDown(self) -> None:
        observe._shutdown.set()
        if observe._socket is not None:
            with suppress(Exception):
                observe._socket.close()
            observe._socket = None

    def _start(self) -> MagicMock:
        ctx = _make_ctx()
        observe.on_session_start({"session_id": self.session_id}, ctx)
        return ctx

    def _read_sidecar(self) -> dict:
        path = os.path.join(self.state_dir, "fir", "agents", f"{self.session_id}.json")
        with open(path, encoding="utf-8") as f:
            return json.load(f)

    def test_assistant_message_end_records_usage_and_model(self) -> None:
        self._start()
        observe.on_message_end(
            {
                "role": "assistant",
                "provider": "anthropic",
                "model": "claude-3-5-sonnet",
                "usage": {
                    "input": 100,
                    "output": 50,
                    "cache_read": 20,
                    "cache_write": 10,
                    "total_tokens": 180,
                    "cost": {
                        "input": 0.001,
                        "output": 0.002,
                        "cache_read": 0.0,
                        "cache_write": 0.0,
                        "total": 0.003,
                    },
                },
            },
            MagicMock(),
        )
        s = self._read_sidecar()
        self.assertEqual(s["model"], {"provider": "anthropic", "id": "claude-3-5-sonnet"})
        self.assertEqual(s["usage"]["input"], 100)
        self.assertEqual(s["usage"]["total_tokens"], 180)
        self.assertEqual(s["usage"]["requests"], 1)
        self.assertAlmostEqual(s["usage"]["cost"]["total"], 0.003, places=6)
        self.assertEqual(s["activity"]["assistant_messages"], 1)
        self.assertEqual(s["activity"]["messages"], 1)
        self.assertEqual(s["activity"]["last_event_type"], "message_end")
        self.assertTrue(s["activity"]["last_event"], "last_event timestamp must be set")

    def test_user_message_end_does_not_touch_usage(self) -> None:
        self._start()
        observe.on_message_end({"role": "user"}, MagicMock())
        s = self._read_sidecar()
        self.assertEqual(s["usage"]["requests"], 0)
        self.assertEqual(s["activity"]["assistant_messages"], 0)
        self.assertEqual(s["activity"]["messages"], 1)

    def test_usage_accumulates_across_calls(self) -> None:
        self._start()
        for _ in range(3):
            observe.on_message_end(
                {
                    "role": "assistant",
                    "provider": "openai",
                    "model": "gpt-4",
                    "usage": {
                        "input": 10,
                        "output": 5,
                        "cache_read": 0,
                        "cache_write": 0,
                        "total_tokens": 15,
                        "cost": {
                            "input": 0,
                            "output": 0,
                            "cache_read": 0,
                            "cache_write": 0,
                            "total": 0.01,
                        },
                    },
                },
                MagicMock(),
            )
        s = self._read_sidecar()
        self.assertEqual(s["usage"]["input"], 30)
        self.assertEqual(s["usage"]["total_tokens"], 45)
        self.assertEqual(s["usage"]["requests"], 3)
        self.assertAlmostEqual(s["usage"]["cost"]["total"], 0.03, places=6)

    def test_tool_execution_end_counts_calls_and_errors(self) -> None:
        self._start()
        observe.on_tool_execution_end({"is_error": False}, MagicMock())
        observe.on_tool_execution_end({"is_error": True}, MagicMock())
        observe.on_tool_execution_end({"is_error": False}, MagicMock())
        s = self._read_sidecar()
        self.assertEqual(s["activity"]["tool_calls"], 3)
        self.assertEqual(s["activity"]["tool_errors"], 1)

    def test_concurrent_writes_do_not_corrupt_sidecar(self) -> None:
        """Regression: shallow snapshot + json.dumps outside lock would
        race on nested dicts. With the fix (json.dumps inside lock),
        many concurrent event handlers still produce valid JSON."""
        self._start()

        def _hammer(n: int) -> None:
            for _ in range(50):
                observe.on_message_end(
                    {
                        "role": "assistant",
                        "provider": "p",
                        "model": "m",
                        "usage": {
                            "input": 1,
                            "output": 1,
                            "cache_read": 0,
                            "cache_write": 0,
                            "total_tokens": 2,
                            "cost": {
                                "input": 0,
                                "output": 0,
                                "cache_read": 0,
                                "cache_write": 0,
                                "total": 0.0001,
                            },
                        },
                    },
                    MagicMock(),
                )
                observe.on_tool_execution_end({"is_error": False}, MagicMock())

        threads = [threading.Thread(target=_hammer, args=(i,)) for i in range(8)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()

        # Sidecar must parse cleanly and counters must equal totals.
        s = self._read_sidecar()
        self.assertEqual(s["usage"]["requests"], 8 * 50)
        self.assertEqual(s["usage"]["total_tokens"], 8 * 50 * 2)
        self.assertEqual(s["activity"]["tool_calls"], 8 * 50)
        self.assertEqual(s["activity"]["assistant_messages"], 8 * 50)


# ---------------------------------------------------------------------------
# CLI verb tests — formatter, sigil parser, age formatter, arg parser
# ---------------------------------------------------------------------------


class TestFormatter(unittest.TestCase):
    def _fmt(self, **kw):
        return observe._Formatter(
            raw_json=kw.get("raw_json", False),
            color=kw.get("color", False),
        )

    def test_raw_json_passthrough(self):
        line = '{"type":"message","timestamp":"2026-04-27T12:00:00Z"}'
        self.assertEqual(self._fmt(raw_json=True).render(line), line)

    def test_session_header(self):
        line = '{"type":"session","version":3,"id":"abcdef0123","cwd":"/path"}'
        out = self._fmt().render(line)
        self.assertIn("session abcdef01", out)
        self.assertIn("v3", out)

    def test_user_message(self):
        line = (
            '{"type":"message","timestamp":"2026-04-27T12:00:00Z",'
            '"message":{"role":"user","content":"hi there"}}'
        )
        out = self._fmt().render(line)
        self.assertIn("user", out)
        self.assertIn("hi there", out)

    def test_assistant_with_tool_use(self):
        line = (
            '{"type":"message","timestamp":"2026-04-27T12:00:00Z","message":'
            '{"role":"assistant","content":'
            '[{"type":"text","text":"thinking..."},{"type":"tool_use","name":"bash"}]}}'
        )
        out = self._fmt().render(line)
        self.assertIn("assistant", out)
        self.assertIn("thinking", out)
        self.assertIn("→ bash", out)

    def test_model_change(self):
        line = (
            '{"type":"model_change","timestamp":"2026-04-27T12:00:00Z",'
            '"provider":"anthropic","modelId":"claude-opus-4"}'
        )
        out = self._fmt().render(line)
        self.assertIn("model →", out)
        self.assertIn("anthropic", out)

    def test_compaction(self):
        line = (
            '{"type":"compaction","timestamp":"2026-04-27T12:00:00Z",'
            '"summary":"compressed 50 turns"}'
        )
        out = self._fmt().render(line)
        self.assertIn("compaction", out)
        self.assertIn("50 turns", out)

    def test_plan_update(self):
        line = (
            '{"type":"plan_update","timestamp":"2026-04-27T12:00:00Z",'
            '"planTitle":"Implement caching"}'
        )
        self.assertIn("Implement caching", self._fmt().render(line))

    def test_command(self):
        line = (
            '{"type":"command","timestamp":"2026-04-27T12:00:00Z",'
            '"command":"bash","args":"go test ./..."}'
        )
        out = self._fmt().render(line)
        self.assertIn("bash", out)
        self.assertIn("go test", out)

    def test_hidden_types_return_none(self):
        for ty in ("label", "branch_summary", "custom", "custom_message"):
            line = f'{{"type":"{ty}","timestamp":"2026-04-27T12:00:00Z"}}'
            self.assertIsNone(self._fmt().render(line), f"type {ty} should be suppressed")

    def test_unknown_type_renders_name(self):
        line = '{"type":"future_type","timestamp":"2026-04-27T12:00:00Z"}'
        self.assertIn("future_type", self._fmt().render(line))


class TestSummariseContent(unittest.TestCase):
    def test_strings_collapse_newlines(self):
        self.assertEqual(
            observe._summarise_content("hello\nworld\nlong text"),
            "hello world long text",
        )

    def test_no_truncation(self):
        long = "x" * 500
        self.assertEqual(observe._summarise_content(long), long)


class TestEncodeSend(unittest.TestCase):
    def _decode(self, b) -> dict:
        assert b is not None
        return json.loads(b.decode().rstrip("\n"))

    def test_basic(self):
        d = self._decode(observe._encode_send("hello agent", ""))
        self.assertEqual(d, {"deliver_as": "", "content": "hello agent"})

    def test_bang_sigil_steer(self):
        d = self._decode(observe._encode_send("!stop, read foo.go", ""))
        self.assertEqual(d["deliver_as"], "steer")
        self.assertEqual(d["content"], "stop, read foo.go")

    def test_plus_sigil_followup(self):
        d = self._decode(observe._encode_send("+also update changelog", ""))
        self.assertEqual(d["deliver_as"], "followUp")
        self.assertEqual(d["content"], "also update changelog")

    def test_escaped_bang(self):
        d = self._decode(observe._encode_send("\\!literal bang", ""))
        self.assertEqual(d["deliver_as"], "")
        self.assertEqual(d["content"], "!literal bang")

    def test_escaped_plus(self):
        d = self._decode(observe._encode_send("\\+literal plus", ""))
        self.assertEqual(d["content"], "+literal plus")

    def test_tilde_sigil_abort(self):
        d = self._decode(observe._encode_send("~", ""))
        self.assertEqual(d["deliver_as"], "abort")
        self.assertEqual(d["content"], "")

    def test_tilde_sigil_abort_ignores_trailing_text(self):
        d = self._decode(observe._encode_send("~ whatever", "steer"))
        self.assertEqual(d["deliver_as"], "abort")
        self.assertEqual(d["content"], "")

    def test_escaped_tilde(self):
        d = self._decode(observe._encode_send("\\~literal tilde", ""))
        self.assertEqual(d["deliver_as"], "")
        self.assertEqual(d["content"], "~literal tilde")

    def test_default_deliver_as_steer(self):
        d = self._decode(observe._encode_send("no sigil", "steer"))
        self.assertEqual(d["deliver_as"], "steer")

    def test_sigil_overrides_default(self):
        d = self._decode(observe._encode_send("+override", "steer"))
        self.assertEqual(d["deliver_as"], "followUp")

    def test_empty_returns_none(self):
        self.assertIsNone(observe._encode_send("   ", ""))
        self.assertIsNone(observe._encode_send("", ""))


class TestAgeString(unittest.TestCase):
    def test_formats(self):
        # Use a fixed reference time so tests are deterministic.
        ref = datetime(2026, 4, 27, 12, 0, 0, tzinfo=timezone.utc).timestamp()

        def t(seconds: int) -> str:
            dt = datetime.fromtimestamp(ref - seconds, tz=timezone.utc)
            return dt.strftime("%Y-%m-%dT%H:%M:%SZ")

        self.assertEqual(observe._age_string(t(30), ref), "30s")
        self.assertEqual(observe._age_string(t(5 * 60), ref), "5m00s")
        self.assertEqual(observe._age_string(t(2 * 3600 + 30 * 60), ref), "2h30m")
        self.assertEqual(observe._age_string(t(49 * 3600), ref), "2d")
        self.assertEqual(observe._age_string("not-a-date", ref), "?")
        self.assertEqual(observe._age_string("", ref), "?")


class TestArgParsers(unittest.TestCase):
    def test_observe_no_args(self):
        o = observe._parse_observe_args([])
        self.assertIsNone(o.error)
        self.assertEqual((o.id_prefix, o.cwd, o.lines, o.follow), ("", "", 0, False))

    def test_observe_id_prefix(self):
        o = observe._parse_observe_args(["abc"])
        self.assertIsNone(o.error)
        self.assertEqual(o.id_prefix, "abc")

    def test_observe_flags(self):
        o = observe._parse_observe_args(["abc", "--json", "--interact"])
        self.assertIsNone(o.error)
        self.assertTrue(o.json_out)
        self.assertTrue(o.interact)

    def test_observe_all_flag(self):
        self.assertTrue(observe._parse_observe_args(["--all"]).include_all)

    def test_observe_cwd(self):
        self.assertEqual(observe._parse_observe_args(["--cwd", "/path"]).cwd, "/path")
        self.assertEqual(observe._parse_observe_args(["--cwd=/path"]).cwd, "/path")
        self.assertIn("requires", observe._parse_observe_args(["--cwd"]).error or "")

    def test_observe_snapshot_flags(self):
        o = observe._parse_observe_args(
            ["abc", "-n", "7", "--status", "--wait", "--timeout", "90s"]
        )
        self.assertIsNone(o.error)
        self.assertEqual((o.lines, o.status, o.wait, o.timeout), (7, True, True, 90.0))
        self.assertEqual(observe._parse_observe_args(["abc", "--lines=12"]).lines, 12)
        self.assertTrue(observe._parse_observe_args(["abc", "-f"]).follow)
        self.assertTrue(observe._parse_observe_args(["abc", "--follow"]).follow)

    def test_observe_bad_values(self):
        for argv in (["a", "-n"], ["a", "-n", "x"], ["a", "--lines=0"], ["a", "--timeout", "soon"]):
            self.assertIsNotNone(observe._parse_observe_args(argv).error, argv)
        self.assertIn(
            "cannot be combined", observe._parse_observe_args(["a", "-f", "--wait"]).error or ""
        )
        self.assertIn(
            "cannot be combined",
            observe._parse_observe_args(["a", "--interact", "--status"]).error or "",
        )

    def test_observe_unknown_flag(self):
        self.assertIn("unknown flag", observe._parse_observe_args(["--bogus"]).error or "")

    def test_observe_extra_arg(self):
        self.assertIn("extra argument", observe._parse_observe_args(["a", "b"]).error or "")

    def test_observe_help(self):
        self.assertEqual(observe._parse_observe_args(["--help"]).error, "__HELP__")

    def test_send_no_args_required(self):
        self.assertIn("required", observe._parse_send_args([]).error or "")

    def test_send_unknown_flag(self):
        self.assertIn("unknown flag", observe._parse_send_args(["--bogus"]).error or "")

    def test_send_conflicting_flags(self):
        o = observe._parse_send_args(["--steer", "--follow", "abc"])
        self.assertIn("mutually exclusive", o.error or "")

    def test_send_steer_default(self):
        o = observe._parse_send_args(["--steer", "abc"])
        self.assertIsNone(o.error)
        self.assertEqual(o.deliver_as, "steer")

    def test_send_follow_default(self):
        o = observe._parse_send_args(["--follow", "abc"])
        self.assertIsNone(o.error)
        self.assertEqual(o.deliver_as, "followUp")

    def test_send_positional_message(self):
        # Regression: `fir send <id> 'text'` used to fail with
        # "unexpected extra argument".
        o = observe._parse_send_args(["abc", "fix the bug"])
        self.assertIsNone(o.error)
        self.assertEqual((o.id_prefix, o.message), ("abc", "fix the bug"))
        o = observe._parse_send_args(["abc", "--wait", "--timeout=5m", "run", "the", "tests"])
        self.assertEqual((o.message, o.wait, o.timeout), ("run the tests", True, 300.0))

    def test_send_flags_after_message_are_text(self):
        o = observe._parse_send_args(["abc", "explain", "--steer", "flag"])
        self.assertIsNone(o.error)
        self.assertEqual((o.message, o.deliver_as), ("explain --steer flag", ""))
        o = observe._parse_send_args(["abc", "--", "--literal"])
        self.assertEqual(o.message, "--literal")

    def test_send_cwd_then_message(self):
        o = observe._parse_send_args(["--cwd", ".", "hello"])
        self.assertIsNone(o.error)
        self.assertEqual((o.cwd, o.id_prefix, o.message), (".", "", "hello"))

    def test_send_abort_rejects_message(self):
        self.assertIn("no message", observe._parse_send_args(["abc", "--abort", "x"]).error or "")
        self.assertEqual(observe._parse_send_args(["abc", "--abort"]).deliver_as, "abort")


class TestHtopHelpers(unittest.TestCase):
    """Tests for the formatting + parsing helpers backing the `fir htop`
    cli verb. They're pure functions so we exercise them directly."""

    def test_format_tokens(self) -> None:
        self.assertEqual(observe._format_tokens(0), "-")
        self.assertEqual(observe._format_tokens(-5), "-")
        self.assertEqual(observe._format_tokens(42), "42")
        self.assertEqual(observe._format_tokens(1234), "1.2k")
        self.assertEqual(observe._format_tokens(2_500_000), "2.5M")
        self.assertEqual(observe._format_tokens(3_400_000_000), "3.4G")

    def test_format_cost(self) -> None:
        self.assertEqual(observe._format_cost(0), "-")
        self.assertEqual(observe._format_cost(0.005), "<$.01")
        self.assertEqual(observe._format_cost(1.234), "$1.23")
        self.assertEqual(observe._format_cost(250.0), "$250")

    def test_format_tools(self) -> None:
        self.assertEqual(observe._format_tools(0, 0), "-")
        self.assertEqual(observe._format_tools(7, 0), "7")
        self.assertEqual(observe._format_tools(7, 2), "7/2")

    def test_format_model(self) -> None:
        self.assertEqual(observe._format_model("anthropic", "claude"), "anthropic/claude")
        self.assertEqual(observe._format_model("", "claude"), "claude")
        self.assertEqual(observe._format_model("anthropic", ""), "anthropic")
        self.assertEqual(observe._format_model("", ""), "-")

    def test_parse_duration(self) -> None:
        self.assertEqual(observe._parse_duration("500ms"), 0.5)
        self.assertEqual(observe._parse_duration("2s"), 2.0)
        self.assertEqual(observe._parse_duration("1m"), 60.0)
        self.assertEqual(observe._parse_duration("3"), 3.0)
        with self.assertRaises(ValueError):
            observe._parse_duration("")
        with self.assertRaises(ValueError):
            observe._parse_duration("abc")

    def test_parse_htop_args_default(self) -> None:
        interval, err = observe._parse_htop_args([])
        self.assertIsNone(err)
        self.assertEqual(interval, 1.0)

    def test_parse_htop_args_help(self) -> None:
        _, err = observe._parse_htop_args(["--help"])
        self.assertEqual(err, "__HELP__")

    def test_parse_htop_args_interval_separate(self) -> None:
        interval, err = observe._parse_htop_args(["--interval", "500ms"])
        self.assertIsNone(err)
        self.assertEqual(interval, 0.5)

    def test_parse_htop_args_interval_equals(self) -> None:
        interval, err = observe._parse_htop_args(["--interval=2s"])
        self.assertIsNone(err)
        self.assertEqual(interval, 2.0)

    def test_parse_htop_args_short_flag(self) -> None:
        interval, err = observe._parse_htop_args(["-n", "3s"])
        self.assertIsNone(err)
        self.assertEqual(interval, 3.0)

    def test_parse_htop_args_clamp_below_min(self) -> None:
        interval, err = observe._parse_htop_args(["--interval", "10ms"])
        self.assertIsNone(err)
        self.assertEqual(interval, 0.1)

    def test_parse_htop_args_unknown_flag(self) -> None:
        _, err = observe._parse_htop_args(["--bogus"])
        self.assertIsNotNone(err)
        assert err is not None
        self.assertIn("unknown", err)

    def test_parse_htop_args_missing_value(self) -> None:
        _, err = observe._parse_htop_args(["--interval"])
        self.assertIsNotNone(err)

    def test_parse_htop_args_invalid_duration(self) -> None:
        _, err = observe._parse_htop_args(["--interval", "abc"])
        self.assertIsNotNone(err)

    def test_last_activity_uses_sidecar_timestamp(self) -> None:
        # 2 minutes ago in UTC. We construct the iso-8601 'Z' string from
        # gmtime so the test is independent of the host timezone.
        now = time.time()
        ts_str = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(now - 120))
        s = {"activity": {"last_event": ts_str}}
        self.assertEqual(observe._last_activity_string(s, now), "2m")

    def test_last_activity_dst_safe(self) -> None:
        """Regression: the previous implementation parsed UTC strings via
        time.mktime (local) and subtracted time.timezone (standard offset),
        which slips by 1 hour during DST. We assert the helper returns
        a sub-minute reading for an event marked 'now in UTC' regardless
        of the host's DST state.
        """
        now = time.time()
        ts_str = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(now))
        s = {"activity": {"last_event": ts_str}}
        out = observe._last_activity_string(s, now)
        # The DST bug would surface as ~"59m" or "1h"; a healthy reading
        # is "now" or "Ns" with N small.
        self.assertIn(out, ("now", "0s", "1s"), f"DST bug? got {out!r}")

    def test_last_activity_falls_back_to_mtime(self) -> None:
        with tempfile.NamedTemporaryFile(delete=False) as tmp:
            tmp.write(b"")
            path = tmp.name
        try:
            now = time.time()
            os.utime(path, (now - 90, now - 90))  # 90s ago
            s = {"store_path": path, "activity": {"last_event": ""}}
            self.assertEqual(observe._last_activity_string(s, now), "1m")
        finally:
            os.unlink(path)

    def test_last_activity_returns_dash_when_unavailable(self) -> None:
        s: dict[str, object] = {"activity": {}, "store_path": ""}
        self.assertEqual(observe._last_activity_string(s, time.time()), "-")

    def test_htop_render_empty(self) -> None:
        out = observe._htop_render([], color=False)
        self.assertIn("0 sessions", out)
        self.assertIn("no fir sessions found", out)
        # ANSI clear should be present.
        self.assertTrue(out.startswith("\x1b[H\x1b[2J"))

    def test_htop_render_populated(self) -> None:
        sidecars = [
            {
                "session_id": "deadbeefcafe1234",
                "session_name": "demo",
                "cwd": "/tmp/work",
                "status": "running",
                "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                "store_path": "",
                "model": {"provider": "anthropic", "id": "claude"},
                "usage": {"total_tokens": 12345, "cost": {"total": 1.23}},
                "activity": {
                    "tool_calls": 7,
                    "tool_errors": 2,
                    "last_event": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                },
            }
        ]
        out = observe._htop_render(sidecars, color=False)
        self.assertIn("1 session ", out)  # singular, no trailing 's'
        self.assertIn("deadbeef", out)  # truncated id
        self.assertIn("anthropic/claude", out)
        self.assertIn("12.3k", out)
        self.assertIn("$1.23", out)
        self.assertIn("7/2", out)


class TestCards(unittest.TestCase):
    """Verify the observable-cards reader path through observe.py."""

    def setUp(self) -> None:
        self.tmpdir = tempfile.mkdtemp(prefix="observe-cards-")
        self.session_id = "abc1234567890def" + "0" * 20
        _reset_observe_state(self.tmpdir, self.tmpdir)
        # The state dir resolver appends fir/agents — that's where the
        # observe extension looks for sidecars, so create it now.
        self.sidecar_dir = os.path.join(self.tmpdir, "fir", "agents")
        os.makedirs(self.sidecar_dir, exist_ok=True)

    def tearDown(self) -> None:
        with suppress(FileNotFoundError):
            shutil.rmtree(self.tmpdir, ignore_errors=True)

    def _write_cards(self, cards: list) -> str:
        """Write a cards-file to disk and return its path.

        ``cards`` is loosely typed because some test cases include
        deliberately malformed entries (non-dicts) to exercise the
        defensive filter in observe._read_cards.
        """
        path = os.path.join(self.tmpdir, "session.jsonl.cards")
        with open(path, "w", encoding="utf-8") as f:
            json.dump(cards, f)
        return path

    def _plant_sidecar(self, store_path: str) -> None:
        """Put a sidecar in the state dir so _resolve_sidecar finds us."""
        sidecar = os.path.join(self.sidecar_dir, f"{self.session_id}.json")
        with open(sidecar, "w") as f:
            json.dump(
                {
                    "session_id": self.session_id,
                    "cwd": self.tmpdir,
                    "store_path": store_path,
                    "cards_path": store_path + ".cards",
                    "status": "running",
                    "started_at": "2026-01-01T00:00:00Z",
                    "pid": os.getpid(),
                    "host_pid": os.getppid(),
                    "socket_path": "",
                    "schema": 1,
                },
                f,
            )

    def test_session_start_records_cards_path(self) -> None:
        """observe sidecar must publish cards_path next to store_path."""
        ctx = _make_ctx(session_file=os.path.join(self.tmpdir, "session.jsonl"))
        observe.on_session_start({"session_id": self.session_id}, ctx)
        with open(os.path.join(self.sidecar_dir, f"{self.session_id}.json")) as f:
            d = json.load(f)
        self.assertEqual(
            d.get("cards_path"),
            os.path.join(self.tmpdir, "session.jsonl.cards"),
        )

    def test_read_cards_missing_file_returns_empty(self) -> None:
        self.assertEqual(observe._read_cards(""), [])
        self.assertEqual(observe._read_cards("/does/not/exist"), [])

    def test_read_cards_filters_malformed_entries(self) -> None:
        path = self._write_cards(
            [
                {"source": "plan", "key": "active", "slug": "ok", "ts": "2026-01-01T00:00:00Z"},
                {"source": "plan"},  # missing key — dropped
                {"key": "k"},  # missing source — dropped
                "not an object",  # dropped
            ]
        )
        got = observe._read_cards(path)
        self.assertEqual(len(got), 1)
        self.assertEqual(got[0]["slug"], "ok")

    def test_read_cards_corrupt_returns_empty(self) -> None:
        path = os.path.join(self.tmpdir, "cards.json")
        with open(path, "w") as f:
            f.write("not json at all")
        self.assertEqual(observe._read_cards(path), [])

    def test_header_orders_priority_sources_first(self) -> None:
        cards = [
            {"source": "zalpha", "key": "k", "slug": "z-slug", "ts": "2026-01-01T00:00:01Z"},
            {"source": "plan", "key": "k", "slug": "3/8", "ts": "2026-01-01T00:00:02Z"},
            {"source": "mood", "key": "k", "slug": "#engaged", "ts": "2026-01-01T00:00:03Z"},
        ]
        header = observe._render_cards_header(cards)
        # plan first (priority 0), then mood (priority 1), then zalpha
        # (everything else). The order is fixed regardless of timestamp.
        self.assertEqual(
            header,
            "plan: 3/8  ·  mood: #engaged  ·  zalpha: z-slug",
        )

    def test_header_truncates_to_limit_with_more_marker(self) -> None:
        # Five sources, header limit is 3, so two should collapse into
        # the "…+2 more (--ext)" suffix.
        cards = [
            {"source": "plan", "key": "k", "slug": "p", "ts": "2026-01-01T00:00:01Z"},
            {"source": "mood", "key": "k", "slug": "m", "ts": "2026-01-01T00:00:02Z"},
            {"source": "model", "key": "k", "slug": "M", "ts": "2026-01-01T00:00:03Z"},
            {"source": "extA", "key": "k", "slug": "a", "ts": "2026-01-01T00:00:04Z"},
            {"source": "extB", "key": "k", "slug": "b", "ts": "2026-01-01T00:00:05Z"},
        ]
        header = observe._render_cards_header(cards)
        self.assertIn("plan: p", header)
        self.assertIn("mood: m", header)
        self.assertIn("model: M", header)
        self.assertIn("…+2 more (--ext)", header)

    def test_header_collapses_multiple_keys_per_source(self) -> None:
        cards = [
            {"source": "plan", "key": "a", "slug": "old", "ts": "2026-01-01T00:00:01Z"},
            {"source": "plan", "key": "b", "slug": "newer", "ts": "2026-01-01T00:00:02Z"},
        ]
        header = observe._render_cards_header(cards)
        # The newer card wins; one line only.
        self.assertEqual(header, "plan: newer")

    def test_header_empty_for_no_cards(self) -> None:
        self.assertEqual(observe._render_cards_header([]), "")

    def test_render_card_detail_for_one_source(self) -> None:
        cards = [
            {
                "source": "plan",
                "key": "active",
                "slug": "3/8",
                "detail": "Step three",
                "ts": "2026-01-01T00:00:00Z",
                "entry_id": "tc-1",
            },
            {
                "source": "mood",
                "key": "current",
                "slug": "#engaged",
                "detail": "Feeling good",
                "ts": "2026-01-01T00:00:00Z",
            },
        ]
        plan_detail = observe._render_card_detail(cards, "plan")
        self.assertIn("== cards: plan ==", plan_detail)
        self.assertIn("[active] 3/8", plan_detail)
        self.assertIn("entry=tc-1", plan_detail)
        self.assertIn("Step three", plan_detail)
        # mood not included.
        self.assertNotIn("#engaged", plan_detail)

    def test_render_card_detail_unknown_source(self) -> None:
        self.assertIn(
            "(no cards for source:",
            observe._render_card_detail([], "ghost"),
        )

    def _write_minimal_session(self, path: str, include_message: bool = True) -> None:
        """Write a minimal session JSONL with header and (optionally) one message."""
        header = {
            "type": "session",
            "version": 3,
            "id": self.session_id,
            "timestamp": "2026-01-01T00:00:00Z",
            "cwd": self.tmpdir,
        }
        msg_entry = {
            "type": "message",
            "id": "e1",
            "parentId": "",
            "timestamp": "2026-01-01T00:00:01Z",
            "message": {"role": "user", "content": "hello", "timestamp": 0},
        }
        with open(path, "w") as f:
            f.write(json.dumps(header) + "\n")
            if include_message:
                f.write(json.dumps(msg_entry) + "\n")

    def test_snapshot_transcript_prepends_card_header(self) -> None:
        """End-to-end smoke: a session with cards + transcript renders both."""
        store_path = os.path.join(self.tmpdir, "session.jsonl")
        self._write_minimal_session(store_path)
        self._write_cards(
            [
                {
                    "source": "plan",
                    "key": "active",
                    "slug": "1/3 in_progress",
                    "detail": "Step 1",
                    "ts": "2026-01-01T00:00:00Z",
                },
                {
                    "source": "mood",
                    "key": "current",
                    "slug": "#engaged",
                    "detail": "good",
                    "ts": "2026-01-01T00:00:01Z",
                },
            ]
        )
        self._plant_sidecar(store_path)
        out = observe._snapshot_transcript(self.session_id, "", 10, False)
        self.assertIn("plan: 1/3 in_progress", out)
        self.assertIn("mood: #engaged", out)
        # transcript line still rendered.
        self.assertIn("hello", out)

    def test_snapshot_transcript_raw_json_includes_cards_array(self) -> None:
        store_path = os.path.join(self.tmpdir, "session.jsonl")
        self._write_minimal_session(store_path, include_message=False)
        self._write_cards(
            [
                {
                    "source": "plan",
                    "key": "active",
                    "slug": "1/3",
                    "detail": "x",
                    "ts": "2026-01-01T00:00:00Z",
                },
            ]
        )
        self._plant_sidecar(store_path)
        out = observe._snapshot_transcript(self.session_id, "", 10, True)
        # The first section must be the cards JSON object.
        self.assertIn('"cards"', out)
        self.assertIn('"source": "plan"', out)

    def test_snapshot_transcript_ext_expands_one_source(self) -> None:
        store_path = os.path.join(self.tmpdir, "session.jsonl")
        self._write_minimal_session(store_path, include_message=False)
        self._write_cards(
            [
                {
                    "source": "plan",
                    "key": "active",
                    "slug": "1/3",
                    "detail": "EXPAND ME",
                    "ts": "2026-01-01T00:00:00Z",
                },
                {
                    "source": "mood",
                    "key": "current",
                    "slug": "x",
                    "detail": "not shown",
                    "ts": "2026-01-01T00:00:00Z",
                },
            ]
        )
        self._plant_sidecar(store_path)
        out = observe._snapshot_transcript(
            self.session_id,
            "",
            10,
            False,
            ext="plan",
        )
        self.assertIn("EXPAND ME", out)
        self.assertNotIn("not shown", out)

    def _write_multi_line_session(self, path: str, n: int) -> None:
        """Write a session header + n user messages, one JSONL record each."""
        header = {
            "type": "session",
            "version": 3,
            "id": self.session_id,
            "timestamp": "2026-01-01T00:00:00Z",
            "cwd": self.tmpdir,
        }
        with open(path, "w") as f:
            f.write(json.dumps(header) + "\n")
            for k in range(n):
                f.write(
                    json.dumps(
                        {
                            "type": "message",
                            "id": f"e{k}",
                            "parentId": "",
                            "timestamp": "2026-01-01T00:00:01Z",
                            "message": {"role": "user", "content": f"msg{k}", "timestamp": 0},
                        }
                    )
                    + "\n"
                )

    def test_read_line_range_inclusive(self) -> None:
        store_path = os.path.join(self.tmpdir, "session.jsonl")
        self._write_multi_line_session(store_path, 5)  # 1 header + 5 msgs = 6 lines
        sliced, total = observe._read_line_range(store_path, 2, 4)
        self.assertEqual(total, 6)
        self.assertEqual(len(sliced), 3)  # lines 2,3,4
        self.assertIn("msg0", sliced[0])  # line 2 is first message
        self.assertIn("msg2", sliced[2])

    def test_read_line_range_to_eof(self) -> None:
        store_path = os.path.join(self.tmpdir, "session.jsonl")
        self._write_multi_line_session(store_path, 3)  # 4 lines
        sliced, total = observe._read_line_range(store_path, 3, 0)
        self.assertEqual(total, 4)
        self.assertEqual(len(sliced), 2)  # lines 3,4

    def test_snapshot_transcript_range_slice(self) -> None:
        store_path = os.path.join(self.tmpdir, "session.jsonl")
        self._write_multi_line_session(store_path, 5)
        self._plant_sidecar(store_path)
        out = observe._snapshot_transcript(
            self.session_id,
            "",
            50,
            False,
            start=2,
            end=3,
        )
        self.assertIn("transcript lines 2-3 of 6", out)
        self.assertIn("msg0", out)
        self.assertIn("msg1", out)
        self.assertNotIn("msg3", out)

    def test_snapshot_transcript_default_is_tail(self) -> None:
        """No start => unchanged tail behaviour, no range note."""
        store_path = os.path.join(self.tmpdir, "session.jsonl")
        self._write_multi_line_session(store_path, 3)
        self._plant_sidecar(store_path)
        out = observe._snapshot_transcript(self.session_id, "", 50, False)
        self.assertNotIn("transcript lines", out)
        self.assertIn("msg2", out)


# ---------------------------------------------------------------------------
# Remote-drivable observe/send: status card, snapshot mode, send --wait
# ---------------------------------------------------------------------------


class _FakeHost:
    """Minimal stand-in for fir_ext.Host used by the CLI verbs."""

    def __init__(self, stdin: "list[str] | None" = None, tty: bool = False) -> None:
        self.out: list[str] = []
        self.err: list[str] = []
        self._stdin = list(stdin or [])
        self.stdin_is_tty = tty
        self.stdout_is_tty = tty
        self.stderr_is_tty = tty

    def print(self, *a, sep=" ", end=""):
        self.out.append(sep.join(str(x) for x in a) + end)

    def println(self, *a, sep=" "):
        self.print(*a, sep=sep, end="\n")

    def eprint(self, *a, sep=" ", end=""):
        self.err.append(sep.join(str(x) for x in a) + end)

    def eprintln(self, *a, sep=" "):
        self.eprint(*a, sep=sep, end="\n")

    def readline(self, timeout=None):
        return self._stdin.pop(0) if self._stdin else None

    def wake(self):
        pass

    @property
    def stdout(self) -> str:
        return "".join(self.out)

    @property
    def stderr(self) -> str:
        return "".join(self.err)


def _status_card(status: str, ts: str = "2026-01-01T00:00:00.123456789Z", **extra: str) -> dict:
    lines = [f"status: {status}"] + [f"{k}: {v}" for k, v in extra.items()]
    return {
        "source": "session",
        "key": "status",
        "slug": status,
        "detail": "\n".join(lines),
        "ts": ts,
    }


def _now_card_ts() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f") + "123Z"


class TestRemoteDriving(unittest.TestCase):
    def setUp(self) -> None:
        # Short root: AF_UNIX paths are capped at ~104 bytes on macOS.
        self.tmpdir = tempfile.mkdtemp(prefix="obs-", dir="/tmp")
        self.session_id = "feedface" + "1" * 28
        _reset_observe_state(self.tmpdir, self.tmpdir)
        self.sidecar_dir = os.path.join(self.tmpdir, "fir", "agents")
        os.makedirs(self.sidecar_dir, exist_ok=True)
        self.store = os.path.join(self.tmpdir, "s.jsonl")
        with open(self.store, "w") as f:
            f.write(json.dumps({"type": "session", "version": 3, "id": self.session_id}) + "\n")
        self.sock_path = ""
        observe._verb_stop.clear()

    def tearDown(self) -> None:
        observe._verb_stop.clear()
        shutil.rmtree(self.tmpdir, ignore_errors=True)

    # -- fixtures --------------------------------------------------------

    def _sidecar(self, status: str = "idle", **extra) -> None:
        d = {
            "session_id": self.session_id,
            "session_name": "worker",
            "cwd": self.tmpdir,
            "store_path": self.store,
            "cards_path": self.store + ".cards",
            "status": status,
            "started_at": "2026-01-01T00:00:00Z",
            "pid": os.getpid(),
            "host_pid": os.getpid(),
            "socket_path": self.sock_path,
            "schema": 1,
            "model": {"provider": "", "id": ""},
        }
        d.update(extra)
        with open(os.path.join(self.sidecar_dir, f"{self.session_id}.json"), "w") as f:
            json.dump(d, f)

    def _cards(self, *cards: dict) -> None:
        tmp = self.store + ".cards.tmp"
        with open(tmp, "w") as f:
            json.dump(list(cards), f)
        os.replace(tmp, self.store + ".cards")

    def _append(self, role: str, content, **msg) -> None:
        m = {"role": role, "content": content}
        m.update(msg)
        with open(self.store, "a") as f:
            f.write(
                json.dumps({"type": "message", "timestamp": "2026-01-01T00:00:01Z", "message": m})
                + "\n"
            )

    def _listen(self) -> "tuple[socket.socket, list[dict]]":
        self.sock_path = os.path.join(self.tmpdir, "s.sock")
        srv = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        srv.bind(self.sock_path)
        srv.listen(16)  # room for liveness probes alongside the real send
        got: list[dict] = []
        self.addCleanup(srv.close)
        return srv, got

    def _recv_all(self, srv: socket.socket, got: list) -> None:
        # Skip empty connections — liveness probes (_socket_alive) connect
        # and hang up without sending anything.
        while not got:
            try:
                conn, _ = srv.accept()
            except ConnectionAbortedError:
                continue  # a probe that hung up before we accepted it
            with conn, conn.makefile("r") as f:
                got.extend(json.loads(line) for line in f)
        # Keep accepting (and dropping) later probes, like a live session,
        # so the listen backlog never fills and refuses connections.
        threading.Thread(target=self._drain, args=(srv,), daemon=True).start()

    @staticmethod
    def _drain(srv: socket.socket) -> None:
        with suppress(OSError):
            while True:
                conn, _ = srv.accept()
                conn.close()

    # -- status ------------------------------------------------------------

    def test_list_status_comes_from_core_card(self) -> None:
        """Regression: the sidecar said idle (out-of-order extension events)
        while the agent was mid-turn. The core card is authoritative."""
        self._sidecar(status="idle")
        self._cards(_status_card("running", tool="bash", model="anthropic/claude"))
        out = observe._snapshot_session_list()
        self.assertIn("running", out)
        self.assertNotIn("idle", out)

    def test_error_and_no_model_sessions_are_live(self) -> None:
        self._sidecar(status="running")
        self._cards(_status_card("no-model", notice="No models available. Use /login"))
        rows = observe._read_sidecars(include_all=False)
        self.assertEqual([r["status"] for r in rows], ["no-model"])

    def test_reused_pid_with_dead_socket_is_crashed(self) -> None:
        """Regression: after a reboot, old sidecars' pids belonged to
        unrelated daemons and months-old sessions were listed as running."""
        self.sock_path = os.path.join(self.tmpdir, "gone.sock")  # never bound
        self._sidecar(status="running", started_at="2026-01-01T00:00:00Z")
        self.assertEqual(observe._read_sidecars(include_all=True)[0]["status"], "crashed")

    def test_single_refusal_is_not_death(self) -> None:
        """A momentarily full listen backlog refuses one connect; only a
        repeated refusal (or a missing socket file) means dead."""
        self.sock_path = os.path.join(self.tmpdir, "busy.sock")
        self._sidecar(status="running", started_at="2026-01-01T00:00:00Z")
        results = iter(["refused", None])
        with mock.patch.object(observe, "_probe_socket", side_effect=lambda p: next(results)):
            self.assertEqual(observe._read_sidecars(include_all=True)[0]["status"], "running")
        with mock.patch.object(observe, "_probe_socket", return_value="refused"):
            self.assertEqual(observe._read_sidecars(include_all=True)[0]["status"], "crashed")

    def test_fresh_session_without_socket_yet_is_live(self) -> None:
        self.sock_path = os.path.join(self.tmpdir, "soon.sock")
        now = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
        self._sidecar(status="running", started_at=now)
        self.assertEqual(observe._read_sidecars(include_all=True)[0]["status"], "running")

    def test_resolve_by_cwd_follows_symlinks(self) -> None:
        self._sidecar(status="idle")
        link = os.path.join(self.tmpdir, "link")
        os.symlink(self.tmpdir, link)
        self.assertEqual(observe._resolve_sidecar("", link)["session_id"], self.session_id)

    def test_live_session_wins_over_dead_namesake(self) -> None:
        """Re-spawning worker 'worker' must not make it ambiguous with the
        crashed 'worker' from the previous run."""
        self._sidecar(status="idle")
        old_sid = "deadbeef" + "2" * 28
        with open(os.path.join(self.sidecar_dir, f"{old_sid}.json"), "w") as f:
            json.dump(
                {
                    "session_id": old_sid,
                    "session_name": "worker",
                    "cwd": self.tmpdir,
                    "status": "ended",
                    "started_at": "2025-01-01T00:00:00Z",
                    "pid": os.getpid(),
                },
                f,
            )
        self.assertEqual(observe._resolve_sidecar("worker", "")["session_id"], self.session_id)
        self.assertEqual(observe._resolve_sidecar("", self.tmpdir)["session_id"], self.session_id)

    def test_pick_match_rules(self) -> None:
        live1 = {"session_id": "a1", "status": "idle"}
        live2 = {"session_id": "a2", "status": "running"}
        dead_new = {"session_id": "d1", "status": "crashed"}
        dead_old = {"session_id": "d2", "status": "ended"}
        with self.assertRaisesRegex(ValueError, "ambiguous"):
            observe._pick_match([live1, dead_new, live2])
        self.assertIs(observe._pick_match([dead_new, dead_old]), dead_new)

    def test_dead_pid_still_crashed_despite_card(self) -> None:
        self._sidecar(status="running", pid=2**22 + 12345)
        self._cards(_status_card("running"))
        rows = observe._read_sidecars(include_all=True)
        self.assertEqual(rows[0]["status"], "crashed")

    def test_status_json_and_snapshot_surface_startup_failure(self) -> None:
        """Regression: the TUI said 'No models available' while observe
        showed only '✎ thinking level changed'."""
        self._sidecar(status="running")
        self._cards(
            _status_card(
                "no-model",
                runs="0",
                error="no model selected. Use /login",
                notice="No models available. Use /login or set an API key environment variable.",
            )
        )
        h = _FakeHost()
        self.assertEqual(observe.cli_observe([self.session_id[:8], "--status", "--json"], h), 0)
        st = json.loads(h.stdout)
        self.assertEqual(st["status"], "no-model")
        self.assertIn("No models available", st["notice"])
        self.assertEqual(st["runs"], 0)
        self.assertTrue(st["live"])

        h = _FakeHost()
        self.assertEqual(observe.cli_observe([self.session_id[:8]], h), 0)
        first = h.stdout.splitlines()[0]
        self.assertIn("status: no-model", first)
        self.assertIn("notice: No models available", h.stdout)
        # The status card is not duplicated into the cards header.
        self.assertNotIn("session: no-model", h.stdout)

    def test_parse_card_ts_handles_go_nanos(self) -> None:
        t = observe._parse_card_ts("2026-01-02T03:04:05.123456789Z")
        self.assertAlmostEqual(
            t, datetime(2026, 1, 2, 3, 4, 5, 123456, tzinfo=timezone.utc).timestamp()
        )
        self.assertEqual(observe._parse_card_ts("garbage"), 0.0)
        self.assertEqual(
            observe._parse_card_ts("2026-01-02T03:04:05.5-02:00"),
            datetime(2026, 1, 2, 5, 4, 5, 500000, tzinfo=timezone.utc).timestamp(),
        )
        self.assertGreater(observe._parse_card_ts("2026-01-02T03:04:05Z"), 0)
        # Go trims trailing zeros; Python 3.9 would reject a 1-digit fraction.
        self.assertEqual(
            observe._parse_card_ts("2026-01-02T03:04:05.5Z"),
            datetime(2026, 1, 2, 3, 4, 5, 500000, tzinfo=timezone.utc).timestamp(),
        )

    # -- formatter ----------------------------------------------------------

    def test_formatter_shows_turn_error_and_tools(self) -> None:
        fmt = observe._Formatter(raw_json=False, color=False)
        err = json.dumps(
            {
                "type": "message",
                "message": {
                    "role": "assistant",
                    "content": [],
                    "stopReason": "error",
                    "errorMessage": "Refresh token expired. Run '/login anthropic'",
                },
            }
        )
        self.assertIn("✗ error: Refresh token expired", fmt.render(err) or "")
        call = json.dumps(
            {
                "type": "message",
                "message": {
                    "role": "assistant",
                    "content": [
                        {"type": "toolCall", "name": "bash", "arguments": {"command": "make test"}}
                    ],
                },
            }
        )
        self.assertIn("→ bash  make test", fmt.render(call) or "")
        res = json.dumps(
            {
                "type": "message",
                "message": {
                    "role": "toolResult",
                    "toolName": "bash",
                    "isError": True,
                    "content": [{"type": "text", "text": "x" * 1000}],
                },
            }
        )
        out = fmt.render(res) or ""
        self.assertIn("✗ bash", out)
        self.assertLess(len(out), 300)

    # -- snapshot mode -------------------------------------------------------

    def test_observe_without_tty_is_a_snapshot_that_exits(self) -> None:
        self._sidecar(status="running")
        self._cards(_status_card("running"))
        for i in range(5):
            self._append("user", f"msg{i}")
        h = _FakeHost(tty=False)
        rc = observe.cli_observe([self.session_id[:8], "-n", "2"], h)
        self.assertEqual(rc, 0)
        self.assertIn("msg4", h.stdout)
        self.assertNotIn("msg2", h.stdout)

    def test_observe_follow_stops_on_signal(self) -> None:
        self._sidecar(status="running")
        observe._verb_stop.set()  # as if Ctrl-C / SIGTERM arrived
        h = _FakeHost(tty=True)
        self.assertEqual(observe.cli_observe([self.session_id[:8]], h), 0)
        self.assertIn("status:", h.stdout)

    def test_observe_wait_times_out_with_124(self) -> None:
        self._sidecar(status="running")
        self._cards(_status_card("running"))
        h = _FakeHost()
        rc = observe.cli_observe([self.session_id[:8], "--wait", "--status", "--timeout", "0.3"], h)
        self.assertEqual(rc, 124)
        self.assertIn("status: running", h.stdout)

    def test_observe_list_json(self) -> None:
        self._sidecar(status="idle")
        h = _FakeHost()
        self.assertEqual(observe.cli_observe(["--json"], h), 0)
        rows = json.loads(h.stdout)
        self.assertEqual(rows[0]["session_id"], self.session_id)

    # -- send ------------------------------------------------------------------

    def test_send_positional_message(self) -> None:
        srv, got = self._listen()
        self._sidecar(status="idle")
        t = threading.Thread(target=self._recv_all, args=(srv, got))
        t.start()
        h = _FakeHost()
        rc = observe.cli_send([self.session_id[:8], "!stop", "and", "rethink"], h)
        t.join(20)
        self.assertEqual(rc, 0, h.stderr)
        self.assertEqual(got, [{"deliver_as": "steer", "content": "stop and rethink"}])

    def test_send_piped_stdin_is_one_message(self) -> None:
        srv, got = self._listen()
        self._sidecar(status="idle")
        t = threading.Thread(target=self._recv_all, args=(srv, got))
        t.start()
        h = _FakeHost(stdin=["line one\n", "line two\n"], tty=False)
        rc = observe.cli_send([self.session_id[:8]], h)
        t.join(20)
        self.assertEqual(rc, 0, h.stderr)
        self.assertEqual(got, [{"deliver_as": "", "content": "line one\nline two"}])

    def test_send_wait_needs_a_message(self) -> None:
        self._listen()
        self._sidecar(status="idle")
        h = _FakeHost(tty=True)
        self.assertEqual(observe.cli_send([self.session_id[:8], "--wait"], h), 1)
        self.assertIn("--wait needs a message", h.stderr)

    def test_send_interrupted_stdin_read_sends_nothing(self) -> None:
        self._listen()
        self._sidecar(status="idle")
        observe._verb_stop.set()  # signal arrived while reading the brief
        h = _FakeHost(stdin=["half a brie"], tty=False)
        self.assertEqual(observe.cli_send([self.session_id[:8]], h), 130)

    def test_send_to_ended_session_fails_fast(self) -> None:
        self._sidecar(status="ended")
        h = _FakeHost()
        self.assertEqual(observe.cli_send([self.session_id[:8], "hi"], h), 1)
        self.assertIn("ended", h.stderr)

    def _fake_agent(self, srv: socket.socket, got: list, reply: str, error: str = "") -> None:
        """Accept the message, then play a run: running → user msg →
        assistant reply → idle/error, the order fir core writes them."""
        self._recv_all(srv, got)
        self._cards(_status_card("running", ts=_now_card_ts()))
        self._append("user", got[0]["content"])
        self._append(
            "assistant",
            [{"type": "toolCall", "name": "bash", "arguments": {}}],
            stopReason="toolUse",
        )
        if error:
            self._append("assistant", [], stopReason="error", errorMessage=error)
            self._cards(_status_card("error", ts=_now_card_ts(), error=error))
        else:
            self._append("assistant", [{"type": "text", "text": reply}], stopReason="stop")
            self._cards(_status_card("idle", ts=_now_card_ts()))

    def test_send_wait_prints_final_reply(self) -> None:
        srv, got = self._listen()
        self._sidecar(status="idle")
        self._cards(_status_card("idle"))  # stale idle from before the send
        t = threading.Thread(target=self._fake_agent, args=(srv, got, "all 42 tests pass"))
        t.start()
        h = _FakeHost()
        rc = observe.cli_send([self.session_id[:8], "--wait", "--timeout", "20", "run tests"], h)
        t.join(20)
        self.assertEqual(rc, 0, h.stderr)
        self.assertEqual(h.stdout.strip(), "all 42 tests pass")

    def test_send_wait_ignores_someone_elses_turn(self) -> None:
        """A human typing in the TUI (or another sender) must not end our
        wait with their reply."""
        srv, got = self._listen()
        self._sidecar(status="idle")
        self._cards(_status_card("idle"))

        def play() -> None:
            self._recv_all(srv, got)
            self._append("user", "unrelated question from the TUI")
            self._append("assistant", [{"type": "text", "text": "NOT OURS"}], stopReason="stop")
            self._cards(_status_card("idle", ts=_now_card_ts()))
            self._fake_agent_run(got[0]["content"], "OURS")

        t = threading.Thread(target=play)
        t.start()
        h = _FakeHost()
        rc = observe.cli_send([self.session_id[:8], "--wait", "--timeout", "20", "our task"], h)
        t.join(20)
        self.assertEqual(rc, 0, h.stderr)
        self.assertEqual(h.stdout.strip(), "OURS")

    def _fake_agent_run(self, user: str, reply: str) -> None:
        self._cards(_status_card("running", ts=_now_card_ts()))
        self._append("user", user)
        self._append("assistant", [{"type": "text", "text": reply}], stopReason="stop")
        self._cards(_status_card("idle", ts=_now_card_ts()))

    def test_send_wait_reports_failed_turn(self) -> None:
        srv, got = self._listen()
        self._sidecar(status="idle")
        self._cards(_status_card("idle"))
        t = threading.Thread(target=self._fake_agent, args=(srv, got, "", "Refresh token expired"))
        t.start()
        h = _FakeHost()
        rc = observe.cli_send([self.session_id[:8], "--wait", "--timeout", "20", "hi"], h)
        t.join(20)
        self.assertEqual(rc, 1)
        self.assertIn("Refresh token expired", h.stderr)

    def test_send_wait_detects_refused_prompt(self) -> None:
        """No model: the prompt never reaches the agent loop, nothing is
        persisted — only the status card changes. --wait must not hang."""
        srv, got = self._listen()
        self._sidecar(status="running")
        self._cards(_status_card("no-model", ts="2026-01-01T00:00:00Z"))

        def refuse() -> None:
            self._recv_all(srv, got)
            self._cards(_status_card("no-model", ts=_now_card_ts(), error="no model selected"))

        t = threading.Thread(target=refuse)
        t.start()
        h = _FakeHost()
        rc = observe.cli_send([self.session_id[:8], "--wait", "--timeout", "20", "hi"], h)
        t.join(20)
        self.assertEqual(rc, 1)
        self.assertIn("no model selected", h.stderr)


if __name__ == "__main__":
    unittest.main()
