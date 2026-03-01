"""Tests for fir_ext — the Python SDK for fir external process extensions."""

import io
import json
import threading
import unittest

import fir_ext


def _make_input(*messages):
    """Build a readable stream from a sequence of JSON-RPC dicts."""
    return io.StringIO("\n".join(json.dumps(m) for m in messages) + "\n")


def _read_all_messages(stream):
    """Parse all newline-delimited JSON messages from a StringIO."""
    stream.seek(0)
    msgs = []
    for line in stream:
        line = line.strip()
        if line:
            msgs.append(json.loads(line))
    return msgs


class TestInitHandshake(unittest.TestCase):
    def setUp(self):
        fir_ext._tools.clear()
        fir_ext._tool_handlers.clear()
        fir_ext._hook_handlers.clear()
        fir_ext._event_handlers.clear()

    def test_init_returns_capabilities(self):
        @fir_ext.tool("my_tool", "A test tool", {"type": "object", "properties": {"x": {"type": "string"}}})
        def my_tool(params, ctx):
            return {"ok": True}

        @fir_ext.on("session_start")
        def on_start(params, ctx):
            pass

        inp = _make_input({"jsonrpc": "2.0", "id": 1, "method": "init", "params": {"version": "1", "cwd": "/tmp"}})
        out = io.StringIO()
        fir_ext.run(name="test-ext", input_stream=inp, output_stream=out)

        msgs = _read_all_messages(out)
        self.assertEqual(len(msgs), 1)
        resp = msgs[0]
        self.assertEqual(resp["id"], 1)
        result = resp["result"]
        self.assertEqual(result["name"], "test-ext")
        self.assertEqual(len(result["tools"]), 1)
        self.assertEqual(result["tools"][0]["name"], "my_tool")
        self.assertIn("session_start", result["events"])


class TestToolCall(unittest.TestCase):
    def setUp(self):
        fir_ext._tools.clear()
        fir_ext._tool_handlers.clear()
        fir_ext._hook_handlers.clear()
        fir_ext._event_handlers.clear()

    def test_tool_call_success(self):
        @fir_ext.tool("add", "Add two numbers", {"type": "object", "properties": {"a": {"type": "number"}, "b": {"type": "number"}}})
        def add(params, ctx):
            return {"sum": params["a"] + params["b"]}

        inp = _make_input(
            {"jsonrpc": "2.0", "id": 1, "method": "init", "params": {}},
            {"jsonrpc": "2.0", "id": 2, "method": "tool_call", "params": {"name": "add", "params": {"a": 3, "b": 4}}},
        )
        out = io.StringIO()
        fir_ext.run(input_stream=inp, output_stream=out)

        msgs = _read_all_messages(out)
        self.assertEqual(len(msgs), 2)
        self.assertEqual(msgs[1]["result"], {"sum": 7})

    def test_tool_call_unknown(self):
        inp = _make_input(
            {"jsonrpc": "2.0", "id": 1, "method": "init", "params": {}},
            {"jsonrpc": "2.0", "id": 2, "method": "tool_call", "params": {"name": "nope", "params": {}}},
        )
        out = io.StringIO()
        fir_ext.run(input_stream=inp, output_stream=out)

        msgs = _read_all_messages(out)
        self.assertEqual(msgs[1]["error"]["code"], -32601)

    def test_tool_call_error(self):
        @fir_ext.tool("fail", "Always fails")
        def fail(params, ctx):
            raise fir_ext.ToolError("boom", code=-32001)

        inp = _make_input(
            {"jsonrpc": "2.0", "id": 1, "method": "init", "params": {}},
            {"jsonrpc": "2.0", "id": 2, "method": "tool_call", "params": {"name": "fail", "params": {}}},
        )
        out = io.StringIO()
        fir_ext.run(input_stream=inp, output_stream=out)

        msgs = _read_all_messages(out)
        self.assertEqual(msgs[1]["error"]["code"], -32001)
        self.assertEqual(msgs[1]["error"]["message"], "boom")

    def test_tool_call_unhandled_exception(self):
        @fir_ext.tool("oops", "Raises unexpected error")
        def oops(params, ctx):
            raise ValueError("unexpected")

        inp = _make_input(
            {"jsonrpc": "2.0", "id": 1, "method": "init", "params": {}},
            {"jsonrpc": "2.0", "id": 2, "method": "tool_call", "params": {"name": "oops", "params": {}}},
        )
        out = io.StringIO()
        fir_ext.run(input_stream=inp, output_stream=out)

        msgs = _read_all_messages(out)
        self.assertEqual(msgs[1]["error"]["code"], -32000)
        self.assertIn("unexpected", msgs[1]["error"]["message"])

    def test_tool_call_string_auto_wrap(self):
        """When a tool handler returns a plain string, verify it is wrapped
        into the structured content format."""
        @fir_ext.tool("say_hi", "Returns a string")
        def say_hi(params, ctx):
            return "hello world"

        inp = _make_input(
            {"jsonrpc": "2.0", "id": 1, "method": "init", "params": {}},
            {"jsonrpc": "2.0", "id": 2, "method": "tool_call", "params": {"name": "say_hi", "params": {}}},
        )
        out = io.StringIO()
        fir_ext.run(input_stream=inp, output_stream=out)

        msgs = _read_all_messages(out)
        self.assertEqual(len(msgs), 2)
        result = msgs[1]["result"]
        self.assertEqual(result["content"], [{"text": "hello world"}])
        self.assertFalse(result["is_error"])


