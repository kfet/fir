"""Tests for fir_ext — the Python SDK for fir external process extensions."""

import io
import json
import threading
import time
import unittest

import fir_ext

_TOOL_PARAMS = {
    "type": "object",
    "properties": {"x": {"type": "string"}},
}
_ADD_PARAMS = {
    "type": "object",
    "properties": {"a": {"type": "number"}, "b": {"type": "number"}},
}


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


def _init_msg(msg_id=1):
    return {"jsonrpc": "2.0", "id": msg_id, "method": "init", "params": {}}


def _tool_call_msg(name, params=None, msg_id=2):
    return {
        "jsonrpc": "2.0",
        "id": msg_id,
        "method": "tool_call",
        "params": {"name": name, "params": params or {}},
    }


def _hook_msg(hook_name, params=None, msg_id=2):
    return {
        "jsonrpc": "2.0",
        "id": msg_id,
        "method": hook_name,
        "params": params or {},
    }


def _event_msg(event_name, params=None):
    return {
        "jsonrpc": "2.0",
        "method": f"event/{event_name}",
        "params": params or {},
    }


class TestInitHandshake(unittest.TestCase):
    def setUp(self):
        fir_ext._tools.clear()
        fir_ext._tool_handlers.clear()
        fir_ext._hook_handlers.clear()
        fir_ext._event_handlers.clear()
        fir_ext._commands.clear()
        fir_ext._command_handlers.clear()

    def test_init_returns_capabilities(self):
        @fir_ext.tool("my_tool", "A test tool", _TOOL_PARAMS)
        def my_tool(params, ctx):
            return {"ok": True}

        @fir_ext.on("session_start")
        def on_start(params, ctx):
            pass

        init = {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "init",
            "params": {"version": "1", "cwd": "/tmp"},
        }
        inp = _make_input(init)
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
        fir_ext._commands.clear()
        fir_ext._command_handlers.clear()

    def test_tool_call_success(self):
        @fir_ext.tool("add", "Add two numbers", _ADD_PARAMS)
        def add(params, ctx):
            return {"sum": params["a"] + params["b"]}

        inp = _make_input(
            _init_msg(),
            _tool_call_msg("add", {"a": 3, "b": 4}),
        )
        out = io.StringIO()
        fir_ext.run(input_stream=inp, output_stream=out)

        msgs = _read_all_messages(out)
        self.assertEqual(len(msgs), 2)
        self.assertEqual(msgs[1]["result"], {"sum": 7})

    def test_tool_call_unknown(self):
        inp = _make_input(
            _init_msg(),
            _tool_call_msg("nope"),
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
            _init_msg(),
            _tool_call_msg("fail"),
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
            _init_msg(),
            _tool_call_msg("oops"),
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
            _init_msg(),
            _tool_call_msg("say_hi"),
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
        fir_ext._commands.clear()
        fir_ext._command_handlers.clear()

    def test_hook_dispatch(self):
        @fir_ext.on("hook/tool_call")
        def intercept(params, ctx):
            if params.get("name") == "dangerous":
                return {"block": True, "reason": "too dangerous"}
            return None

        inp = _make_input(
            _init_msg(),
            _hook_msg("hook/tool_call", {"name": "dangerous"}),
        )
        out = io.StringIO()
        fir_ext.run(input_stream=inp, output_stream=out)

        msgs = _read_all_messages(out)
        self.assertEqual(
            msgs[1]["result"],
            {"block": True, "reason": "too dangerous"},
        )

    def test_hook_unregistered_returns_null(self):
        inp = _make_input(
            _init_msg(),
            _hook_msg("hook/input"),
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
        fir_ext._commands.clear()
        fir_ext._command_handlers.clear()

    def test_event_fires_handler(self):
        received = []

        @fir_ext.on("turn_end")
        def on_turn_end(params, ctx):
            received.append(params)

        inp = _make_input(
            _init_msg(),
            _event_msg("turn_end", {"turn": 3}),
        )
        out = io.StringIO()
        fir_ext.run(input_stream=inp, output_stream=out)

        self.assertEqual(received, [{"turn": 3}])

    def test_unknown_event_ignored(self):
        inp = _make_input(
            _init_msg(),
            _event_msg("unknown_thing"),
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
        fir_ext._commands.clear()
        fir_ext._command_handlers.clear()

    def test_context_outbound_call(self):
        """Simulate extension calling ctx.set_status() during a tool_call."""

        @fir_ext.tool("do_status", "Sets status")
        def do_status(params, ctx):
            ctx.set_status("working")
            return {"done": True}

        out = io.StringIO()

        class FakeInput:
            def __init__(self):
                self._lines = [
                    json.dumps(_init_msg()) + "\n",
                    json.dumps(_tool_call_msg("do_status")) + "\n",
                ]
                self._idx = 0
                self._extra: list[str] = []
                self._extra_ready = threading.Event()

            def readline(self):
                if self._idx < len(self._lines):
                    line = self._lines[self._idx]
                    self._idx += 1
                    return line
                if self._extra_ready.wait(timeout=10):
                    self._extra_ready.clear()
                    if self._extra:
                        return self._extra.pop(0)
                return ""

            def inject(self, msg):
                self._extra.append(json.dumps(msg) + "\n")
                self._extra_ready.set()

        fake_in = FakeInput()

        def watch_and_respond():
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
                        resp = {
                            "jsonrpc": "2.0",
                            "id": msg["id"],
                            "result": None,
                        }
                        fake_in.inject(resp)
                        time.sleep(0.1)
                        fake_in._extra.append("")
                        fake_in._extra_ready.set()
                        return

        t = threading.Thread(target=watch_and_respond, daemon=True)
        t.start()

        fir_ext.run(input_stream=fake_in, output_stream=out)
        t.join(timeout=5)

        msgs = _read_all_messages(out)
        self.assertTrue(
            any(m.get("method") == "set_status" for m in msgs),
        )
        self.assertTrue(
            any(m.get("result") == {"done": True} for m in msgs),
        )


class TestDecorators(unittest.TestCase):
    def setUp(self):
        fir_ext._tools.clear()
        fir_ext._tool_handlers.clear()
        fir_ext._hook_handlers.clear()
        fir_ext._event_handlers.clear()
        fir_ext._commands.clear()
        fir_ext._command_handlers.clear()
        fir_ext._commands.clear()
        fir_ext._command_handlers.clear()

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

        self.assertEqual(
            fir_ext._tools[0]["parameters"],
            {"type": "object", "properties": {}},
        )

    def test_command_decorator_registers(self):
        @fir_ext.command(name="greet", description="Say hello")
        def greet(args, ctx):
            return {"message": "hello"}

        self.assertEqual(len(fir_ext._commands), 1)
        self.assertEqual(fir_ext._commands[0]["name"], "greet")
        self.assertEqual(fir_ext._commands[0]["description"], "Say hello")
        self.assertIn("greet", fir_ext._command_handlers)


class TestCommands(unittest.TestCase):
    def setUp(self):
        fir_ext._tools.clear()
        fir_ext._tool_handlers.clear()
        fir_ext._hook_handlers.clear()
        fir_ext._event_handlers.clear()
        fir_ext._commands.clear()
        fir_ext._command_handlers.clear()
        fir_ext._commands.clear()
        fir_ext._command_handlers.clear()

    def test_init_includes_commands(self):
        @fir_ext.command(name="ping", description="Ping")
        def ping(args, ctx):
            return {"message": "pong"}

        inp = _make_input(_init_msg())
        out = io.StringIO()
        fir_ext.run(name="cmd-ext", input_stream=inp, output_stream=out)

        msgs = _read_all_messages(out)
        result = msgs[0]["result"]
        self.assertEqual(len(result["commands"]), 1)
        self.assertEqual(result["commands"][0]["name"], "ping")
        self.assertEqual(result["commands"][0]["description"], "Ping")

    def test_hook_command_dispatched(self):
        @fir_ext.command(name="greet", description="Greet")
        def greet(args, ctx):
            name = args[0] if args else "world"
            return {"message": f"hello {name}"}

        inp = _make_input(
            _init_msg(),
            _hook_msg("hook/command", {"name": "greet", "args": ["alice"]}),
        )
        out = io.StringIO()
        fir_ext.run(input_stream=inp, output_stream=out)

        msgs = _read_all_messages(out)
        self.assertEqual(msgs[1]["result"], {"message": "hello alice"})

    def test_hook_command_unknown_returns_null(self):
        inp = _make_input(
            _init_msg(),
            _hook_msg("hook/command", {"name": "nope", "args": []}),
        )
        out = io.StringIO()
        fir_ext.run(input_stream=inp, output_stream=out)

        msgs = _read_all_messages(out)
        self.assertIsNone(msgs[1]["result"])

    def test_hook_command_error(self):
        @fir_ext.command(name="fail", description="Always fails")
        def fail(args, ctx):
            raise ValueError("boom")

        inp = _make_input(
            _init_msg(),
            _hook_msg("hook/command", {"name": "fail", "args": []}),
        )
        out = io.StringIO()
        fir_ext.run(input_stream=inp, output_stream=out)

        msgs = _read_all_messages(out)
        self.assertIn("error", msgs[1])
        self.assertIn("boom", msgs[1]["error"]["message"])


class TestJsonRpcHelpers(unittest.TestCase):
    def test_make_response(self):
        r = fir_ext._make_response(42, {"ok": True})
        self.assertEqual(
            r,
            {"jsonrpc": "2.0", "id": 42, "result": {"ok": True}},
        )

    def test_make_error(self):
        r = fir_ext._make_error(1, -32600, "bad")
        self.assertEqual(r["error"]["code"], -32600)

    def test_read_write_roundtrip(self):
        buf = io.StringIO()
        msg = {"jsonrpc": "2.0", "id": 1, "method": "test", "params": {}}
        fir_ext._write_message(msg, buf)
        buf.seek(0)
        got = fir_ext._read_message(buf)
        assert got is not None
        self.assertEqual(got["method"], "test")

    def test_read_eof(self):
        buf = io.StringIO("")
        self.assertIsNone(fir_ext._read_message(buf))


# ---------------------------------------------------------------------------
# CLI verbs (cli_verb / on_cli_signal / Host)
# ---------------------------------------------------------------------------


class TestCLIVerb(unittest.TestCase):
    def setUp(self):
        # Reset module-level CLI verb state so tests don't interfere.
        fir_ext._cli_verb_handlers.clear()
        fir_ext._cli_signal_handlers.clear()

    def tearDown(self):
        fir_ext._cli_verb_handlers.clear()
        fir_ext._cli_signal_handlers.clear()

    def test_cli_verb_decorator_registers(self):
        @fir_ext.cli_verb("greet", summary="say hello")
        def greet(argv, host):
            return 0

        self.assertIn("greet", fir_ext._cli_verb_handlers)
        self.assertIs(fir_ext._cli_verb_handlers["greet"], greet)

    def test_on_cli_signal_appends(self):
        calls = []

        @fir_ext.on_cli_signal
        def handler(name, host):
            calls.append(name)

        self.assertEqual(len(fir_ext._cli_signal_handlers), 1)
        # Multiple registrations stack.
        @fir_ext.on_cli_signal
        def handler2(name, host):
            calls.append(("h2", name))

        self.assertEqual(len(fir_ext._cli_signal_handlers), 2)


class TestHost(unittest.TestCase):
    def _host_with_buf(self):
        buf = io.StringIO()
        return fir_ext.Host(out=buf), buf

    def _decode_stream(self, buf):
        """Pull cli_stdout/cli_stderr notification payloads out of buf."""
        buf.seek(0)
        out = []
        for line in buf:
            line = line.strip()
            if not line:
                continue
            msg = json.loads(line)
            out.append((msg.get("method"), msg.get("params", {}).get("data", "")))
        return out

    def test_print_emits_cli_stdout(self):
        host, buf = self._host_with_buf()
        host.print("hello")
        self.assertEqual(self._decode_stream(buf), [("cli_stdout", "hello")])

    def test_println_appends_newline(self):
        host, buf = self._host_with_buf()
        host.println("hello", "world")
        self.assertEqual(self._decode_stream(buf), [("cli_stdout", "hello world\n")])

    def test_eprintln_emits_cli_stderr(self):
        host, buf = self._host_with_buf()
        host.eprintln("warning")
        self.assertEqual(self._decode_stream(buf), [("cli_stderr", "warning\n")])

    def test_print_empty_skipped(self):
        host, buf = self._host_with_buf()
        host.print()  # no args → nothing emitted
        self.assertEqual(self._decode_stream(buf), [])

    def test_readline_returns_queued(self):
        host, _ = self._host_with_buf()
        host._push_stdin("first\n")
        host._push_stdin("second\n")
        self.assertEqual(host.readline(), "first\n")
        self.assertEqual(host.readline(), "second\n")

    def test_readline_eof_returns_none(self):
        host, _ = self._host_with_buf()
        host._push_stdin(None)  # signal eof
        self.assertIsNone(host.readline())
        # Repeated calls after EOF still return None.
        self.assertIsNone(host.readline())

    def test_readline_blocks_until_pushed(self):
        host, _ = self._host_with_buf()
        result = []

        def reader():
            result.append(host.readline())

        t = threading.Thread(target=reader, daemon=True)
        t.start()
        time.sleep(0.05)
        self.assertFalse(result, "readline should block until data arrives")
        host._push_stdin("late\n")
        t.join(timeout=1.0)
        self.assertEqual(result, ["late\n"])

    def test_readline_timeout_returns_none(self):
        host, _ = self._host_with_buf()
        # No data, no eof → timeout fires, returns None without blocking forever.
        start = time.monotonic()
        self.assertIsNone(host.readline(timeout=0.05))
        self.assertLess(time.monotonic() - start, 0.5)

    def test_wake_unblocks_readline(self):
        """Host.wake() must wake a pending readline immediately, returning
        None (EOF) — used by cli_signal handlers that want to interrupt a
        long timeout so the verb can exit cleanly."""
        host, _ = self._host_with_buf()
        result: list = []

        def reader():
            result.append(host.readline(timeout=5.0))

        t = threading.Thread(target=reader, daemon=True)
        t.start()
        time.sleep(0.05)
        self.assertFalse(result, "readline should still be blocked")
        start = time.monotonic()
        host.wake()
        t.join(timeout=1.0)
        self.assertLess(time.monotonic() - start, 0.5,
                        "wake() should unblock readline promptly")
        self.assertEqual(result, [None])
        # After wake() further reads keep returning None (EOF state set).
        self.assertIsNone(host.readline(timeout=0.05))

    def test_stdin_lines_iterates_until_eof(self):
        host, _ = self._host_with_buf()
        host._push_stdin("a\n")
        host._push_stdin("b\n")
        host._push_stdin(None)
        self.assertEqual(list(host.stdin_lines()), ["a\n", "b\n"])

    def test_argv_and_tty_flags_default(self):
        host, _ = self._host_with_buf()
        self.assertEqual(host.argv, [])
        self.assertEqual(host.cwd, "")
        self.assertFalse(host.stdin_is_tty)
        self.assertFalse(host.stdout_is_tty)
        self.assertFalse(host.stderr_is_tty)


if __name__ == "__main__":
    unittest.main()
