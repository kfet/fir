"""
fir_ext — Lightweight Python SDK for fir external process extensions.

Speaks JSON-RPC 2.0 over stdin/stdout so extension authors write simple
handler functions instead of protocol code.

Usage example::

    import fir_ext

    @fir_ext.tool(
        name="greet",
        description="Greet someone by name",
        parameters={
            "type": "object",
            "properties": {"name": {"type": "string"}},
            "required": ["name"],
        },
    )
    def greet(params, ctx):
        ctx.notify(f"Greeting {params['name']}!", level="info")
        return {"message": f"Hello, {params['name']}!"}

    @fir_ext.on("session_start")
    def on_start(params, ctx):
        ctx.set_status("extension ready")

    fir_ext.run()

Protocol details live in docs/plan/ext-process.md §2.
"""

from __future__ import annotations

import json
import sys
import threading
from typing import TYPE_CHECKING, Any, Protocol

if TYPE_CHECKING:
    from collections.abc import Callable


class ReadStream(Protocol):
    """Minimal interface for an input stream (e.g. sys.stdin)."""

    def readline(self) -> str: ...


class WriteStream(Protocol):
    """Minimal interface for an output stream (e.g. sys.stdout)."""

    def write(self, s: str, /) -> int: ...
    def flush(self) -> None: ...


# ---------------------------------------------------------------------------
# Global registries (populated by decorators, consumed by run())
# ---------------------------------------------------------------------------

_tools: list[dict[str, Any]] = []
_tool_handlers: dict[str, Callable] = {}
_hook_handlers: dict[str, Callable] = {}
_event_handlers: dict[str, Callable] = {}
_commands: list[dict[str, Any]] = []
_command_handlers: dict[str, Callable] = {}

# ---------------------------------------------------------------------------
# Decorators
# ---------------------------------------------------------------------------


def tool(
    name: str,
    description: str,
    parameters: dict[str, Any] | None = None,
) -> Callable:
    """Register a tool that fir can invoke via ``tool_call``.

    The decorated function receives ``(params: dict, ctx: Context)`` and
    should return a JSON-serialisable result (or raise ``ToolError``).
    """

    def decorator(fn: Callable) -> Callable:
        _tools.append(
            {
                "name": name,
                "description": description,
                "parameters": parameters or {"type": "object", "properties": {}},
            }
        )
        _tool_handlers[name] = fn
        return fn

    return decorator


def command(name: str, description: str = "") -> Callable:
    """Register a slash command (``/name``) that users can type in the TUI.

    The decorated function receives ``(args: list[str], ctx: Context)`` and
    may return a dict with an optional ``"message"`` key shown in the TUI,
    or ``None`` to show nothing.

    Example::

        @fir_ext.command(name="greet", description="Greet the user")
        def cmd_greet(args, ctx):
            return {"message": f"Hello, {args[0] if args else 'world'}!"}
    """

    def decorator(fn: Callable) -> Callable:
        _commands.append({"name": name, "description": description})
        _command_handlers[name] = fn
        return fn

    return decorator


def on(event_name: str) -> Callable:
    """Register a handler for an event or hook.

    Hook names start with ``hook/`` (e.g. ``hook/tool_call``).
    Event names are bare (e.g. ``session_start``).

    Hook handlers receive ``(params, ctx)`` and may return a value.
    Event handlers receive ``(params, ctx)``; return value is ignored.
    """

    def decorator(fn: Callable) -> Callable:
        if event_name.startswith("hook/"):
            _hook_handlers[event_name] = fn
        else:
            _event_handlers[event_name] = fn
        return fn

    return decorator


# ---------------------------------------------------------------------------
# ToolError
# ---------------------------------------------------------------------------


class ToolError(Exception):
    """Raise inside a tool handler to return a structured error to fir."""

    code: int

    def __init__(self, message: str, code: int = -32000):
        super().__init__(message)
        self.code = code


# ---------------------------------------------------------------------------
# JSON-RPC I/O helpers
# ---------------------------------------------------------------------------

_write_lock = threading.Lock()