class TestHooks(unittest.TestCase):
    def setUp(self):
        fir_ext._tools.clear()
        fir_ext._tool_handlers.clear()
        fir_ext._hook_handlers.clear()
        fir_ext._event_handlers.clear()

    def test_hook_dispatch(self):
        @fir_ext.on("hook/tool_call")
        def intercept(params, ctx):
            if params.get("name") == "dangerous":
                return {"block": True, "reason": "too dangerous"}
            return None

        inp = _make_input(
            {"jsonrpc": "2.0", "id": 1, "method": "init", "params": {}},
            {"jsonrpc": "2.0", "id": 2, "method": "hook/tool_call", "params": {"name": "dangerous"}},
        )
        out = io.StringIO()
        fir_ext.run(input_stream=inp, output_stream=out)

        msgs = _read_all_messages(out)
        self.assertEqual(msgs[1]["result"], {"block": True, "reason": "too dangerous"})

    def test_hook_unregistered_returns_null(self):
        inp = _make_input(
            {"jsonrpc": "2.0", "id": 1, "method": "init", "params": {}},
            {"jsonrpc": "2.0", "id": 2, "method": "hook/input", "params": {}},
        )
        out = io.StringIO()
        fir_ext.run(input_stream=inp, output_stream=out)

        msgs = _read_all_messages(out)
        self.assertIsNone(msgs[1]["result"])


class TestEvents(unittest.TestCase):
    def setUp(self):
        fir_ext._tools.clear()
        fir_ext._tool_handlers.clear()
        fir_ext._hook_handlers.clear()
        fir_ext._event_handlers.clear()

    def test_event_fires_handler(self):
        received = []

        @fir_ext.on("turn_end")
        def on_turn_end(params, ctx):
            received.append(params)

        inp = _make_input(
            {"jsonrpc": "2.0", "id": 1, "method": "init", "params": {}},
            {"jsonrpc": "2.0", "method": "event/turn_end", "params": {"turn": 3}},
        )
        out = io.StringIO()
        fir_ext.run(input_stream=inp, output_stream=out)

        self.assertEqual(received, [{"turn": 3}])

    def test_unknown_event_ignored(self):
        inp = _make_input(
            {"jsonrpc": "2.0", "id": 1, "method": "init", "params": {}},
            {"jsonrpc": "2.0", "method": "event/unknown_thing", "params": {}},
        )
        out = io.StringIO()
        # Should not raise
        fir_ext.run(input_stream=inp, output_stream=out)


