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
from typing import ClassVar, Optional

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


_DEMO_EXT_NAME: Optional[str] = None


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
    fir_ext._providers.clear()
    fir_ext._provider_stream_handlers.clear()
    fir_ext._provider_list_models_handlers.clear()
    fir_ext._provider_resolve_custom_id_handlers.clear()
    _DEMO_EXT_NAME = None

    orig_run = fir_ext.run

    def _capture_run(*a: object, **kw: object) -> None:
        global _DEMO_EXT_NAME
        name_val = kw.get("name") or (a[0] if a else None)
        _DEMO_EXT_NAME = name_val if isinstance(name_val, str) else None

    fir_ext.run = _capture_run  # type: ignore[assignment]  # ty: ignore[invalid-assignment]
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

    def __init__(self, *, model_ok: bool = True):
        # Queue for fir → extension messages
        self._to_ext: queue.SimpleQueue[str] = queue.SimpleQueue()
        # All messages received from extension
        self._from_ext: list[dict] = []
        self._from_ext_lock = threading.Lock()
        self._from_ext_event = threading.Event()
        # Extension thread handle
        self._thread: Optional[threading.Thread] = None

        # Default auto-response data
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
        if method == "set_model":
            result: object = {"ok": self._model_ok}
        elif method == "exec":
            result = {"stdout": "mock output", "stderr": "", "exit_code": 0}
        elif method == "call_tool":
            name = (msg.get("params") or {}).get("name", "unknown")
            result = {
                "content": [{"text": f"mock result of {name}"}],
                "is_error": False,
            }
        elif method == "side_query":
            result = {"ok": True, "text": "mock synthesis"}
        elif method == "list_tools":
            result = [
                {"name": "bash", "description": "mock"},
                {"name": "read", "description": "mock"},
            ]
        elif method == "get_session_data":
            result = {"ok": True, "value": "mock_value"}
        elif method == "get_session_file":
            result = {"path": "/tmp/mock-session.jsonl"}
        elif method == "get_session_name":
            result = {"name": "mock-session-name"}
        elif method == "get_session_id":
            result = {"id": "mock-session-id-1234"}
        else:
            result = {"ok": True}
        resp = json.dumps({"jsonrpc": "2.0", "id": rid, "result": result}) + "\n"
        self._to_ext.put(resp)

    # -- test helpers -------------------------------------------------------

    def start_extension(self, name: Optional[str] = None) -> "FakeFir":
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

    def send_init(self, cwd: str = "/tmp", config_dirs: Optional[list] = None) -> dict:
        """Send init and wait for the response. Returns the parsed result."""
        params: dict = {"version": "1", "cwd": cwd}
        if config_dirs is not None:
            params["config_dirs"] = config_dirs
        self.send({
            "jsonrpc": "2.0",
            "id": 1,
            "method": "init",
            "params": params,
        })
        resp = self.wait_for_response(1)
        assert resp is not None, "no init response"
        return resp.get("result", {})

    def send_event(self, event_name: str, params: Optional[dict] = None) -> None:
        self.send({"jsonrpc": "2.0", "method": f"event/{event_name}", "params": params or {}})

    def send_tool_call(
        self, msg_id: int, tool_name: str, params: Optional[dict] = None
    ) -> Optional[dict]:
        self.send({
            "jsonrpc": "2.0",
            "id": msg_id,
            "method": "tool_call",
            "params": {"tool_call_id": f"tc-{msg_id}", "name": tool_name, "params": params or {}},
        })
        return self.wait_for_response(msg_id)

    def send_hook(
        self, msg_id: int, hook_name: str, params: Optional[dict] = None
    ) -> Optional[dict]:
        self.send({
            "jsonrpc": "2.0",
            "id": msg_id,
            "method": hook_name,
            "params": params or {},
        })
        return self.wait_for_response(msg_id)

    def wait_for_method(self, method: str, timeout: float = 3.0) -> Optional[dict]:
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

    def wait_for_response(self, msg_id: int, timeout: float = 3.0) -> Optional[dict]:
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
                "word_count", "shell_run",
                "change_model", "inject_message", "restart_demo",
                "batch_example", "show_config_dirs",
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

    # -- restart_demo --------------------------------------------------------

    def test_restart_demo_without_confirm_does_nothing(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        fake.send_init()
        fake.send_tool_call(2, "restart_demo", {"prompt": "x"})
        # No restart_session method should be observed.
        msg = fake.wait_for_method("restart_session", timeout=0.5)
        self.assertIsNone(msg, "restart_session should not fire without confirm")
        fake.stop()

    def test_restart_demo_with_confirm_calls_restart_session(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        fake.send_init()
        fake.send_tool_call(
            2, "restart_demo", {"prompt": "read /tmp/h.md", "confirm": "yes-really"}
        )
        msg = fake.wait_for_method("restart_session")
        self.assertIsNotNone(msg, "expected restart_session call")
        assert msg is not None
        self.assertEqual(msg["params"]["prompt"], "read /tmp/h.md")
        fake.stop()

    # -- batch_example -------------------------------------------------------

    def test_batch_example_calls_exec_and_call_tool(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        fake.send_init()
        resp = fake.send_tool_call(2, "batch_example", {"directory": "/tmp/myproject"})
        self.assertIsNotNone(resp)
        # Should have called exec to probe files
        msg = fake.wait_for_method("exec")
        self.assertIsNotNone(msg, "expected exec call to probe files")
        assert msg is not None
        self.assertEqual(msg["params"]["command"], "sh")
        # Should have called call_tool at least once (for git status Bash)
        ct = fake.wait_for_method("call_tool")
        self.assertIsNotNone(ct, "expected call_tool call")
        fake.stop()

    def test_batch_example_calls_side_query(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        fake.send_init()
        fake.send_tool_call(2, "batch_example", {"directory": "/tmp/proj"})
        msg = fake.wait_for_method("side_query")
        self.assertIsNotNone(msg, "expected side_query call")
        assert msg is not None
        # The synthesis prompt should include instructions
        question = msg["params"]["question"]
        self.assertIn("Instructions", question)
        fake.stop()

    def test_batch_example_reports_progress(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        fake.send_init()
        fake.send_tool_call(2, "batch_example", {"directory": "/tmp/proj"})
        msg = fake.wait_for_method("report_progress")
        self.assertIsNotNone(msg, "expected report_progress call")
        assert msg is not None
        self.assertIn("message", msg["params"])
        fake.stop()

    def test_batch_example_returns_synthesis(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        fake.send_init()
        resp = fake.send_tool_call(2, "batch_example", {"directory": "/tmp/proj"})
        self.assertIsNotNone(resp)
        assert resp is not None
        self.assertIsNone(resp.get("error"))
        # Result should be a string (the side_query synthesis)
        result = resp["result"]
        # The mock side_query returns "mock synthesis" — could be wrapped in content
        self.assertIsNotNone(result)
        fake.stop()

    def test_batch_example_with_extra_instructions(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        fake.send_init()
        fake.send_tool_call(2, "batch_example", {
            "directory": "/tmp/proj",
            "extra_instructions": "focus on test coverage",
        })
        msg = fake.wait_for_method("side_query")
        self.assertIsNotNone(msg, "expected side_query call")
        assert msg is not None
        question = msg["params"]["question"]
        self.assertIn("focus on test coverage", question)
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
    def _run_event(self, event_name: str, params: Optional[dict] = None) -> "FakeFir":
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

    def test_session_start_calls_set_session_data(self) -> None:
        fake = self._run_event("session_start")
        msg = fake.wait_for_method("set_session_data")
        fake.stop()
        self.assertIsNotNone(msg, "expected set_session_data after session_start")
        assert msg is not None
        self.assertEqual(msg["params"]["key"], "started")
        self.assertEqual(msg["params"]["value"], "true")

    def test_session_start_calls_prepend(self) -> None:
        fake = self._run_event("session_start")
        msg = fake.wait_for_method("prepend_context")
        fake.stop()
        self.assertIsNotNone(msg, "expected prepend_context after session_start")
        assert msg is not None
        self.assertEqual(msg["params"]["content"], "Demo extension is active.")

    def test_session_start_calls_agent_info(self) -> None:
        fake = self._run_event("session_start")
        msg = fake.wait_for_method("agent.info")
        fake.stop()
        self.assertIsNotNone(msg, "expected agent.info after session_start")

    def test_session_start_calls_get_session_file(self) -> None:
        fake = self._run_event("session_start")
        msg = fake.wait_for_method("get_session_file")
        fake.stop()
        self.assertIsNotNone(msg, "expected get_session_file after session_start")

    def test_session_start_calls_get_session_name(self) -> None:
        fake = self._run_event("session_start")
        msg = fake.wait_for_method("get_session_name")
        fake.stop()
        self.assertIsNotNone(msg, "expected get_session_name after session_start")

    # -- session_shutdown ----------------------------------------------------

    def test_session_shutdown_calls_get_session_data(self) -> None:
        fake = self._run_event("session_shutdown")
        msg = fake.wait_for_method("get_session_data")
        fake.stop()
        self.assertIsNotNone(msg, "expected get_session_data after session_shutdown")
        assert msg is not None
        self.assertEqual(msg["params"]["key"], "started")

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
        resp = fake.send_tool_call(2, "word_count", {"text": "hello"})
        fake.stop()
        self.assertIsNotNone(resp, f"process died after event {event_name}")

    def test_turn_start_no_crash(self) -> None:
        self._assert_no_crash("turn_start")

    def test_turn_end_calls_continue_session(self) -> None:
        fake = self._run_event("turn_end")
        msg = fake.wait_for_method("continue_session")
        fake.stop()
        self.assertIsNotNone(msg, "expected continue_session after turn_end")

    def test_message_start_no_crash(self) -> None:
        self._assert_no_crash("message_start")

    def test_message_end_no_crash(self) -> None:
        self._assert_no_crash("message_end")


# ---------------------------------------------------------------------------
# API coverage: ensure demo.py exercises every public ext API
# ---------------------------------------------------------------------------


class TestDemoAPIConverage(DemoTestCase):
    """Verify that demo.py exercises every public API surface so it stays
    useful as a comprehensive reference for extension authors."""

    _INTERNAL_METHODS: ClassVar[set[str]] = {"_call"}

    def _public_context_methods(self) -> set[str]:
        """Return the set of public method names on fir_ext.Context."""
        return {
            name
            for name in dir(fir_ext.Context)
            if not name.startswith("_")
            and callable(getattr(fir_ext.Context, name))
            and name not in self._INTERNAL_METHODS
        }

    def test_demo_covers_all_context_methods(self) -> None:
        """Every public Context method should be called somewhere in demo.py."""
        with open(_DEMO_PATH) as f:
            source = f.read()

        public_methods = self._public_context_methods()
        missing = {m for m in public_methods if f"ctx.{m}(" not in source}
        self.assertSetEqual(
            missing,
            set(),
            f"demo.py does not call these Context methods: {sorted(missing)}. "
            "Please add usage examples so the demo remains a complete API reference.",
        )

    def test_demo_covers_all_decorators(self) -> None:
        """demo.py should use every public decorator: @fir_ext.tool, @fir_ext.on,
        @fir_ext.command."""
        with open(_DEMO_PATH) as f:
            source = f.read()

        expected_decorators = {"@fir_ext.tool", "@fir_ext.on", "@fir_ext.command"}
        missing = {d for d in expected_decorators if d not in source}
        self.assertSetEqual(
            missing,
            set(),
            f"demo.py does not use these decorators: {sorted(missing)}. "
            "Please add examples so the demo remains a complete API reference.",
        )

    def test_demo_covers_all_events(self) -> None:
        """demo.py should subscribe to every known event type."""
        known_events = {
            "session_start", "session_shutdown",
            "agent_start", "agent_end",
            "turn_start", "turn_end",
            "message_start", "message_end",
            "tool_execution_start", "tool_execution_end",
        }
        registered = set(fir_ext._event_handlers.keys())
        missing = known_events - registered
        self.assertSetEqual(
            missing,
            set(),
            f"demo.py does not handle these events: {sorted(missing)}. "
            "Please add handlers so the demo remains a complete API reference.",
        )

    def test_demo_registers_hook(self) -> None:
        """demo.py should register at least the hook/tool_call hook."""
        self.assertIn(
            "hook/tool_call",
            fir_ext._hook_handlers,
            "demo.py should register a hook/tool_call handler.",
        )

    def test_tests_cover_all_context_methods(self) -> None:
        """This test file should reference every public Context method."""
        with open(__file__) as f:
            test_source = f.read()

        public_methods = self._public_context_methods()
        missing = {m for m in public_methods if m not in test_source}
        self.assertSetEqual(
            missing,
            set(),
            f"This test file does not reference these Context methods: {sorted(missing)}. "
            "Please add test coverage.",
        )


if __name__ == "__main__":
    unittest.main()


# ---------------------------------------------------------------------------
# CLI verb (cli_invoke / cli_stdout / cli_stderr / cli_stdin)
# ---------------------------------------------------------------------------


class TestDemoCLIVerb(DemoTestCase):
    def test_init_advertises_cli_verbs(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        result = fake.send_init()
        fake.stop()
        self.assertIn("demo-cli", result.get("cli_verbs", []))

    def test_cli_invoke_dispatch_returns_exit_code(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        fake.send_init()
        fake.send({
            "jsonrpc": "2.0",
            "id": 100,
            "method": "cli_invoke",
            "params": {
                "verb": "demo-cli",
                "argv": ["hello", "world"],
                "cwd": "/tmp",
                "stdin_is_tty": True,
                "stdout_is_tty": True,
                "stderr_is_tty": True,
            },
        })
        resp = fake.wait_for_response(100)
        fake.stop()
        assert resp is not None
        self.assertEqual(resp["result"]["exit_code"], 0)
        # Check that cli_stdout was emitted with the argv echo.
        stdouts = fake.received_with_method("cli_stdout")
        joined = "".join(m["params"]["data"] for m in stdouts)
        self.assertIn("hello", joined)
        self.assertIn("world", joined)

    def test_cli_invoke_unknown_verb_errors(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        fake.send_init()
        fake.send({
            "jsonrpc": "2.0",
            "id": 101,
            "method": "cli_invoke",
            "params": {"verb": "no-such-verb", "argv": []},
        })
        resp = fake.wait_for_response(101)
        fake.stop()
        assert resp is not None
        self.assertIn("error", resp)

    def test_cli_stdin_forwarded_to_handler(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        fake.send_init()
        fake.send({
            "jsonrpc": "2.0",
            "id": 102,
            "method": "cli_invoke",
            "params": {
                "verb": "demo-cli",
                "argv": [],
                "cwd": "/tmp",
                "stdin_is_tty": False,  # signals piped stdin → handler will read
                "stdout_is_tty": True,
                "stderr_is_tty": True,
            },
        })
        # Forward two stdin lines, then EOF.
        fake.send({"jsonrpc": "2.0", "method": "cli_stdin", "params": {"data": "line1\n"}})
        fake.send({"jsonrpc": "2.0", "method": "cli_stdin", "params": {"data": "line2\n"}})
        fake.send({"jsonrpc": "2.0", "method": "cli_stdin", "params": {"eof": True}})
        resp = fake.wait_for_response(102, timeout=5.0)
        fake.stop()
        assert resp is not None
        self.assertEqual(resp["result"]["exit_code"], 0)
        joined = "".join(
            m["params"]["data"] for m in fake.received_with_method("cli_stdout")
        )
        self.assertIn("line1", joined)
        self.assertIn("line2", joined)


# ---------------------------------------------------------------------------
# Hosted provider — exercises the @provider_stream / register_provider surface
# ---------------------------------------------------------------------------


class TestDemoProvider(DemoTestCase):
    def test_init_advertises_echo_provider(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        result = fake.send_init()
        fake.stop()
        providers = result.get("providers", [])
        ids = [p.get("id") for p in providers]
        self.assertIn("echo", ids, f"providers={providers}")
        echo = next(p for p in providers if p["id"] == "echo")
        self.assertEqual(echo.get("display_name"), "Echo")
        self.assertEqual(echo.get("env_keys", {}).get("primary"), "ECHO_API_KEY")
        model_ids = [m.get("id") for m in echo.get("models", [])]
        self.assertEqual(model_ids, ["echo-1"])
        self.assertTrue(echo.get("supports_live_list"))

    def test_provider_list_models_returns_model_ids(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        fake.send_init()
        fake.send({
            "jsonrpc": "2.0", "id": 200,
            "method": "provider/listModels",
            "params": {"provider_id": "echo", "base_url": "", "api_key": ""},
        })
        resp = fake.wait_for_response(200)
        fake.stop()
        assert resp is not None
        self.assertIsNone(resp.get("error"))
        self.assertEqual(resp["result"], {"model_ids": ["echo-1"]})

    def _collect_stream_events(
        self, fake: "FakeFir", stream_id: str, timeout: float = 3.0
    ) -> list[dict]:
        deadline = time.monotonic() + timeout
        events: list[dict] = []
        seen_terminal = False
        while time.monotonic() < deadline and not seen_terminal:
            with fake._from_ext_lock:
                for m in fake._from_ext:
                    if m.get("method") != "provider.stream.event":
                        continue
                    p = m.get("params") or {}
                    if p.get("stream_id") != stream_id:
                        continue
                    ev = p.get("event") or {}
                    if ev not in events:
                        events.append(ev)
                        if ev.get("type") in ("done", "error"):
                            seen_terminal = True
                            break
            if seen_terminal:
                break
            fake._from_ext_event.wait(timeout=0.05)
            fake._from_ext_event.clear()
        return events

    def test_provider_stream_emits_ordered_events(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        fake.send_init()
        stream_id = "stream-abc"
        fake.send({
            "jsonrpc": "2.0", "id": 201,
            "method": "provider/stream/start",
            "params": {
                "provider_id": "echo",
                "stream_id": stream_id,
                "model": {"id": "echo-1", "api": "ext:echo", "provider": "echo"},
                "prompt": {
                    "messages": [
                        {"role": "user", "content": "hello"},
                    ],
                },
                "options": {},
            },
        })
        # Ack returns immediately.
        resp = fake.wait_for_response(201)
        assert resp is not None
        self.assertIsNone(resp.get("error"))
        self.assertEqual(resp["result"], {})

        events = self._collect_stream_events(fake, stream_id)
        fake.stop()
        types = [e.get("type") for e in events]
        self.assertIn("text_start", types)
        self.assertIn("text_delta", types)
        self.assertIn("text_end", types)
        self.assertEqual(types[-1], "done")
        # Concatenated deltas should equal the echoed text.
        deltas = "".join(e.get("delta", "") for e in events if e.get("type") == "text_delta")
        self.assertEqual(deltas, "Echo: hello")
        # Final done message should carry a usage block.
        done = events[-1]
        self.assertEqual(done.get("reason"), "stop")
        msg = done.get("message") or {}
        self.assertEqual(msg.get("provider"), "echo")
        self.assertIn("usage", msg)

    def test_provider_stream_cancel_acked(self) -> None:
        fake = FakeFir()
        self.start_demo_ext(fake)
        fake.send_init()
        fake.send({
            "jsonrpc": "2.0", "id": 202,
            "method": "provider/stream/cancel",
            "params": {"stream_id": "no-such-stream"},
        })
        resp = fake.wait_for_response(202)
        fake.stop()
        assert resp is not None
        self.assertIsNone(resp.get("error"))
        self.assertEqual(resp["result"], {"ok": True})


if __name__ == "__main__":
    unittest.main()





class TestTypedSurface(unittest.TestCase):
    """Smoke tests for the TypedDict surface introduced in the strong-typing
    pass.  TypedDicts are plain dict at runtime, so the goal here is to
    confirm:

      * the names exist and are importable from ``fir_ext``,
      * they accept the wire shapes our code actually emits/consumes,
      * the public ``__all__`` covers the names we promised to export.
    """

    def test_typeddicts_are_dicts(self) -> None:
        tr: fir_ext.ToolResult = {"content": [{"type": "text", "text": "hi"}], "is_error": False}
        self.assertIsInstance(tr, dict)
        er: fir_ext.ExecResult = {"stdout": "x", "stderr": "", "exit_code": 0}
        self.assertEqual(er["exit_code"], 0)

    def test_message_end_params_shape(self) -> None:
        params: fir_ext.MessageEndParams = {
            "role": "assistant",
            "provider": "anthropic",
            "model": "claude",
            "usage": {
                "input": 1, "output": 2, "cache_read": 0, "cache_write": 0,
                "total_tokens": 3,
                "cost": {
                    "input": 0.0, "output": 0.0, "cache_read": 0.0,
                    "cache_write": 0.0, "total": 0.0,
                },
            },
        }
        self.assertEqual(params["role"], "assistant")
        self.assertIn("cost", params["usage"])

    def test_hook_result_optional_block(self) -> None:
        allow: fir_ext.ToolCallHookResult = {}
        block: fir_ext.ToolCallHookResult = {"block": True, "reason": "policy"}
        self.assertNotIn("block", allow)
        self.assertTrue(block["block"])

    def test_all_lists_typed_names(self) -> None:
        promised = {
            "ToolResult", "ExecResult", "MessageEndParams",
            "ToolCallHookParams", "ToolCallHookResult",
            "CommandHookResult", "OkResult",
            "SessionStartParams", "ToolExecutionStartParams",
            "ToolExecutionEndParams", "AgentLifecycleParams",
            "Context", "Host",
        }
        missing = promised - set(fir_ext.__all__)
        self.assertSetEqual(missing, set(), f"missing from __all__: {sorted(missing)}")