def _read_message(input_stream: ReadStream | None = None) -> dict[str, Any] | None:
    """Read one newline-delimited JSON-RPC message from *input_stream*."""
    stream = input_stream or sys.stdin
    line = stream.readline()
    if not line:
        return None
    return json.loads(line)


def _write_message(msg: dict[str, Any], output_stream: WriteStream | None = None) -> None:
    """Write one newline-delimited JSON-RPC message to *output_stream*."""
    stream = output_stream or sys.stdout
    with _write_lock:
        stream.write(json.dumps(msg, separators=(",", ":")) + "\n")
        stream.flush()


def _make_response(msg_id: Any, result: Any) -> dict[str, Any]:
    return {"jsonrpc": "2.0", "id": msg_id, "result": result}


def _make_error(msg_id: Any, code: int, message: str) -> dict[str, Any]:
    return {"jsonrpc": "2.0", "id": msg_id, "error": {"code": code, "message": message}}


def _make_request(msg_id: Any, method: str, params: Any = None) -> dict[str, Any]:
    msg: dict[str, Any] = {"jsonrpc": "2.0", "id": msg_id, "method": method}
    if params is not None:
        msg["params"] = params
    return msg


# ---------------------------------------------------------------------------
# Context — outbound calls from extension → fir
# ---------------------------------------------------------------------------

_next_id_lock = threading.Lock()
_next_id = 1000


def _alloc_id() -> int:
    global _next_id
    with _next_id_lock:
        _next_id += 1
        return _next_id