class TestContext(unittest.TestCase):
    def setUp(self):
        fir_ext._tools.clear()
        fir_ext._tool_handlers.clear()
        fir_ext._hook_handlers.clear()
        fir_ext._event_handlers.clear()

    def test_context_outbound_call(self):
        """Simulate extension calling ctx.set_status() during a tool_call.

        The handler runs in a thread; the main read loop stays free to
        deliver the fir response for set_status back to ctx._call().
        """
        @fir_ext.tool("do_status", "Sets status")
        def do_status(params, ctx):
            ctx.set_status("working")
            return {"done": True}

        out = io.StringIO()

        # We need a custom input stream: after delivering init + tool_call,
        # it waits for the handler's outbound set_status request to appear
        # on out, then provides the matching response on the input.
        class FakeInput:
            def __init__(self):
                self._lines = [
                    json.dumps({"jsonrpc": "2.0", "id": 1, "method": "init", "params": {}}) + "\n",
                    json.dumps({"jsonrpc": "2.0", "id": 2, "method": "tool_call", "params": {"name": "do_status", "params": {}}}) + "\n",
                ]
                self._idx = 0
                self._extra = []
                self._extra_ready = threading.Event()

            def readline(self):
                if self._idx < len(self._lines):
                    line = self._lines[self._idx]
                    self._idx += 1
                    return line
                # Wait for an injected line (the fir response to set_status)
                if self._extra_ready.wait(timeout=10):
                    self._extra_ready.clear()
                    if self._extra:
                        return self._extra.pop(0)
                return ""  # EOF

            def inject(self, msg):
                self._extra.append(json.dumps(msg) + "\n")
                self._extra_ready.set()

        fake_in = FakeInput()

        def watch_and_respond():
            """Watch out for the set_status request and inject a response."""
            import time
            seen = set()
            for _ in range(200):
                time.sleep(0.02)
                out.seek(0)
                for line in out.read().strip().split("\n"):
                    if not line or line in seen:
                        continue
                    seen.add(line)
                    msg = json.loads(line)
                    if msg.get("method") == "set_status":
                        fake_in.inject({"jsonrpc": "2.0", "id": msg["id"], "result": None})
                        # After response delivered, handler finishes, main loop
                        # will read EOF and exit. Give it a moment then signal EOF.
                        time.sleep(0.1)
                        fake_in.inject(None)  # won't work, use empty
                        fake_in._extra.append("")
                        fake_in._extra_ready.set()
                        return

        t = threading.Thread(target=watch_and_respond, daemon=True)
        t.start()

        fir_ext.run(input_stream=fake_in, output_stream=out)
        t.join(timeout=5)

        msgs = _read_all_messages(out)
        # Verify set_status was called outbound
        self.assertTrue(any(m.get("method") == "set_status" for m in msgs))
        # Verify tool_call got result
        self.assertTrue(any(m.get("result") == {"done": True} for m in msgs))


class TestDecorators(unittest.TestCase):
    def setUp(self):
        fir_ext._tools.clear()
        fir_ext._tool_handlers.clear()
        fir_ext._hook_handlers.clear()
        fir_ext._event_handlers.clear()

    def test_tool_decorator_registers(self):
        @fir_ext.tool("t1", "desc1")
        def t1(params, ctx):
            pass

        self.assertEqual(len(fir_ext._tools), 1)
        self.assertEqual(fir_ext._tools[0]["name"], "t1")
        self.assertIn("t1", fir_ext._tool_handlers)

    def test_on_decorator_event(self):
        @fir_ext.on("session_start")
        def h(params, ctx):
            pass

        self.assertIn("session_start", fir_ext._event_handlers)
        self.assertEqual(len(fir_ext._hook_handlers), 0)

    def test_on_decorator_hook(self):
        @fir_ext.on("hook/tool_call")
        def h(params, ctx):
            pass

        self.assertIn("hook/tool_call", fir_ext._hook_handlers)
        self.assertEqual(len(fir_ext._event_handlers), 0)

    def test_default_parameters(self):
        @fir_ext.tool("bare", "no params given")
        def bare(params, ctx):
            pass

        self.assertEqual(fir_ext._tools[0]["parameters"], {"type": "object", "properties": {}})


class TestJsonRpcHelpers(unittest.TestCase):
    def test_make_response(self):
        r = fir_ext._make_response(42, {"ok": True})
        self.assertEqual(r, {"jsonrpc": "2.0", "id": 42, "result": {"ok": True}})

    def test_make_error(self):
        r = fir_ext._make_error(1, -32600, "bad")
        self.assertEqual(r["error"]["code"], -32600)

    def test_read_write_roundtrip(self):
        buf = io.StringIO()
        msg = {"jsonrpc": "2.0", "id": 1, "method": "test", "params": {}}
        fir_ext._write_message(msg, buf)
        buf.seek(0)
        got = fir_ext._read_message(buf)
        self.assertEqual(got["method"], "test")

    def test_read_eof(self):
        buf = io.StringIO("")
        self.assertIsNone(fir_ext._read_message(buf))


if __name__ == "__main__":
    unittest.main()
