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
    os.path.dirname(__file__), "..", "..", "extension", "sdk", "python",
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
    observe._state.update({
        "session_id": "",
        "pid": os.getpid(),
        "socket_path": "",
        "store_path": "",
        "cwd": "",
        "started_at": "",
        "status": "running",
        "session_name": "",
        "schema": 1,
    })
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
            self.state_dir, "fir", "agents", f"{self.session_id}.json",
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
        self.assertTrue(d["socket_path"].endswith(".sock"))
        self.assertIn(self.session_id[:16], d["socket_path"])
        self.assertTrue(d["started_at"])  # non-empty timestamp

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
                    observe._state["session_id"], "",
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
            self.sock_dir, "fir", "observe", f"{self.session_id[:16]}.sock",
        )

    def _connect(self, timeout: float = 2.0) -> socket.socket:
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
        deadline = time.monotonic() + 2.0
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
            deadline = time.monotonic() + 2.0
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
            c.sendall((
                json.dumps({"deliver_as": "steer", "content": "stop, look at foo.go"})
                + "\n"
            ).encode())
            deadline = time.monotonic() + 2.0
            while time.monotonic() < deadline:
                if ctx.send_user_message.called:
                    break
                time.sleep(0.01)
        finally:
            c.close()
        ctx.send_user_message.assert_called_with(
            "stop, look at foo.go", deliver_as="steer",
        )

    def test_followup_sigil_passes_through(self) -> None:
        ctx = _make_ctx()
        observe.on_session_start({"session_id": self.session_id}, ctx)
        c = self._connect()
        try:
            c.sendall((
                json.dumps({"deliver_as": "followUp", "content": "and update CHANGELOG"})
                + "\n"
            ).encode())
            deadline = time.monotonic() + 2.0
            while time.monotonic() < deadline:
                if ctx.send_user_message.called:
                    break
                time.sleep(0.01)
        finally:
            c.close()
        ctx.send_user_message.assert_called_with(
            "and update CHANGELOG", deliver_as="followUp",
        )

    def test_unknown_deliver_as_normalized_to_empty(self) -> None:
        ctx = _make_ctx()
        observe.on_session_start({"session_id": self.session_id}, ctx)
        c = self._connect()
        try:
            c.sendall((
                json.dumps({"deliver_as": "garbage", "content": "x"}) + "\n"
            ).encode())
            deadline = time.monotonic() + 2.0
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
            c.sendall((
                json.dumps({"content": "real message"}) + "\n"
            ).encode())
            deadline = time.monotonic() + 2.0
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
        deadline = time.monotonic() + 2.0
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
        deadline = time.monotonic() + 2.0
        while time.monotonic() < deadline:
            if os.path.exists(self._socket_path()):
                break
            time.sleep(0.01)

        def _client(text: str) -> None:
            c = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            c.connect(self._socket_path())
            c.sendall((json.dumps({"content": text}) + "\n").encode())
            c.close()

        threads = [
            threading.Thread(target=_client, args=(f"msg-{i}",)) for i in range(5)
        ]
        for t in threads:
            t.start()
        for t in threads:
            t.join()

        # Wait for handlers to drain.
        deadline = time.monotonic() + 2.0
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
            "input": 0, "output": 0, "cache_read": 0, "cache_write": 0,
            "total_tokens": 0, "requests": 0,
            "cost": {"input": 0.0, "output": 0.0, "cache_read": 0.0,
                     "cache_write": 0.0, "total": 0.0},
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
        return json.loads(open(path).read())

    def test_assistant_message_end_records_usage_and_model(self) -> None:
        self._start()
        observe.on_message_end({
            "role": "assistant",
            "provider": "anthropic",
            "model": "claude-3-5-sonnet",
            "usage": {
                "input": 100, "output": 50,
                "cache_read": 20, "cache_write": 10,
                "total_tokens": 180,
                "cost": {"input": 0.001, "output": 0.002,
                         "cache_read": 0.0, "cache_write": 0.0,
                         "total": 0.003},
            },
        }, MagicMock())
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
            observe.on_message_end({
                "role": "assistant",
                "provider": "openai", "model": "gpt-4",
                "usage": {"input": 10, "output": 5, "cache_read": 0,
                          "cache_write": 0, "total_tokens": 15,
                          "cost": {"input": 0, "output": 0, "cache_read": 0,
                                   "cache_write": 0, "total": 0.01}},
            }, MagicMock())
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
                observe.on_message_end({
                    "role": "assistant",
                    "provider": "p", "model": "m",
                    "usage": {"input": 1, "output": 1, "cache_read": 0,
                              "cache_write": 0, "total_tokens": 2,
                              "cost": {"input": 0, "output": 0,
                                       "cache_read": 0, "cache_write": 0,
                                       "total": 0.0001}},
                }, MagicMock())
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
            full_text=kw.get("full_text", False),
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
        line = ('{"type":"message","timestamp":"2026-04-27T12:00:00Z",'
                '"message":{"role":"user","content":"hi there"}}')
        out = self._fmt().render(line)
        self.assertIn("user", out)
        self.assertIn("hi there", out)

    def test_assistant_with_tool_use(self):
        line = ('{"type":"message","timestamp":"2026-04-27T12:00:00Z","message":'
                '{"role":"assistant","content":'
                '[{"type":"text","text":"thinking..."},{"type":"tool_use","name":"bash"}]}}')
        out = self._fmt().render(line)
        self.assertIn("assistant", out)
        self.assertIn("thinking", out)
        self.assertIn("→ bash", out)

    def test_model_change(self):
        line = ('{"type":"model_change","timestamp":"2026-04-27T12:00:00Z",'
                '"provider":"anthropic","modelId":"claude-opus-4"}')
        out = self._fmt().render(line)
        self.assertIn("model →", out)
        self.assertIn("anthropic", out)

    def test_compaction(self):
        line = ('{"type":"compaction","timestamp":"2026-04-27T12:00:00Z",'
                '"summary":"compressed 50 turns"}')
        out = self._fmt().render(line)
        self.assertIn("compaction", out)
        self.assertIn("50 turns", out)

    def test_plan_update(self):
        line = ('{"type":"plan_update","timestamp":"2026-04-27T12:00:00Z",'
                '"planTitle":"Implement caching"}')
        self.assertIn("Implement caching", self._fmt().render(line))

    def test_command(self):
        line = ('{"type":"command","timestamp":"2026-04-27T12:00:00Z",'
                '"command":"bash","args":"go test ./..."}')
        out = self._fmt().render(line)
        self.assertIn("bash", out)
        self.assertIn("go test", out)

    def test_hidden_types_return_none(self):
        for ty in ("label", "branch_summary", "custom", "custom_message"):
            line = f'{{"type":"{ty}","timestamp":"2026-04-27T12:00:00Z"}}'
            self.assertIsNone(self._fmt().render(line),
                              f"type {ty} should be suppressed")

    def test_unknown_type_renders_name(self):
        line = '{"type":"future_type","timestamp":"2026-04-27T12:00:00Z"}'
        self.assertIn("future_type", self._fmt().render(line))