class Context:
    """Provides outbound RPC helpers for extension → fir communication.

    Passed as the second argument to every handler.
    """

    _out: WriteStream | None
    _pending: dict[int, threading.Event]
    _results: dict[int, Any]

    def __init__(
        self,
        output_stream: WriteStream | None = None,
        pending: dict[int, threading.Event] | None = None,
        results: dict[int, Any] | None = None,
    ):
        self._out = output_stream
        self._pending = pending if pending is not None else {}
        self._results = results if results is not None else {}

    def _call(self, method: str, params: Any = None, timeout: float = 10.0) -> Any:
        """Send a JSON-RPC request to fir and wait for the response."""
        rid = _alloc_id()
        event = threading.Event()
        self._pending[rid] = event
        _write_message(_make_request(rid, method, params), self._out)
        if not event.wait(timeout):
            _ = self._pending.pop(rid, None)
            raise TimeoutError(f"Timed out waiting for response to {method}")
        _ = self._pending.pop(rid, None)
        resp = self._results.pop(rid, None)
        if resp and "error" in resp:
            raise RuntimeError(resp["error"].get("message", "unknown error"))
        return resp.get("result") if resp else None

    # -- convenience methods ------------------------------------------------

    def notify(self, message: str, level: str = "info") -> None:
        """Show a notification in fir. *level*: info, warning, error."""
        self._call("notify", {"message": message, "level": level})

    def exec(
        self, command: str, args: list[str] | None = None, timeout: float = 10.0
    ) -> dict[str, Any]:
        """Run a command via fir. Returns dict with stdout, stderr, exit_code.

        Parameters
        ----------
        command : str
            The command to execute.
        args : list of str, optional
            Arguments to pass to the command.
        timeout : float, optional
            How long to wait for the RPC response (client-side only, not sent to Go).
        """
        return self._call("exec", {"command": command, "args": args or []}, timeout=timeout)

    def send_message(
        self,
        custom_type: str,
        content: Any,
        *,
        display: bool = False,
        deliver_as: str | None = None,
        trigger_turn: bool = False,
    ) -> None:
        """Inject a custom message into the session.

        Parameters
        ----------
        custom_type : str
            Arbitrary type tag consumed by the session renderer.
        content : any JSON-serialisable value
            Payload attached to the message.
        display : bool, optional
            When True the message is shown in the UI.
        deliver_as : str, optional
            How to deliver the message to the agent. One of ``"steer"`` or
            ``"followUp"``. When omitted the message is only appended to the
            session log.
        trigger_turn : bool, optional
            When True fir will start a new agent turn after injecting the
            message.
        """
        params: dict[str, Any] = {
            "custom_type": custom_type,
            "content": content,
            "display": display,
        }
        if deliver_as is not None:
            params["deliver_as"] = deliver_as
        if trigger_turn:
            params["trigger_turn"] = trigger_turn
        self._call("send_message", params)

    def send_user_message(self, content: str, *, deliver_as: str | None = None) -> None:
        """Inject a user-role message into the session.

        Parameters
        ----------
        content : str
            The message text.
        deliver_as : str, optional
            How to deliver the message to the agent. One of ``"steer"`` or
            ``"followUp"``. When omitted the message triggers a new user turn.
        """
        params: dict[str, Any] = {"content": content}
        if deliver_as is not None:
            params["deliver_as"] = deliver_as
        self._call("send_user_message", params)

    def set_status(self, text: str) -> None:
        """Set persistent status text in the footer."""
        self._call("set_status", {"status": text})

    def set_session_name(self, name: str) -> None:
        """Set the display name for the session."""
        self._call("set_session_name", {"name": name})

    def set_session_data(self, key: str, value: str) -> None:
        """Store a key/value pair in this extension's session data store.

        Values are persisted across ``/reexec`` via the reexec sidecar and are
        handed back to the extension in the ``session_start`` event params
        under the ``"session_data"`` key, so state can be restored without an
        explicit ``get_session_data`` call.
        """
        self._call("set_session_data", {"key": key, "value": value})

    def get_session_data(self, key: str) -> str | None:
        """Retrieve a value previously stored with ``set_session_data``.

        Returns the stored string, or ``None`` if the key is absent.
        """
        result = self._call("get_session_data", {"key": key})
        if isinstance(result, dict) and result.get("ok"):
            return result.get("value")
        return None

    def set_label(self, entry_id: str, label: str) -> None:
        """Set a label on a session entry."""
        self._call("set_label", {"entry_id": entry_id, "label": label})

    def clear_label(self, entry_id: str) -> None:
        """Clear a label from a session entry."""
        self._call("clear_label", {"entry_id": entry_id})

    def get_active_tools(self) -> list[str]:
        """Return the list of currently active tool names."""
        return self._call("get_active_tools")

    def set_active_tools(self, tools: list[str]) -> None:
        """Set which tools are active."""
        self._call("set_active_tools", {"names": tools})

    def set_model(self, provider: str, model_id: str) -> bool:
        """Change the current model. Returns True on success."""
        result = self._call("set_model", {"provider": provider, "id": model_id})
        if isinstance(result, dict):
            return bool(result.get("ok"))
        return False

    def continue_session(self) -> None:
        """Trigger the agent to continue without injecting any message."""
        self._call("continue_session", timeout=60.0)

    def btw(self, question: str, timeout: float = 120.0) -> str:
        """Ask a side question using the current session context.

        Makes a one-shot LLM call with no tools and no history persistence.
        Returns the full response text. Blocks until the response is complete.
        """
        result = self._call("side_query", {"question": question}, timeout=timeout)
        if isinstance(result, dict):
            return result.get("text", "")
        return ""


# ---------------------------------------------------------------------------
# Main loop
# ---------------------------------------------------------------------------


