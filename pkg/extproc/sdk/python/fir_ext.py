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
from typing import Any, Callable, Dict, List, Optional

# ---------------------------------------------------------------------------
# Global registries (populated by decorators, consumed by run())
# ---------------------------------------------------------------------------

_tools: List[Dict[str, Any]] = []
_tool_handlers: Dict[str, Callable] = {}
_hook_handlers: Dict[str, Callable] = {}
_event_handlers: Dict[str, Callable] = {}

# ---------------------------------------------------------------------------
# Decorators
# ---------------------------------------------------------------------------


def tool(
    name: str,
    description: str,
    parameters: Optional[Dict[str, Any]] = None,
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

    def __init__(self, message: str, code: int = -32000):
        super().__init__(message)
        self.code = code


# ---------------------------------------------------------------------------
# JSON-RPC I/O helpers
# ---------------------------------------------------------------------------

_write_lock = threading.Lock()


def _read_message(input_stream=None) -> Optional[Dict[str, Any]]:
    """Read one newline-delimited JSON-RPC message from *input_stream*."""
    stream = input_stream or sys.stdin
    line = stream.readline()
    if not line:
        return None
    return json.loads(line)


def _write_message(msg: Dict[str, Any], output_stream=None) -> None:
    """Write one newline-delimited JSON-RPC message to *output_stream*."""
    stream = output_stream or sys.stdout
    with _write_lock:
        stream.write(json.dumps(msg, separators=(",", ":")) + "\n")
        stream.flush()


def _make_response(id: Any, result: Any) -> Dict[str, Any]:
    return {"jsonrpc": "2.0", "id": id, "result": result}


def _make_error(id: Any, code: int, message: str) -> Dict[str, Any]:
    return {"jsonrpc": "2.0", "id": id, "error": {"code": code, "message": message}}


def _make_request(id: Any, method: str, params: Any = None) -> Dict[str, Any]:
    msg: Dict[str, Any] = {"jsonrpc": "2.0", "id": id, "method": method}
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

    def __init__(self, output_stream=None, pending: Optional[Dict[int, threading.Event]] = None,
                 results: Optional[Dict[int, Any]] = None):
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
            self._pending.pop(rid, None)
            raise TimeoutError(f"Timed out waiting for response to {method}")
        self._pending.pop(rid, None)
        resp = self._results.pop(rid, None)
        if resp and "error" in resp:
            raise RuntimeError(resp["error"].get("message", "unknown error"))
        return resp.get("result") if resp else None

    # -- convenience methods ------------------------------------------------

    def notify(self, message: str, level: str = "info") -> None:
        """Show a notification in fir. *level*: info, warning, error."""
        self._call("notify", {"message": message, "level": level})

    def exec(self, command: str, args: Optional[List[str]] = None, timeout: float = 10.0) -> Dict[str, Any]:
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

    def send_message(self, role: str, content: str) -> None:
        """Inject a message into the session."""
        self._call("send_message", {"role": role, "content": content})

    def set_status(self, text: str) -> None:
        """Set persistent status text in the footer."""
        self._call("set_status", {"status": text})

    def set_session_name(self, name: str) -> None:
        """Set the display name for the session."""
        self._call("set_session_name", {"name": name})

    def set_label(self, entry_id: str, label: str) -> None:
        """Set a label on a session entry."""
        self._call("set_label", {"entry_id": entry_id, "label": label})

    def clear_label(self, entry_id: str) -> None:
        """Clear a label from a session entry."""
        self._call("clear_label", {"entry_id": entry_id})

    def get_active_tools(self) -> List[str]:
        """Return the list of currently active tool names."""
        return self._call("get_active_tools")

    def set_active_tools(self, tools: List[str]) -> None:
        """Set which tools are active."""
        self._call("set_active_tools", {"names": tools})

    def set_model(self, provider: str, model_id: str) -> None:
        """Change the current model."""
        self._call("set_model", {"provider": provider, "id": model_id})


# ---------------------------------------------------------------------------
# Main loop
# ---------------------------------------------------------------------------


def run(
    name: Optional[str] = None,
    input_stream=None,
    output_stream=None,
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
    pending: Dict[int, threading.Event] = {}
    results: Dict[int, Any] = {}

    ctx = Context(output_stream=out, pending=pending, results=results)

    # Worker threads for handlers that may call back into fir
    _workers: List[threading.Thread] = []

    # Collect subscribed events
    subscribed_events: List[str] = list(_event_handlers.keys())
    # Hooks are also events the extension must subscribe to
    for h in _hook_handlers:
        subscribed_events.append(h)

    def _handle_request(method: str, msg_id: Any, params: Dict[str, Any]) -> None:
        """Handle a tool_call or hook in a worker thread so the read loop
        stays free to deliver responses to outbound ``ctx._call()`` requests."""

        # --- tool_call ---
        if method == "tool_call":
            tool_name = params.get("name", "")
            handler = _tool_handlers.get(tool_name)
            if handler is None:
                _write_message(
                    _make_error(msg_id, -32601, f"Unknown tool: {tool_name}"), out
                )
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

    def _dispatch(msg: Dict[str, Any]) -> None:
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
                    "events": subscribed_events,
                },
            )
            _write_message(resp, out)
            return

        # --- tool_call / hooks: run in thread so read loop stays free ---
        if method in ("tool_call",) or method.startswith("hook/"):
            t = threading.Thread(
                target=_handle_request, args=(method, msg_id, params), daemon=True
            )
            t.start()
            _workers.append(t)
            return

        # --- events (async notifications, no id) ---
        if method.startswith("event/"):
            event_name = method[len("event/"):]
            handler = _event_handlers.get(event_name)
            if handler is not None:
                try:
                    handler(params, ctx)
                except Exception:
                    pass  # events are fire-and-forget
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