class TestSummariseContent(unittest.TestCase):
    def test_strings_collapse_newlines(self):
        self.assertEqual(
            observe._summarise_content("hello\nworld\nlong text", False),
            "hello world long text",
        )

    def test_truncates(self):
        out = observe._summarise_content("x" * 500, False)
        self.assertLessEqual(len(out), 200)
        self.assertTrue(out.endswith("…"))

    def test_full_no_truncation(self):
        long = "x" * 500
        self.assertEqual(observe._summarise_content(long, True), long)


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
        self.assertEqual(
            observe._parse_observe_args([]),
            ("", "", False, False, False, None),
        )

    def test_observe_id_prefix(self):
        self.assertEqual(
            observe._parse_observe_args(["abc"]),
            ("abc", "", False, False, False, None),
        )

    def test_observe_flags(self):
        r = observe._parse_observe_args(["abc", "--json", "--full", "--interact"])
        self.assertEqual(r, ("abc", "", True, True, True, None))

    def test_observe_cwd(self):
        self.assertEqual(
            observe._parse_observe_args(["--cwd", "/path"])[1],
            "/path",
        )
        self.assertEqual(
            observe._parse_observe_args(["--cwd=/path"])[1],
            "/path",
        )

    def test_observe_unknown_flag(self):
        _, _, _, _, _, err = observe._parse_observe_args(["--bogus"])
        assert err is not None

        self.assertIn("unknown flag", err)

    def test_observe_extra_arg(self):
        _, _, _, _, _, err = observe._parse_observe_args(["a", "b"])
        assert err is not None

        self.assertIn("extra argument", err)

    def test_observe_help(self):
        _, _, _, _, _, err = observe._parse_observe_args(["--help"])
        self.assertEqual(err, "__HELP__")

    def test_send_no_args_required(self):
        _, _, _, err = observe._parse_send_args([])
        assert err is not None

        self.assertIn("required", err)

    def test_send_unknown_flag(self):
        _, _, _, err = observe._parse_send_args(["--bogus"])
        assert err is not None

        self.assertIn("unknown flag", err)

    def test_send_conflicting_flags(self):
        _, _, _, err = observe._parse_send_args(["--steer", "--follow", "abc"])
        assert err is not None

        self.assertIn("mutually exclusive", err)

    def test_send_steer_default(self):
        _, _, da, err = observe._parse_send_args(["--steer", "abc"])
        self.assertIsNone(err)
        self.assertEqual(da, "steer")

    def test_send_follow_default(self):
        _, _, da, err = observe._parse_send_args(["--follow", "abc"])
        self.assertIsNone(err)
        self.assertEqual(da, "followUp")


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
        sidecars = [{
            "session_id": "deadbeefcafe1234",
            "session_name": "demo",
            "cwd": "/tmp/work",
            "status": "running",
            "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "store_path": "",
            "model": {"provider": "anthropic", "id": "claude"},
            "usage": {"total_tokens": 12345, "cost": {"total": 1.23}},
            "activity": {"tool_calls": 7, "tool_errors": 2,
                         "last_event": time.strftime("%Y-%m-%dT%H:%M:%SZ",
                                                     time.gmtime())},
        }]
        out = observe._htop_render(sidecars, color=False)
        self.assertIn("1 session ", out)  # singular, no trailing 's'
        self.assertIn("deadbeef", out)    # truncated id
        self.assertIn("anthropic/claude", out)
        self.assertIn("12.3k", out)
        self.assertIn("$1.23", out)
        self.assertIn("7/2", out)


