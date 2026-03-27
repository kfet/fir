"""
fir_ext — Lightweight Python SDK for fir external process extensions.

Speaks JSON-RPC 2.0 over stdin/stdout so extension authors write simple
handler functions instead of protocol code.  The full protocol reference
lives in ``docs/extension-protocol.md``.

Quick start::

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

See ``pkg/resources/builtin_extensions/demo.py`` for a working example that
exercises every API surface.

-------------------------------------------------------------------------------
WIRE PROTOCOL
-------------------------------------------------------------------------------

Transport
~~~~~~~~~
All messages are JSON-RPC 2.0 objects written as a single line of JSON,
terminated by a newline (``\\n``).  No Content-Length framing.  The extension
process communicates with fir over its **stdin** (fir → ext) and **stdout**
(ext → fir).  **stderr** is captured by fir and forwarded to its structured
log at INFO level.

Message types
~~~~~~~~~~~~~
Three JSON-RPC message types are used:

* **Request** - has ``id``, ``method``, and optional ``params``.  Requires a
  **Response** with the matching ``id``.
* **Response** - has ``id`` and either ``result`` or ``error``.
* **Notification** - has ``method`` and optional ``params``, but *no* ``id``.
  No response is expected.

-------------------------------------------------------------------------------
INIT HANDSHAKE
-------------------------------------------------------------------------------

Immediately after spawning the extension process, fir sends an ``init``
request.  The extension **must** respond within **5 seconds**.

fir → extension::

    {"jsonrpc":"2.0","id":1,"method":"init","params":{"version":"1","cwd":"/project"}}

Params:

* ``version`` - protocol version string (currently ``"1"``).
* ``cwd`` - the project's working directory.

extension → fir::

    {
      "jsonrpc": "2.0",
      "id": 1,
      "result": {
        "name": "my-ext",
        "tools": [
          {
            "name": "count_words",
            "description": "Count words in a string",
            "parameters": {
              "type": "object",
              "properties": {"text": {"type": "string"}},
              "required": ["text"]
            },
            "display_hint": {
              "title_args": [{"name": "text", "style": "accent"}],
              "result_max_lines": 10,
              "use_box": false
            }
          }
        ],
        "commands": [
          {"name": "my-cmd", "description": "Do something useful"}
        ],
        "events": ["session_start", "hook/tool_call"]
      }
    }

Result fields:

* ``name`` (str) - display name for the extension.
* ``tools`` (list) - tool definitions to register (see Tool Definitions below).
* ``commands`` (list) - slash-command definitions ``{name, description}``.
  Omit or send ``[]`` if the extension registers no commands.
* ``events`` (list of str) - event and hook names to subscribe to.  Event
  names are bare (e.g. ``"session_start"``); hook names carry the ``hook/``
  prefix (e.g. ``"hook/tool_call"``).  Omit or send ``[]`` to receive nothing.

Tool definition fields:

* ``name`` (str, required) - unique tool name.
* ``description`` (str) - shown to the LLM.
* ``parameters`` (dict) - JSON Schema ``object`` describing the tool inputs.
* ``display_hint`` (dict, optional) - TUI rendering hints:

  * ``title_args`` - list of ``{name, style, label}`` dicts controlling which
    parameters appear on the header line.  ``style`` may be ``"path"``,
    ``"pattern"``, ``"accent"``, or ``""`` (plain).
  * ``result_max_lines`` - default collapsed line count (default 10).
  * ``use_box`` - render output in a bordered box like the built-in ``bash``
    tool (default false).

-------------------------------------------------------------------------------
TOOL CALLS  (fir → extension, Request)
-------------------------------------------------------------------------------

When the AI invokes a tool that was registered by the extension during init,
fir sends a ``tool_call`` **request** (timeout: **30 seconds**)::

    {
      "jsonrpc": "2.0",
      "id": 2,
      "method": "tool_call",
      "params": {
        "tool_call_id": "toolu_abc123",
        "name": "count_words",
        "params": {"text": "hello world"}
      }
    }

The extension **must** send back a response::

    {
      "jsonrpc": "2.0",
      "id": 2,
      "result": {
        "content": [{"type": "text", "text": "2 words"}],
        "is_error": false
      }
    }

Result fields:

* ``content`` (list) - one or more content blocks.  Each block is
  ``{"type": "text", "text": "..."}`` (only ``text`` type is used today).
  Plain strings and non-structured JSON are automatically wrapped by fir.
* ``is_error`` (bool) - when ``true`` the tool result is reported to the LLM
  as a tool error.

To return a structured error use a JSON-RPC error response::

    {"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"division by zero"}}

-------------------------------------------------------------------------------
HOOKS  (fir → extension, Request)
-------------------------------------------------------------------------------

Hooks are interceptor points.  fir sends them as **requests** (they have an
``id`` and require a response).  Hook method names begin with ``hook/``.

hook/tool_call
..............
Fired before **any** tool call (including built-in tools).  The extension can
block execution by returning ``{"block": true, "reason": "..."}``; returning
``null`` or ``{}`` allows the call to proceed.  Timeout: **5 seconds**. ::

    {
      "jsonrpc": "2.0",
      "id": 3,
      "method": "hook/tool_call",
      "params": {
        "tool_call_id": "toolu_abc123",
        "tool_name": "bash",
        "params": {"command": "rm -rf /"}
      }
    }

Allow response::

    {"jsonrpc":"2.0","id":3,"result":null}

Block response::

    {"jsonrpc":"2.0","id":3,"result":{"block":true,"reason":"dangerous command"}}

hook/command
............
Fired when a user types a slash command registered by the extension.
Timeout: **10 seconds**. ::

    {
      "jsonrpc": "2.0",
      "id": 4,
      "method": "hook/command",
      "params": {"name": "my-cmd", "args": ["arg1", "arg2"]}
    }

Response (optional ``message`` shown in the TUI)::

    {"jsonrpc":"2.0","id":4,"result":{"message":"Done!"}}

-------------------------------------------------------------------------------
EVENTS  (fir → extension, Notification)
-------------------------------------------------------------------------------

Events are JSON-RPC **notifications** — they have no ``id`` and no response is
expected.  The method name is ``event/<event_name>``.  Subscribe by listing the
bare event name (without the ``event/`` prefix) in the ``events`` array of the
init response.

+-------------------------+------------------------------------------------+
| Event                   | ``params`` shape                               |
+=========================+================================================+
| ``session_start``       | ``{"session_data": {"key": "value", ...}}``    |
|                         | ``session_data`` contains values previously    |
|                         | stored via ``set_session_data``, seeded from   |
|                         | the reexec sidecar on ``/reexec``.             |
|                         | ``params`` key absent on a fresh session.      |
+-------------------------+------------------------------------------------+
| ``session_shutdown``    | *(params key absent)*                          |
+-------------------------+------------------------------------------------+
| ``session_named``       | ``{"name": "session-name"}``                   |
|                         | Fired when the session gets a display name     |
|                         | (on start if a name already exists, or when    |
|                         | it is set later).                              |
+-------------------------+------------------------------------------------+
| ``session_update``      | ``{"type": "session_named"|"plan_update",      |
|                         |   "session_name": "...",                       |
|                         |   "plan": {"total":N, "completed":N,           |
|                         |             "metadata":{...}}}``               |
|                         | Generic session-state change event.            |
+-------------------------+------------------------------------------------+
| ``agent_start``         | *(params key absent)* — LLM turn starting      |
+-------------------------+------------------------------------------------+
| ``agent_end``           | *(params key absent)* — LLM turn finished      |
+-------------------------+------------------------------------------------+
| ``turn_start``          | *(params key absent)* — streaming turn starting|
+-------------------------+------------------------------------------------+
| ``turn_end``            | *(params key absent)* — streaming turn finished|
+-------------------------+------------------------------------------------+
| ``message_start``       | *(params key absent)* — LLM message block start|
+-------------------------+------------------------------------------------+
| ``message_end``         | *(params key absent)* — LLM message block end  |
+-------------------------+------------------------------------------------+
| ``tool_execution_start``| ``{"tool_call_id": "...", "tool_name": "..."}``|
+-------------------------+------------------------------------------------+
| ``tool_execution_end``  | ``{"tool_call_id": "...", "tool_name": "...",  |
|                         |   "is_error": false}``                         |
+-------------------------+------------------------------------------------+

-------------------------------------------------------------------------------
EXTENSION → FIR CALLS  (extension → fir, Request)
-------------------------------------------------------------------------------

Extensions may call back into fir at any time by writing a JSON-RPC **request**
to stdout.  fir sends the response on the extension's stdin.  All responses
include either ``result`` or ``error``.

The default client-side timeout (used by this SDK) is **10 seconds** unless
otherwise noted.

+------------------+---------------------------------------+---------------------------+
| Method           | Params                                | Result                    |
+==================+=======================================+===========================+
| ``notify``       | ``{message, level}``                  | ``{ok: true}``            |
|                  | ``level``: ``"info"``,                |                           |
|                  | ``"warning"``, ``"error"``            |                           |
+------------------+---------------------------------------+---------------------------+
| ``exec``         | ``{command, args}``                   | ``{stdout, stderr,        |
|                  |                                       | exit_code}``              |
+------------------+---------------------------------------+---------------------------+
| ``send_message`` | ``{custom_type, content, display,     | ``{ok: true}``            |
|                  |   deliver_as?, trigger_turn?}``       |                           |
|                  | ``deliver_as``: ``"steer"`` or        |                           |
|                  | ``"followUp"`` (omit = append only)   |                           |
|                  | ``trigger_turn``: bool, starts new    |                           |
|                  | agent turn after injecting.           |                           |
+------------------+---------------------------------------+---------------------------+
| ``send_user_     | ``{content, deliver_as?}``            | ``{ok: true}``            |
| message``        | ``deliver_as``: ``"steer"`` or        |                           |
|                  | ``"followUp"`` (omit = prompt)        |                           |
+------------------+---------------------------------------+---------------------------+
| ``set_session_   | ``{name}``                            | ``{ok: true}``            |
| name``           |                                       |                           |
+------------------+---------------------------------------+---------------------------+
| ``set_label``    | ``{entry_id, label}``                 | ``{ok: true}``            |
+------------------+---------------------------------------+---------------------------+
| ``clear_label``  | ``{entry_id}``                        | ``{ok: true}``            |
+------------------+---------------------------------------+---------------------------+
| ``get_active_    | *(none / empty object)*               | ``["tool1", "tool2", …]`` |
| tools``          |                                       |                           |
+------------------+---------------------------------------+---------------------------+
| ``set_active_    | ``{names}``                           | ``{ok: true}``            |
| tools``          |                                       |                           |
+------------------+---------------------------------------+---------------------------+
| ``set_model``    | ``{provider, id}``                    | ``{ok: bool}``            |
|                  |                                       | ``false`` if provider has |
|                  |                                       | no API key configured.    |
+------------------+---------------------------------------+---------------------------+
| ``set_status``   | ``{status}``                          | ``{ok: true}``            |
|                  | Empty string clears the status.       |                           |
+------------------+---------------------------------------+---------------------------+
| ``continue_      | *(none)*                              | ``{ok: true}``            |
| session``        | Triggers a new agent turn.            | SDK timeout: 60 s         |
+------------------+---------------------------------------+---------------------------+
| ``side_query``   | ``{question}``                        | ``{ok: true,              |
|                  | One-shot LLM call, no history.        | text: "..."}``            |
|                  |                                       | SDK timeout: 120 s        |
+------------------+---------------------------------------+---------------------------+
| ``set_session_   | ``{key, value}``                      | ``{ok: true}``            |
| data``           | Persist string K/V; survives          |                           |
|                  | ``/reexec`` via sidecar.              |                           |
+------------------+---------------------------------------+---------------------------+
| ``get_session_   | ``{key}``                             | ``{value: "...",          |
| data``           |                                       | ok: bool}``               |
+------------------+---------------------------------------+---------------------------+
| ``call_tool``    | ``{name, params}``                    | ``{content: [...],        |
|                  | Calls any registered tool directly,   | is_error: bool}``         |
|                  | bypassing conversation history.       | SDK timeout: 60 s         |
+------------------+---------------------------------------+---------------------------+
| ``list_tools``   | ``{}``                                | ``[{name,                 |
|                  |                                       | description?,             |
|                  |                                       | parameters?}, …]``        |
+------------------+---------------------------------------+---------------------------+
| ``prepend_       | ``{content}``                         | ``{ok: true}``            |
| context``        | Adds a ``[SYS_EXT]`` block to the     |                           |
|                  | system prompt (dynamic context).      |                           |
+------------------+---------------------------------------+---------------------------+

-------------------------------------------------------------------------------
COMMENT FRONTMATTER
-------------------------------------------------------------------------------

Extension files may include a comment frontmatter block (after an optional
shebang line) to declare metadata visible to fir before the process is
started::

    #!/usr/bin/env python3
    # ---
    # name: my-ext                        # overrides filename-derived name
    # events: session_start, hook/tool_call
    # commands: my-cmd: Brief description
    # modes: tui, acp                     # restrict to specific fir modes
    # demo: true                          # mark as demo; not loaded by default
    # ---

fir checks the frontmatter against the actual init-handshake result and warns
(or auto-fixes when the user consents) if they diverge.  The ``events`` and
``commands`` keys must stay in sync with what ``fir_ext.run()`` actually
registers.

Supported ``modes`` values: ``tui`` (alias ``interactive``), ``text``,
``json``, ``rpc``, ``acp``.  Omitting the key runs the extension in all modes.

-------------------------------------------------------------------------------
DISCOVERY & TRUST
-------------------------------------------------------------------------------

fir searches for extensions in order of decreasing precedence:

1. ``.fir/extensions/`` — project-local (requires explicit trust per file hash)
2. ``~/.config/fir/extensions/`` — user-global (always trusted)
3. Extra directories/files supplied by installed fir packages (package scope)

Any executable file in these directories is a candidate.  The extension name
is the filename without its suffix (``wordcount.py`` → ``wordcount``).  A
project-local extension with the same name as a global one shadows the global
one.

Trust records are stored in ``~/.config/fir/trusted-extensions.json`` keyed by
``<projectDir>:<extensionName>``, each entry holding the approved SHA-256 hash.
If the file changes after approval, fir re-prompts.

-------------------------------------------------------------------------------
PROCESS LIFECYCLE
-------------------------------------------------------------------------------

* fir spawns one process per extension, passing it the project env plus any
  SDK path variables (``PYTHONPATH`` for Python).
* On crash, fir restarts the extension with **exponential backoff**:
  1 s → 2 s → 4 s → … capped at 30 s.
* After **5 consecutive failures** fir gives up and logs an error.
* On clean shutdown, fir sends **SIGTERM**, waits up to 2 seconds, then
  **SIGKILL**.
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

# Auth provider registries
_auth_providers: list[dict[str, Any]] = []
_auth_login_handlers: dict[str, Callable] = {}
_auth_refresh_handlers: dict[str, Callable] = {}
_auth_api_key_handlers: dict[str, Callable] = {}
_auth_list_models_handlers: dict[str, Callable] = {}
_auth_modify_models_handlers: dict[str, Callable] = {}

# ---------------------------------------------------------------------------
# Decorators
# ---------------------------------------------------------------------------


def tool(
    name: str,
    description: str,
    parameters: dict[str, Any] | None = None,
    display_hint: dict[str, Any] | None = None,
) -> Callable:
    """Register a tool that fir can invoke via ``tool_call``.

    The decorated function receives ``(params: dict, ctx: Context)`` and
    should return a JSON-serialisable result (or raise ``ToolError``).

    *display_hint* tells the TUI how to render the tool execution.  Keys:

    - ``title_args``: list of ``{"name": ..., "style": ..., "label": ...}``
      dicts controlling which args appear on the header line.  *style* can
      be ``"path"``, ``"pattern"``, ``"accent"``, or ``""`` (plain).
    - ``result_max_lines``: default collapsed line count (default 10).
    - ``use_box``: render output in a bordered box like ``bash``.
    """

    def decorator(fn: Callable) -> Callable:
        spec: dict[str, Any] = {
            "name": name,
            "description": description,
            "parameters": parameters or {"type": "object", "properties": {}},
        }
        if display_hint is not None:
            spec["display_hint"] = display_hint
        _tools.append(spec)
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


def auth_provider(
    provider_id: str,
    name: str,
    uses_callback_server: bool = True,
) -> Callable:
    """Register an OAuth auth provider login handler.

    The decorated function receives ``(params: dict, ctx: AuthContext)``
    where *params* contains ``{"provider_id": "..."}`` and *ctx* provides
    OAuth helper methods. It should return a dict with ``access``, ``refresh``,
    and ``expires`` keys.

    Example::

        @fir_ext.auth_provider(id="my-corp", name="My Corp SSO")
        def login(params, ctx):
            pkce = ctx.generate_pkce()
            server = ctx.start_callback_server()
            ctx.open_url(build_auth_url(pkce, server))
            result = ctx.await_callback()
            tokens = exchange_code(result["code"], pkce["verifier"])
            return {
                "access": tokens["access_token"],
                "refresh": tokens["refresh_token"],
                "expires": int(time.time() * 1000) + tokens["expires_in"] * 1000,
            }
    """

    def decorator(fn: Callable) -> Callable:
        _auth_providers.append(
            {
                "id": provider_id,
                "name": name,
                "uses_callback_server": uses_callback_server,
            }
        )
        _auth_login_handlers[provider_id] = fn
        return fn

    return decorator


def auth_refresh(provider: str) -> Callable:
    """Register a token refresh handler for an auth provider.

    The decorated function receives ``(params: dict, ctx: AuthContext)``
    where *params* contains ``{"provider_id": "...", "credentials": {...}}``.
    It should return a dict with ``access``, ``refresh``, and ``expires`` keys.
    """

    def decorator(fn: Callable) -> Callable:
        _auth_refresh_handlers[provider] = fn
        return fn

    return decorator


def auth_api_key(provider: str) -> Callable:
    """Register a custom API key extractor for an auth provider.

    The decorated function receives ``(params: dict, ctx: AuthContext)``
    where *params* contains ``{"provider_id": "...", "credentials": {...}}``.
    It should return a string (the API key).

    If not registered, the default behavior returns ``credentials["access"]``.
    """

    def decorator(fn: Callable) -> Callable:
        _auth_api_key_handlers[provider] = fn
        return fn

    return decorator


def auth_modify_models(provider: str) -> Callable:
    """Register a model modifier for an auth provider.

    The decorated function receives ``(params: dict, ctx: AuthContext)``
    where *params* contains ``{"provider_id": "...", "credentials": {...}, "models": [...]}``.
    It should return a list of modified models, or None for no changes.
    """

    def decorator(fn: Callable) -> Callable:
        _auth_modify_models_handlers[provider] = fn
        return fn

    return decorator


def auth_list_models(provider: str) -> Callable:
    """Register a model lister for an auth provider.

    The decorated function receives ``(params: dict, ctx: AuthContext)``
    where *params* contains ``{"provider_id": "...", "credentials": {...}}``.
    It should return a list of model ID strings, or None if not supported.
    """

    def decorator(fn: Callable) -> Callable:
        _auth_list_models_handlers[provider] = fn
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

    def side_query(self, question: str, timeout: float = 120.0) -> str:
        """Ask a side question using the current session context.

        Makes a one-shot LLM call with no tools and no history persistence.
        Returns the full response text. Blocks until the response is complete.
        """
        result = self._call("side_query", {"question": question}, timeout=timeout)
        if isinstance(result, dict):
            return result.get("text", "")
        return ""

    def report_progress(self, message: str) -> None:
        """Send a transient progress message to the UI.

        Updates the spinner text inside the tool's display component
        (e.g. "Calling Read..." or "Synthesizing..."). Fire-and-forget —
        does not wait for a response.
        """
        _write_message(
            {"jsonrpc": "2.0", "method": "report_progress", "params": {"message": message}},
            self._out,
        )

    def call_tool(
        self,
        name: str,
        params: dict[str, Any] | None = None,
        timeout: float = 60.0,
    ) -> dict[str, Any]:
        """Call a registered tool by name and return its result.

        Executes the tool directly via the bridge — the call does not appear
        in the agent's conversation history.

        Parameters
        ----------
        name : str
            Name of the tool to call (built-in, extension, or MCP).
        params : dict, optional
            Parameters to pass to the tool.
        timeout : float, optional
            How long to wait for the tool to finish.

        Returns
        -------
        dict
            Tool result with ``content`` (list of content blocks) and
            ``is_error`` (bool).  On RPC-level errors a dict with
            ``is_error=True`` and a text content block is returned.
        """
        result = self._call(
            "call_tool",
            {"name": name, "params": params or {}},
            timeout=timeout,
        )
        if isinstance(result, dict):
            return result
        return {"content": [{"text": str(result)}], "is_error": False}

    def list_tools(self, timeout: float = 10.0) -> list[dict[str, Any]]:
        """Return info about all registered tools.

        Returns
        -------
        list of dict
            Each dict has ``name`` (str), ``description`` (str, optional),
            and ``parameters`` (dict, optional — JSON Schema).
        """
        result = self._call("list_tools", {}, timeout=timeout)
        if isinstance(result, list):
            return result
        return []

    def prepend(self, content: str) -> None:
        """Add a [SYS_EXT] block to the system prompt.

        Extensions use this to inject dynamic context that the LLM treats
        as an authoritative extension of the system prompt. The content is
        wrapped in ``[SYS_EXT]`` tags automatically by the runtime.

        Parameters
        ----------
        content : str
            The context to prepend (e.g. project info, user preferences).
        """
        self._call("prepend_context", {"content": content})


class AuthContext(Context):
    """Extended context for auth provider handlers with OAuth helper methods.

    Inherits all standard Context methods and adds auth-specific helpers
    that call back into fir's OAuth infrastructure.
    """

    def generate_pkce(self, timeout: float = 10.0) -> dict[str, str]:
        """Generate a PKCE code verifier and challenge.

        Returns
        -------
        dict
            ``{"verifier": "...", "challenge": "..."}``
        """
        return self._call("auth/generate_pkce", {}, timeout=timeout)

    def start_callback_server(
        self,
        addr: str = "127.0.0.1:0",
        path: str = "/callback",
        state: str = "",
        timeout: float = 10.0,
    ) -> dict[str, str]:
        """Start a local HTTP server to receive the OAuth callback.

        Parameters
        ----------
        addr : str
            Address to bind (use port 0 for auto-assign).
        path : str
            URL path for the callback endpoint.
        state : str
            Expected OAuth state parameter. If provided, requests with a
            mismatched state receive a 400 error instead of being forwarded.

        Returns
        -------
        dict
            ``{"addr": "127.0.0.1:NNNNN", "redirect_uri": "http://localhost:NNNNN/callback"}``
        """
        return self._call(
            "auth/start_callback_server",
            {"addr": addr, "path": path, "state": state},
            timeout=timeout,
        )

    def await_callback(self, timeout: float = 300.0) -> dict[str, str]:
        """Block until the callback server receives an auth code.

        Returns
        -------
        dict
            ``{"code": "...", "state": "..."}``
        """
        return self._call("auth/await_callback", {}, timeout=timeout)

    def stop_callback_server(self, timeout: float = 10.0) -> None:
        """Stop the local callback server."""
        self._call("auth/stop_callback_server", {}, timeout=timeout)

    def open_url(self, url: str, instructions: str = "") -> None:
        """Ask fir to open a URL in the user's browser and/or display it."""
        self._call("auth/open_url", {"url": url, "instructions": instructions})

    def progress(self, message: str) -> None:
        """Show a progress/status message in fir's UI."""
        self._call("auth/progress", {"message": message})

    def prompt(
        self, message: str, placeholder: str = "", allow_empty: bool = False, timeout: float = 300.0
    ) -> str:
        """Ask the user for text input via fir's TUI.

        Returns the entered value.
        """
        result = self._call(
            "auth/prompt",
            {"message": message, "placeholder": placeholder, "allow_empty": allow_empty},
            timeout=timeout,
        )
        if isinstance(result, dict):
            return result.get("value", "")
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

    # Auth context for auth handlers
    auth_ctx = AuthContext(output_stream=out, pending=pending, results=results)

    def _handle_auth_request(
        method: str, msg_id: Any, params: dict[str, Any], out_stream: WriteStream
    ) -> None:
        """Handle auth/* RPC methods from fir."""
        provider_id = params.get("provider_id", "")

        try:
            if method == "auth/login":
                handler = _auth_login_handlers.get(provider_id)
                if handler is None:
                    _write_message(
                        _make_error(
                            msg_id, -32601, f"No login handler for provider: {provider_id}"
                        ),
                        out_stream,
                    )
                    return
                result = handler(params, auth_ctx)
                if isinstance(result, dict) and "access" in result:
                    result = {"credentials": result}
                _write_message(_make_response(msg_id, result), out_stream)

            elif method == "auth/refresh":
                handler = _auth_refresh_handlers.get(provider_id)
                if handler is None:
                    _write_message(
                        _make_error(
                            msg_id, -32601, f"No refresh handler for provider: {provider_id}"
                        ),
                        out_stream,
                    )
                    return
                result = handler(params, auth_ctx)
                if isinstance(result, dict) and "access" in result:
                    result = {"credentials": result}
                _write_message(_make_response(msg_id, result), out_stream)

            elif method == "auth/api_key":
                handler = _auth_api_key_handlers.get(provider_id)
                if handler is None:
                    # Default: return access token
                    creds = params.get("credentials", {})
                    _write_message(
                        _make_response(msg_id, {"api_key": creds.get("access", "")}), out_stream
                    )
                    return
                result = handler(params, auth_ctx)
                if isinstance(result, str):
                    result = {"api_key": result}
                _write_message(_make_response(msg_id, result), out_stream)

            elif method == "auth/modify_models":
                handler = _auth_modify_models_handlers.get(provider_id)
                if handler is None:
                    _write_message(_make_response(msg_id, {"models": None}), out_stream)
                    return
                result = handler(params, auth_ctx)
                if isinstance(result, list):
                    result = {"models": result}
                _write_message(_make_response(msg_id, result), out_stream)

            elif method == "auth/list_models":
                handler = _auth_list_models_handlers.get(provider_id)
                if handler is None:
                    _write_message(_make_response(msg_id, {"models": None}), out_stream)
                    return
                result = handler(params, auth_ctx)
                if isinstance(result, list):
                    result = {"models": result}
                _write_message(_make_response(msg_id, result), out_stream)

            else:
                _write_message(
                    _make_error(msg_id, -32601, f"Unknown auth method: {method}"), out_stream
                )

        except Exception as exc:
            _write_message(_make_error(msg_id, -32000, str(exc)), out_stream)

    def _dispatch(msg: dict[str, Any]) -> None:
        method = msg.get("method", "")
        msg_id = msg.get("id")
        params = msg.get("params", {})

        # --- init handshake (always synchronous) ---
        if method == "init":
            init_result: dict[str, Any] = {
                "name": ext_name,
                "tools": list(_tools),
                "commands": list(_commands),
                "events": subscribed_events,
            }
            if _auth_providers:
                init_result["auth_providers"] = list(_auth_providers)
            resp = _make_response(msg_id, init_result)
            _write_message(resp, out)
            return

        # --- tool_call / hooks: run in thread so read loop stays free ---
        if method in ("tool_call",) or method.startswith("hook/"):
            t = threading.Thread(target=_handle_request, args=(method, msg_id, params), daemon=True)
            t.start()
            _track_worker(t)
            return

        # --- auth/* RPCs: run in thread ---
        if method.startswith("auth/"):
            t = threading.Thread(
                target=_handle_auth_request, args=(method, msg_id, params, out), daemon=True
            )
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
                    except RuntimeError as exc:
                        if "shutdown" not in str(exc):
                            import traceback
                            traceback.print_exc(file=sys.stderr)
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

    # Wait for any in-flight handlers to finish.
    # First, unblock any pending outbound calls so handler threads don't wait
    # the full timeout after the connection is closed.
    for rid, evt in list(pending.items()):
        results[rid] = {
            "jsonrpc": "2.0", "id": rid,
            "error": {"code": -32000, "message": "shutdown"},
        }
        evt.set()
    for w in _workers:
        w.join(timeout=15)
