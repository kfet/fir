#!/usr/bin/env python3
"""Protocol-level tests for demo.py — exercises every API surface via a
FakeFir that drives the real fir_ext JSON-RPC loop.

These tests are as close to end-to-end as possible in pure Python:
  • fir_ext.run() is invoked for real (in a background thread)
  • All messages travel as JSON-RPC over in-memory streams
  • FakeFir auto-responds to outbound extension requests
  • Assertions verify the outbound calls the extension makes
"""

import importlib.util
import json
import os
import queue
import sys
import threading
import time
import unittest

# Ensure the SDK directory is on sys.path (needed when run directly).
_HERE = os.path.dirname(os.path.abspath(__file__))
if _HERE not in sys.path:
    sys.path.insert(0, _HERE)

import fir_ext  # noqa: E402

_PROJECT_ROOT = os.path.normpath(os.path.join(_HERE, "..", "..", "..", ".."))
_DEMO_PATH = os.path.join(_PROJECT_ROOT, ".fir", "extensions", "demo.py")


# ---------------------------------------------------------------------------
# Helper: import demo.py and register its handlers into fir_ext globals
# ---------------------------------------------------------------------------


_DEMO_EXT_NAME: str | None = None


def _load_demo() -> None:
    """Execute demo.py so its decorators populate fir_ext's global registries.

    fir_ext.run() is stubbed to a no-op so the blocking event loop does not
    start; the caller drives the loop via fir_ext.run(input_stream=…) instead.
    Records the ``name`` argument passed by demo.py so tests can inject it.
    """
    global _DEMO_EXT_NAME

    fir_ext._tools.clear()
    fir_ext._tool_handlers.clear()
    fir_ext._hook_handlers.clear()
    fir_ext._event_handlers.clear()
    _DEMO_EXT_NAME = None

    orig_run = fir_ext.run

    def _capture_run(*a: object, **kw: object) -> None:
        global _DEMO_EXT_NAME
        name_val = kw.get("name") or (a[0] if a else None)
        _DEMO_EXT_NAME = name_val if isinstance(name_val, str) else None

    fir_ext.run = _capture_run  # type: ignore[assignment]
    try:
        spec = importlib.util.spec_from_file_location("_demo_ext", _DEMO_PATH)
        assert spec and spec.loader
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)
    finally:
        fir_ext.run = orig_run


# ---------------------------------------------------------------------------
# FakeFir — bidirectional fake fir server
# ---------------------------------------------------------------------------