def run(
    name: str | None = None,
    input_stream: ReadStream | None = None,
    output_stream: WriteStream | None = None,
) -> None:
    """Start the extension event loop.

    Performs the init handshake, then dispatches incoming tool_call,
    hook, and event messages to the registered handlers.

    Parameters
    ----------
    name : str, optional
        Extension name reported during init. Defaults to ``"python-ext"``.
    input_stream, output_stream : file-like, optional
        Override stdin/stdout (useful for testing).
    """
    ext_name = name or "python-ext"
    inp = input_stream or sys.stdin
    out = output_stream or sys.stdout

    # Pending outbound requests (extension→fir)
    pending: dict[int, threading.Event] = {}
    results: dict[int, Any] = {}

    ctx = Context(output_stream=out, pending=pending, results=results)

    # Worker threads for handlers that may call back into fir
    _workers: list[threading.Thread] = []
    _worker_prune_threshold = 100  # prune completed threads when list exceeds this

    def _track_worker(t: threading.Thread) -> None:
        """Append a worker thread, pruning completed ones when the list grows large."""
        nonlocal _workers
        _workers.append(t)
        if len(_workers) > _worker_prune_threshold:
            _workers = [w for w in _workers if w.is_alive()]

    # Collect subscribed events
    subscribed_events: list[str] = list(_event_handlers.keys()) + list(_hook_handlers.keys())

    def _handle_request(method: str, msg_id: Any, params: dict[str, Any]) -> None:
        """Handle a tool_call or hook in a worker thread so the read loop
        stays free to deliver responses to outbound ``ctx._call()`` requests."""

        # --- tool_call ---
        if method == "tool_call":
            tool_name = params.get("name", "")
            handler = _tool_handlers.get(tool_name)
            if handler is None:
                _write_message(_make_error(msg_id, -32601, f"Unknown tool: {tool_name}"), out)
                return
            try:
                result = handler(params.get("params", {}), ctx)
                # Wrap plain string results into structured format
                if isinstance(result, str):
                    result = {"content": [{"text": result}], "is_error": False}
                _write_message(_make_response(msg_id, result), out)
            except ToolError as exc:
                _write_message(_make_error(msg_id, exc.code, str(exc)), out)
            except Exception as exc:
                _write_message(_make_error(msg_id, -32000, str(exc)), out)
            return

        # --- hooks ---
        if method.startswith("hook/"):
            # hook/command is dispatched to registered command handlers.
            if method == "hook/command":
                cmd_name = params.get("name", "")
                handler = _command_handlers.get(cmd_name)
                if handler is None:
                    _write_message(_make_response(msg_id, None), out)
                    return
                try:
                    result = handler(params.get("args", []), ctx)
                    _write_message(_make_response(msg_id, result), out)
                except Exception as exc:
                    _write_message(_make_error(msg_id, -32000, str(exc)), out)
                return

            handler = _hook_handlers.get(method)
            if handler is None:
                _write_message(_make_response(msg_id, None), out)
                return
            try:
                result = handler(params, ctx)
                _write_message(_make_response(msg_id, result), out)
            except Exception as exc:
                _write_message(_make_error(msg_id, -32000, str(exc)), out)
            return

    def _dispatch(msg: dict[str, Any]) -> None:
        method = msg.get("method", "")
        msg_id = msg.get("id")
        params = msg.get("params", {})

        # --- init handshake (always synchronous) ---
        if method == "init":
            resp = _make_response(
                msg_id,
                {
                    "name": ext_name,
                    "tools": list(_tools),
                    "commands": list(_commands),
                    "events": subscribed_events,
                },
            )
            _write_message(resp, out)
            return

        # --- tool_call / hooks: run in thread so read loop stays free ---
        if method in ("tool_call",) or method.startswith("hook/"):
            t = threading.Thread(target=_handle_request, args=(method, msg_id, params), daemon=True)
            t.start()
            _track_worker(t)
            return

        # --- events (async notifications, no id) ---
        if method.startswith("event/"):
            event_name = method[len("event/") :]
            handler = _event_handlers.get(event_name)
            if handler is not None:
                # Run in a worker thread so the read loop stays free to deliver
                # responses to any ctx.xxx() outbound calls the handler makes.
                def _run_event(h=handler, p=params):
                    try:
                        h(p, ctx)
                    except Exception:
                        import traceback

                        traceback.print_exc(file=sys.stderr)

                t = threading.Thread(target=_run_event, daemon=True)
                t.start()
                _track_worker(t)
            return

        # --- response to an outbound request we made ---
        if msg_id is not None and "method" not in msg:
            results[msg_id] = msg
            event = pending.get(msg_id)
            if event:
                event.set()
            return

        # Unknown method with id → error
        if msg_id is not None:
            _write_message(_make_error(msg_id, -32601, f"Method not found: {method}"), out)

    # Main read loop
    while True:
        msg = _read_message(inp)
        if msg is None:
            break
        _dispatch(msg)

    # Wait for any in-flight handlers to finish
    for w in _workers:
        w.join(timeout=15)
