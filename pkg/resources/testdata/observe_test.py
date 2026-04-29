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


if __name__ == "__main__":
    unittest.main()