if __name__ == "__main__":
    unittest.main()



# ---------------------------------------------------------------------------
# Slash commands and AI tools (snapshot, not live tail)
# ---------------------------------------------------------------------------


class TestSlashCommandsAndTools(unittest.TestCase):
    def setUp(self) -> None:
        self.state_dir = tempfile.mkdtemp(prefix="observe-test-state-")
        self.sock_dir = f"/tmp/o{os.getpid()}-{int(time.time() * 1000) % 10000}"
        os.makedirs(self.sock_dir, exist_ok=True)
        _reset_observe_state(self.state_dir, self.sock_dir)
        self.session_id = "abcd1234ef567890" + "0" * 20
        # A pretend transcript file with a few JSONL entries.
        self.transcript = os.path.join(self.state_dir, "transcript.jsonl")
        with open(self.transcript, "w") as f:
            f.write('{"type":"session","version":3,"id":"' + self.session_id + '","cwd":"/tmp"}\n')
            f.write('{"type":"message","timestamp":"2026-04-27T12:00:00Z",'
                    '"message":{"role":"user","content":"hello"}}\n')
            f.write('{"type":"message","timestamp":"2026-04-27T12:00:05Z",'
                    '"message":{"role":"assistant","content":"hi back"}}\n')
        # Set up server-side socket via session_start so /send + tool_send work.
        ctx = _make_ctx(session_file=self.transcript)
        observe.on_session_start({"session_id": self.session_id}, ctx)
        # Wait for socket bind.
        sock_path = observe._socket_path(self.session_id)
        deadline = time.monotonic() + 2.0
        while time.monotonic() < deadline:
            if os.path.exists(sock_path):
                break
            time.sleep(0.01)
        self.server_ctx = ctx

    def tearDown(self) -> None:
        observe.on_session_shutdown({}, _make_ctx())

    # -- /observe -----------------------------------------------------------

    def test_observe_command_no_args_lists_sessions(self) -> None:
        result = observe.cmd_observe([], MagicMock())
        self.assertIn("ID", result["message"])
        self.assertIn(self.session_id[:8], result["message"])
        self.assertTrue(result.get("print_response"))

    def test_observe_command_id_prefix_returns_snapshot(self) -> None:
        result = observe.cmd_observe([self.session_id[:8]], MagicMock())
        self.assertIn("hello", result["message"])
        self.assertIn("hi back", result["message"])

    def test_observe_command_json_passthrough(self) -> None:
        result = observe.cmd_observe([self.session_id[:8], "--json"], MagicMock())
        # Raw JSONL should preserve the JSON structure verbatim.
        self.assertIn('"role":"user"', result["message"])

    def test_observe_command_unknown_flag(self) -> None:
        result = observe.cmd_observe(["--bogus"], MagicMock())
        self.assertIn("unknown flag", result["message"])

    def test_observe_command_no_match(self) -> None:
        result = observe.cmd_observe(["zzz-no-match"], MagicMock())
        self.assertIn("no session matching", result["message"])

    # -- observe_session tool ----------------------------------------------

    def test_tool_observe_no_args_lists(self) -> None:
        out = observe.tool_observe({}, MagicMock())
        self.assertIn("ID", out)
        self.assertIn(self.session_id[:8], out)

    def test_tool_observe_snapshot(self) -> None:
        out = observe.tool_observe(
            {"id_prefix": self.session_id[:8], "lines": 50}, MagicMock(),
        )
        self.assertIn("hello", out)
        self.assertIn("hi back", out)

    def test_tool_observe_always_returns_full_text(self) -> None:
        # The tool schema no longer exposes full_text; output must never
        # be truncated regardless of message length.
        long_msg = "x" * 500
        with open(self.transcript, "a") as f:
            f.write('{"type":"message","timestamp":"2026-04-27T12:00:10Z",'
                    '"message":{"role":"user","content":"' + long_msg + '"}}\n')
        out = observe.tool_observe(
            {"id_prefix": self.session_id[:8], "lines": 50}, MagicMock(),
        )
        self.assertIn(long_msg, out)

    def test_tool_observe_raises_tool_error_on_miss(self) -> None:
        with self.assertRaises(fir_ext.ToolError):
            observe.tool_observe({"id_prefix": "zzz-no-match"}, MagicMock())

    # -- /send --------------------------------------------------------------

    def test_send_command_round_trip(self) -> None:
        # /send delivers via the per-session socket → server forwards via
        # ctx.send_user_message. We registered the server in setUp.
        result = observe.cmd_send(
            [self.session_id[:8], "fix", "the", "bug"],
            MagicMock(),
        )
        self.assertIn("sent", result["message"])
        # Wait for the accept-loop thread to forward.
        deadline = time.monotonic() + 2.0
        while time.monotonic() < deadline:
            if self.server_ctx.send_user_message.called:
                break
            time.sleep(0.01)
        self.server_ctx.send_user_message.assert_called_with(
            "fix the bug", deliver_as="",
        )

    def test_send_command_steer_flag(self) -> None:
        observe.cmd_send(
            [self.session_id[:8], "--steer", "stop", "and", "look"],
            MagicMock(),
        )
        deadline = time.monotonic() + 2.0
        while time.monotonic() < deadline:
            if self.server_ctx.send_user_message.called:
                break
            time.sleep(0.01)
        self.server_ctx.send_user_message.assert_called_with(
            "stop and look", deliver_as="steer",
        )

    def test_send_command_sigil_overrides_default(self) -> None:
        # --steer says steer, but the leading + sigil overrides to followUp.
        observe.cmd_send(
            [self.session_id[:8], "--steer", "+queue", "this"],
            MagicMock(),
        )
        deadline = time.monotonic() + 2.0
        while time.monotonic() < deadline:
            if self.server_ctx.send_user_message.called:
                break
            time.sleep(0.01)
        self.server_ctx.send_user_message.assert_called_with(
            "queue this", deliver_as="followUp",
        )

    def test_send_command_missing_message(self) -> None:
        result = observe.cmd_send([self.session_id[:8]], MagicMock())
        self.assertIn("message text required", result["message"])

    def test_send_command_no_id(self) -> None:
        result = observe.cmd_send(["--steer", "msg"], MagicMock())
        self.assertIn("required", result["message"])

    # -- send_session tool --------------------------------------------------

    def test_tool_send_round_trip(self) -> None:
        out = observe.tool_send(
            {"id_prefix": self.session_id[:8], "content": "review this"},
            MagicMock(),
        )
        self.assertTrue(out.get("ok"))
        deadline = time.monotonic() + 2.0
        while time.monotonic() < deadline:
            if self.server_ctx.send_user_message.called:
                break
            time.sleep(0.01)
        self.server_ctx.send_user_message.assert_called_with(
            "review this", deliver_as="",
        )

    def test_tool_send_deliver_as(self) -> None:
        observe.tool_send(
            {
                "id_prefix": self.session_id[:8],
                "content": "queue this",
                "deliver_as": "followUp",
            },
            MagicMock(),
        )
        deadline = time.monotonic() + 2.0
        while time.monotonic() < deadline:
            if self.server_ctx.send_user_message.called:
                break
            time.sleep(0.01)
        self.server_ctx.send_user_message.assert_called_with(
            "queue this", deliver_as="followUp",
        )

    def test_tool_send_requires_id_or_cwd(self) -> None:
        with self.assertRaises(fir_ext.ToolError):
            observe.tool_send({"content": "no target"}, MagicMock())

    def test_tool_send_invalid_deliver_as(self) -> None:
        with self.assertRaises(fir_ext.ToolError):
            observe.tool_send(
                {"id_prefix": self.session_id[:8], "content": "x", "deliver_as": "bogus"},
                MagicMock(),
            )