class FakeFir:
    """Thread-safe in-memory fake fir server.

    Usage::

        fake = FakeFir()
        self.start_demo_ext(fake)        # runs fir_ext.run() in a thread (DemoTestCase helper)
        fake.send_init()
        fake.send_event("agent_start")
        msg = fake.wait_for_method("set_session_name", timeout=3)
        assert msg["params"]["name"] == "demo session"
        fake.stop()
    """

    def __init__(self, *, active_tools: list[str] | None = None, model_ok: bool = True):
        # Queue for fir → extension messages
        self._to_ext: queue.SimpleQueue[str] = queue.SimpleQueue()
        # All messages received from extension
        self._from_ext: list[dict] = []
        self._from_ext_lock = threading.Lock()
        self._from_ext_event = threading.Event()
        # Extension thread handle
        self._thread: threading.Thread | None = None

        # Default auto-response data
        self._active_tools: list[str] = active_tools or ["bash", "read", "write"]
        self._model_ok: bool = model_ok

        self.input = self._InputStream(self)
        self.output = self._OutputStream(self)

    # -- stream interfaces used by fir_ext.run() ---------------------------

    class _InputStream:
        def __init__(self, fake: "FakeFir") -> None:
            self._f = fake

        def readline(self) -> str:
            try:
                return self._f._to_ext.get(timeout=10)
            except Exception:
                return ""

    class _OutputStream:
        def __init__(self, fake: "FakeFir") -> None:
            self._f = fake

        def write(self, s: str) -> int:
            s = s.strip()
            if not s:
                return 0
            msg: dict = json.loads(s)
            with self._f._from_ext_lock:
                self._f._from_ext.append(msg)
            self._f._from_ext_event.set()
            self._f._handle_outbound(msg)
            return len(s)

        def flush(self) -> None:
            pass

    # -- auto-responder for outbound extension requests --------------------

    def _handle_outbound(self, msg: dict) -> None:
        """Respond automatically to requests the extension sends to fir."""
        rid = msg.get("id")
        method = msg.get("method", "")
        # No id → notification or response; nothing to respond to.
        if rid is None or not method:
            return
        if method == "get_active_tools":
            result: object = self._active_tools
        elif method == "set_model":
            result = {"ok": self._model_ok}
        elif method == "exec":
            result = {"stdout": "mock output", "stderr": "", "exit_code": 0}
        else:
            result = {"ok": True}
        resp = json.dumps({"jsonrpc": "2.0", "id": rid, "result": result}) + "\n"
        self._to_ext.put(resp)

    # -- test helpers -------------------------------------------------------

    def start_extension(self, name: str | None = None) -> "FakeFir":
        """Spawn fir_ext.run() in a background daemon thread."""
        self._thread = threading.Thread(
            target=fir_ext.run,
            kwargs={"input_stream": self.input, "output_stream": self.output, "name": name},
            daemon=True,
        )
        self._thread.start()
        return self

    def stop(self, timeout: float = 5.0) -> None:
        """Send EOF to the extension and wait for it to finish."""
        self._to_ext.put("")  # EOF sentinel
        if self._thread:
            self._thread.join(timeout=timeout)

    def send(self, msg: dict) -> None:
        self._to_ext.put(json.dumps(msg) + "\n")

    def send_init(self, cwd: str = "/tmp") -> dict:
        """Send init and wait for the response. Returns the parsed result."""
        self.send({
            "jsonrpc": "2.0",
            "id": 1,
            "method": "init",
            "params": {"version": "1", "cwd": cwd},
        })
        resp = self.wait_for_response(1)
        assert resp is not None, "no init response"
        return resp.get("result", {})

    def send_event(self, event_name: str, params: dict | None = None) -> None:
        self.send({"jsonrpc": "2.0", "method": f"event/{event_name}", "params": params or {}})

    def send_tool_call(
        self, msg_id: int, tool_name: str, params: dict | None = None
    ) -> dict | None:
        self.send({
            "jsonrpc": "2.0",
            "id": msg_id,
            "method": "tool_call",
            "params": {"tool_call_id": f"tc-{msg_id}", "name": tool_name, "params": params or {}},
        })
        return self.wait_for_response(msg_id)

    def send_hook(self, msg_id: int, hook_name: str, params: dict | None = None) -> dict | None:
        self.send({
            "jsonrpc": "2.0",
            "id": msg_id,
            "method": hook_name,
            "params": params or {},
        })
        return self.wait_for_response(msg_id)

    def wait_for_method(self, method: str, timeout: float = 3.0) -> dict | None:
        """Block until a message with the given outbound method is seen."""
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            with self._from_ext_lock:
                for m in self._from_ext:
                    if m.get("method") == method:
                        return m
            remaining = deadline - time.monotonic()
            self._from_ext_event.wait(timeout=min(0.05, remaining))
            self._from_ext_event.clear()
        return None

    def wait_for_response(self, msg_id: int, timeout: float = 3.0) -> dict | None:
        """Block until a response with the given id is seen."""
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            with self._from_ext_lock:
                for m in self._from_ext:
                    if m.get("id") == msg_id and "method" not in m:
                        return m
            remaining = deadline - time.monotonic()
            self._from_ext_event.wait(timeout=min(0.05, remaining))
            self._from_ext_event.clear()
        return None

    def received_with_method(self, method: str) -> list[dict]:
        with self._from_ext_lock:
            return [m for m in self._from_ext if m.get("method") == method]

    def all_sent_methods(self) -> list[str]:
        with self._from_ext_lock:
            return [m.get("method", "") for m in self._from_ext if m.get("method")]