class TestTailLines(unittest.TestCase):
    def setUp(self):
        # Use mkstemp to avoid SIM115 (NamedTemporaryFile is a context manager).
        fd, self.path = tempfile.mkstemp(suffix=".jsonl")
        os.close(fd)

    def tearDown(self):
        with suppress(FileNotFoundError):
            os.unlink(self.path)

    def _write(self, text: str) -> None:
        with open(self.path, "w", encoding="utf-8") as f:
            f.write(text)

    def test_empty_file(self):
        self._write("")
        self.assertEqual(observe._tail_lines(self.path, 5), [])

    def test_zero_n(self):
        self._write("a\nb\nc\n")
        self.assertEqual(observe._tail_lines(self.path, 0), [])

    def test_fewer_lines_than_n(self):
        self._write("a\nb\n")
        self.assertEqual(observe._tail_lines(self.path, 5), ["a", "b"])

    def test_more_lines_than_n(self):
        self._write("\n".join(str(i) for i in range(100)) + "\n")
        out = observe._tail_lines(self.path, 5)
        self.assertEqual(out, ["95", "96", "97", "98", "99"])

    def test_no_trailing_newline(self):
        self._write("first\nsecond\nthird")
        out = observe._tail_lines(self.path, 2)
        self.assertEqual(out, ["second", "third"])

    def test_exact_chunk_boundary(self):
        # Force backward-read to span multiple chunks.
        line = "x" * 100
        self._write((line + "\n") * 200)
        out = observe._tail_lines(self.path, 3, chunk_size=64)
        self.assertEqual(out, [line, line, line])

    def test_missing_file_returns_empty(self):
        os.unlink(self.path)
        self.assertEqual(observe._tail_lines(self.path, 5), [])