# ---------------------------------------------------------------------------
# Base class: loads demo.py before each test
# ---------------------------------------------------------------------------


class DemoTestCase(unittest.TestCase):
    def setUp(self) -> None:
        _load_demo()

    def start_demo_ext(self, fake: "FakeFir") -> "FakeFir":
        """Start extension using the name captured from demo.py's fir_ext.run() call."""
        return fake.start_extension(name=_DEMO_EXT_NAME)


# ---------------------------------------------------------------------------
# Init / capabilities
# ---------------------------------------------------------------------------


class TestDemoInit(DemoTestCase):
    def test_name_and_tools(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        result = fake.send_init()
        fake.stop()

        self.assertEqual(result["name"], "demo")
        tool_names = {t["name"] for t in result.get("tools", [])}
        self.assertSetEqual(
            tool_names,
            {
                "word_count", "shell_run", "list_tools",
                "pin_tools", "change_model", "inject_message",
            },
        )

    def test_events_include_all_handlers(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        result = fake.send_init()
        fake.stop()

        events = set(result.get("events", []))
        self.assertSetEqual(
            events,
            {
                "session_start", "session_shutdown",
                "agent_start", "agent_end",
                "turn_start", "turn_end",
                "message_start", "message_end",
                "tool_execution_start", "tool_execution_end",
                "hook/tool_call",
            },
        )


# ---------------------------------------------------------------------------
# Tools
# ---------------------------------------------------------------------------


class TestDemoTools(DemoTestCase):

    # -- word_count ----------------------------------------------------------

    def test_word_count_returns_count(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        fake.send_init()
        resp = fake.send_tool_call(2, "word_count", {"text": "hello world foo"})
        self.assertIsNotNone(resp)
        assert resp is not None
        self.assertIsNone(resp.get("error"))
        self.assertEqual(resp["result"]["count"], 3)

    def test_word_count_calls_set_label(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        fake.send_init()
        fake.send_tool_call(2, "word_count", {"text": "one two"})
        msg = fake.wait_for_method("set_label")
        self.assertIsNotNone(msg, "expected set_label call")
        assert msg is not None
        self.assertEqual(msg["params"]["entry_id"], "last_wc")
        self.assertEqual(msg["params"]["label"], "2")
        fake.stop()

    def test_word_count_calls_notify(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        fake.send_init()
        fake.send_tool_call(2, "word_count", {"text": "a b c"})
        msg = fake.wait_for_method("notify")
        self.assertIsNotNone(msg, "expected notify call")
        assert msg is not None
        self.assertIn("3", msg["params"]["message"])
        fake.stop()

    # -- shell_run -----------------------------------------------------------

    def test_shell_run_calls_exec(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        fake.send_init()
        fake.send_tool_call(2, "shell_run", {"command": "echo", "args": ["hi"]})
        msg = fake.wait_for_method("exec")
        self.assertIsNotNone(msg, "expected exec call")
        assert msg is not None
        self.assertEqual(msg["params"]["command"], "echo")
        self.assertEqual(msg["params"]["args"], ["hi"])
        fake.stop()

    def test_shell_run_returns_exec_result(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        fake.send_init()
        resp = fake.send_tool_call(2, "shell_run", {"command": "echo"})
        self.assertIsNotNone(resp)
        assert resp is not None
        self.assertIsNone(resp.get("error"))
        result = resp["result"]
        self.assertEqual(result["stdout"], "mock output")
        self.assertEqual(result["exit_code"], 0)
        fake.stop()

    # -- list_tools ----------------------------------------------------------

    def test_list_tools_calls_get_active_tools(self) -> None:
        fake = FakeFir(active_tools=["bash", "read"])
        self.start_demo_ext(fake)
        fake.send_init()
        fake.send_tool_call(2, "list_tools")
        msg = fake.wait_for_method("get_active_tools")
        self.assertIsNotNone(msg, "expected get_active_tools call")
        fake.stop()

    def test_list_tools_returns_active_list(self) -> None:
        fake = FakeFir(active_tools=["bash", "read"])
        self.start_demo_ext(fake)
        fake.send_init()
        resp = fake.send_tool_call(2, "list_tools")
        self.assertIsNotNone(resp)
        assert resp is not None
        self.assertEqual(resp["result"]["tools"], ["bash", "read"])
        fake.stop()

    # -- pin_tools -----------------------------------------------------------

    def test_pin_tools_calls_set_active_tools(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        fake.send_init()
        fake.send_tool_call(2, "pin_tools", {"tools": ["bash"]})
        msg = fake.wait_for_method("set_active_tools")
        self.assertIsNotNone(msg, "expected set_active_tools call")
        assert msg is not None
        self.assertEqual(msg["params"]["names"], ["bash"])
        fake.stop()

    # -- change_model --------------------------------------------------------

    def test_change_model_calls_set_model(self) -> None:
        fake = FakeFir(model_ok=True)
        self.start_demo_ext(fake)
        fake.send_init()
        fake.send_tool_call(2, "change_model", {"provider": "anthropic", "model": "claude-3"})
        msg = fake.wait_for_method("set_model")
        self.assertIsNotNone(msg, "expected set_model call")
        assert msg is not None
        self.assertEqual(msg["params"]["provider"], "anthropic")
        self.assertEqual(msg["params"]["id"], "claude-3")
        fake.stop()

    def test_change_model_returns_ok(self) -> None:
        fake = FakeFir(model_ok=True)
        self.start_demo_ext(fake)
        fake.send_init()
        resp = fake.send_tool_call(2, "change_model", {"provider": "openai", "model": "gpt-4"})
        self.assertIsNotNone(resp)
        assert resp is not None
        self.assertTrue(resp["result"]["ok"])
        fake.stop()

    # -- inject_message ------------------------------------------------------

    def test_inject_custom_message_calls_send_message(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        fake.send_init()
        fake.send_tool_call(2, "inject_message", {"kind": "custom", "content": "hello"})
        msg = fake.wait_for_method("send_message")
        self.assertIsNotNone(msg, "expected send_message call")
        assert msg is not None
        self.assertEqual(msg["params"]["custom_type"], "demo_note")
        self.assertEqual(msg["params"]["content"], "hello")
        fake.stop()

    def test_inject_user_message_calls_send_user_message(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        fake.send_init()
        fake.send_tool_call(2, "inject_message", {"kind": "user", "content": "hi agent"})
        msg = fake.wait_for_method("send_user_message")
        self.assertIsNotNone(msg, "expected send_user_message call")
        assert msg is not None
        self.assertEqual(msg["params"]["content"], "hi agent")
        fake.stop()


# ---------------------------------------------------------------------------
# Hook: hook/tool_call
# ---------------------------------------------------------------------------


class TestDemoHook(DemoTestCase):
    def test_blocks_tool_with_blocked_prefix(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        fake.send_init()
        resp = fake.send_hook(2, "hook/tool_call", {"tool_name": "blocked:dangerous"})
        self.assertIsNotNone(resp)
        assert resp is not None
        self.assertIsNone(resp.get("error"))
        self.assertTrue(resp["result"]["block"])
        self.assertIn("blocked:dangerous", resp["result"]["reason"])
        fake.stop()

    def test_allows_normal_tool(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        fake.send_init()
        resp = fake.send_hook(2, "hook/tool_call", {"tool_name": "bash"})
        self.assertIsNotNone(resp)
        assert resp is not None
        self.assertIsNone(resp.get("error"))
        self.assertIsNone(resp["result"])
        fake.stop()


# ---------------------------------------------------------------------------
# Events
# ---------------------------------------------------------------------------


class TestDemoEvents(DemoTestCase):
    def _run_event(self, event_name: str, params: dict | None = None) -> "FakeFir":
        fake = FakeFir()
        self.start_demo_ext(fake)
        fake.send_init()
        fake.send_event(event_name, params)
        return fake

    # -- session_start -------------------------------------------------------

    def test_session_start_calls_set_status(self) -> None:
        fake = self._run_event("session_start")
        msg = fake.wait_for_method("set_status")
        fake.stop()
        self.assertIsNotNone(msg, "expected set_status after session_start")
        assert msg is not None
        self.assertEqual(msg["params"]["status"], "demo ready")

    # -- session_shutdown ----------------------------------------------------

    def test_session_shutdown_calls_set_status_clear(self) -> None:
        fake = self._run_event("session_shutdown")
        msg = fake.wait_for_method("set_status")
        fake.stop()
        self.assertIsNotNone(msg, "expected set_status after session_shutdown")
        assert msg is not None
        self.assertEqual(msg["params"]["status"], "")

    # -- agent_start ---------------------------------------------------------

    def test_agent_start_calls_set_session_name(self) -> None:
        fake = self._run_event("agent_start")
        msg = fake.wait_for_method("set_session_name")
        fake.stop()
        self.assertIsNotNone(msg, "expected set_session_name after agent_start")
        assert msg is not None
        self.assertEqual(msg["params"]["name"], "demo session")

    # -- agent_end -----------------------------------------------------------

    def test_agent_end_calls_notify(self) -> None:
        fake = self._run_event("agent_end")
        msg = fake.wait_for_method("notify")
        fake.stop()
        self.assertIsNotNone(msg, "expected notify after agent_end")
        assert msg is not None
        self.assertIn("finished", msg["params"]["message"].lower())

    def test_agent_end_clears_label(self) -> None:
        fake = self._run_event("agent_end")
        msg = fake.wait_for_method("clear_label")
        fake.stop()
        self.assertIsNotNone(msg, "expected clear_label after agent_end")
        assert msg is not None
        self.assertEqual(msg["params"]["entry_id"], "last_wc")

    # -- tool_execution_start ------------------------------------------------

    def test_tool_execution_start_calls_set_label(self) -> None:
        fake = self._run_event(
            "tool_execution_start",
            {"tool_call_id": "tc-99", "tool_name": "bash"},
        )
        msg = fake.wait_for_method("set_label")
        fake.stop()
        self.assertIsNotNone(msg, "expected set_label after tool_execution_start")
        assert msg is not None
        self.assertEqual(msg["params"]["entry_id"], "tc-99")
        self.assertIn("bash", msg["params"]["label"])

    # -- tool_execution_end --------------------------------------------------

    def test_tool_execution_end_clears_label(self) -> None:
        fake = self._run_event(
            "tool_execution_end",
            {"tool_call_id": "tc-99", "tool_name": "bash", "is_error": False},
        )
        msg = fake.wait_for_method("clear_label")
        fake.stop()
        self.assertIsNotNone(msg, "expected clear_label after tool_execution_end")
        assert msg is not None
        self.assertEqual(msg["params"]["entry_id"], "tc-99")

    # -- no-op events (subscribed but no outbound call) ----------------------

    def _assert_no_crash(self, event_name: str) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        fake.send_init()
        fake.send_event(event_name)
        # Give handlers time to run, then verify the process is still alive
        time.sleep(0.1)
        # Confirm we can still get a tool response (process alive)
        resp = fake.send_tool_call(2, "list_tools")
        fake.stop()
        self.assertIsNotNone(resp, f"process died after event {event_name}")

    def test_turn_start_no_crash(self) -> None:
        self._assert_no_crash("turn_start")

    def test_turn_end_no_crash(self) -> None:
        self._assert_no_crash("turn_end")

    def test_message_start_no_crash(self) -> None:
        self._assert_no_crash("message_start")

    def test_message_end_no_crash(self) -> None:
        self._assert_no_crash("message_end")


if __name__ == "__main__":
    unittest.main()
