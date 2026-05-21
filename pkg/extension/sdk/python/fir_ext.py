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

Typed surface
~~~~~~~~~~~~~
Every wire shape has a ``TypedDict`` (e.g. :class:`ToolResult`,
:class:`ExecResult`, :class:`MessageEndParams`, :class:`ToolCallHookParams`)
exported from this module. Annotating handler signatures with these types
gives full IDE/type-checker support without changing runtime behaviour —
all TypedDicts are plain ``dict``\\s under the hood. Existing extensions
that pass dicts around continue to work unchanged.

Example with typed handlers::

    from typing import Optional
    import fir_ext

    @fir_ext.on("message_end")
    def on_message_end(params: fir_ext.MessageEndParams, ctx: fir_ext.Context) -> None:
        if params.get("role") != "assistant":
            return
        usage = params.get("usage", {})
        cost = usage.get("cost", {}).get("total", 0.0)
        ctx.notify(f"turn cost ${cost:.4f}")

    @fir_ext.on("hook/tool_call")
    def on_tool_call(
        params: fir_ext.ToolCallHookParams, ctx: fir_ext.Context
    ) -> Optional[fir_ext.ToolCallHookResult]:
        if (params.get("tool_name") or "").startswith("blocked:"):
            return {"block": True, "reason": "blocked by policy"}
        return None


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
request.  The extension **must** respond within **30 seconds** (the default;
configurable via the ``FIR_EXT_TIMEOUT`` environment variable, in seconds).

fir → extension::

    {"jsonrpc":"2.0","id":1,"method":"init","params":{"version":"1","cwd":"/project","config_dirs":["/project/.fir","~/.config/fir"]}}

Params:

* ``version`` - protocol version string (currently ``"1"``).
* ``cwd`` - the project's working directory.
* ``config_dirs`` - priority-ordered list of directories for per-extension
  config files (highest priority first). Use ``fir_ext.load_config()`` /
  ``fir_ext.config_path()`` instead of reading this directly.

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
| ``message_end``         | ``{role, provider?, model?, usage?}`` — see docs |
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
|                  | Also writes a ``footer`` observable   |                           |
|                  | card under this extension's source.   |                           |
+------------------+---------------------------------------+---------------------------+
| ``put_observable``| ``{key, slug, detail}``              | ``{ok: true}``            |
|                  | Publishes an observable card. Source  |                           |
|                  | + entry_id stamped by the host;       |                           |
|                  | payload Source / EntryID fields are   |                           |
|                  | ignored. Extensions cannot READ       |                           |
|                  | other extensions' cards in v1.        |                           |
+------------------+---------------------------------------+---------------------------+
| ``clear_         | ``{key}``                             | ``{ok: true}``            |
| observable``     | Removes a card previously published   |                           |
|                  | by this extension. Cannot clear       |                           |
|                  | other extensions' cards.              |                           |
+------------------+---------------------------------------+---------------------------+
| ``continue_      | *(none)*                              | ``{ok: true}``            |
| session``        | Triggers a new agent turn.            | SDK timeout: 60 s         |
+------------------+---------------------------------------+---------------------------+
| ``side_query``   | ``{question, model?, provider?,       | ``{ok: true,              |
|                  | effort?}``                            | text: "..."}``            |
|                  | One-shot LLM call, no history.        | SDK timeout: 120 s        |
|                  | model/provider/effort override the    |                           |
|                  | agent's defaults for this call.       |                           |
+------------------+---------------------------------------+---------------------------+
| ``set_session_   | ``{key, value}``                      | ``{ok: true}``            |
| data``           | Persist string K/V; survives          |                           |
|                  | ``/reexec`` via sidecar.              |                           |
+------------------+---------------------------------------+---------------------------+
| ``get_session_   | ``{key}``                             | ``{value: "...",          |
| data``           |                                       | ok: bool}``               |
+------------------+---------------------------------------+---------------------------+
| ``get_session_   | ``{}``                                | ``{path: "..."}``         |
| file``           | Absolute path to the session JSONL    | Empty for in-memory       |
|                  | transcript on disk. Tail-able from    | sessions.                 |
|                  | byte 0; foundation of ``fir observe``.|                           |
+------------------+---------------------------------------+---------------------------+
| ``get_session_   | ``{}``                                | ``{name: "..."}``         |
| name``           | Session display name. Empty if unset. |                           |
+------------------+---------------------------------------+---------------------------+
| ``get_session_   | ``{}``                                | ``{id: "..."}``           |
| id``             | Unique session id. Also in            |                           |
|                  | ``session_start`` event params.       |                           |
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
    # description: One-line summary
    # builtin: true                       # set on extensions shipped with fir
    # modes: tui, acp                     # restrict to specific fir modes
    # cli_verbs: my-cmd, other-cmd        # register top-level `fir <verb>`s
    # ---

The actual capability set (tools, commands, subscribed events) is reported by
the extension during the ``init`` handshake — there is no parallel
declaration in frontmatter.  All extensions start eagerly in parallel; there
is no lazy startup.

Supported ``modes`` values: ``tui`` (alias ``interactive``), ``text``,
``json``, ``rpc``, ``acp``.  Omitting the key runs the extension in all modes.

-------------------------------------------------------------------------------
CLI VERBS  (`fir <verb>` invocations)
-------------------------------------------------------------------------------

Extensions may declare top-level ``fir <verb>`` names in frontmatter. fir
discovers these without spawning the extension; on a matching invocation
the extension is started cold (no session) and dispatched via a
``cli_invoke`` request. The bridge owns stdio, so output flows through
``cli_stdout`` / ``cli_stderr`` notifications and input arrives via
``cli_stdin``::

    # ---
    # cli_verbs: greet
    # ---

    @fir_ext.cli_verb("greet", summary="Say hello")
    def greet(argv, host):
        host.println("hello", *argv)
        return 0

    @fir_ext.on_cli_signal
    def _on_sig(name, host):
        if "interrupt" in name.lower():
            os._exit(130)

The ``host`` argument is a ``Host`` (see class docstring) bound to fir's
real TTY. Bridge methods on ``Context`` that require a session
(``send_user_message`` etc.) are unavailable in verb mode. See
``docs/extension-protocol.md`` § CLI Verbs for the full wire protocol.

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
import os
import sys
import threading
from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Any, Optional, Protocol, TypedDict, TypeVar

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
# Typed wire surface
# ---------------------------------------------------------------------------
#
# Every shape that crosses the JSON-RPC boundary has a ``TypedDict`` so
# extension authors get IDE/type-checker support while we keep zero runtime
# overhead — at runtime these are still plain ``dict``s.
#
# Conventions:
# * ``total=False`` whenever a key is genuinely optional on the wire.
# * Names mirror the wire shape: ``ToolExecutionStartParams`` is what arrives
#   with ``event/tool_execution_start``.
# * Result types end in ``Result``; param types end in ``Params``.
# * Hook return types end in ``HookResult`` and use ``total=False`` because
#   returning ``None`` is always a valid "pass-through" answer.
#
# Compatibility note: every TypedDict is a ``dict`` at runtime, so existing
# handlers that already use ``params.get(...)`` / ``result["ok"]`` keep
# working unchanged. Nothing forces extension code to import these types.


# -- content / tool result --------------------------------------------------


class ContentBlock(TypedDict, total=False):
    """A single content block returned from a tool call."""

    type: str  # currently always "text"
    text: str


class ToolResult(TypedDict, total=False):
    """Structured result of a tool invocation."""

    content: list[ContentBlock]
    is_error: bool


# -- tool / command / hook spec (init handshake) ----------------------------


class TitleArgSpec(TypedDict, total=False):
    """One entry in a ``DisplayHint.title_args`` list."""

    name: str
    style: str   # "path" | "pattern" | "accent" | ""
    label: str


class DisplayHint(TypedDict, total=False):
    """TUI rendering hints attached to a tool definition."""

    title_args: list[TitleArgSpec]
    result_max_lines: int
    use_box: bool


class ToolSpec(TypedDict, total=False):
    """Tool definition reported to fir during the init handshake."""

    name: str
    description: str
    parameters: dict   # JSON Schema; arbitrary nested shape
    display_hint: DisplayHint


class CommandSpec(TypedDict, total=False):
    """Slash-command spec reported during init."""

    name: str
    description: str


class AuthProviderSpec(TypedDict, total=False):
    """Auth-provider spec reported during init."""

    id: str
    name: str
    uses_callback_server: bool


# -- hosted provider (extension-shipped) ------------------------------------
#
# An extension can ship a hosted AI provider — the host treats it like a
# built-in provider but proxies all streaming, listing, and (optional)
# custom-id resolution back to the extension over JSON-RPC. The wire shapes
# below mirror Go's pkg/extension.ProviderSpec / ProviderModelSpec.
#
# Use the dataclass helpers (:class:`Provider`, :class:`Model`,
# :class:`EnvKeys`) plus :func:`register_provider` and the
# ``@provider_stream`` / ``@provider_list_models`` /
# ``@provider_resolve_custom_id`` decorators rather than constructing the
# raw dicts by hand. See ``demo.py`` for a worked example.


@dataclass
class EnvKeys:
    """Environment-variable spec for an extension-shipped provider's API key.

    Mirrors the Go-side ``EnvKeysSpec``.

    Attributes
    ----------
    primary
        Primary env var fir reads to pick up the API key (e.g. ``"ECHO_API_KEY"``).
    fallbacks
        Additional env vars consulted if ``primary`` is unset.
    authenticated
        Set to ``True`` when this provider uses OAuth (``oauth_provider_id``)
        rather than a static API key.
    """

    primary: str = ""
    fallbacks: list[str] = field(default_factory=list)
    authenticated: bool = False


@dataclass
class Model:
    """A single model an extension-shipped provider declares at handshake.

    Maps to a subset of fir's :class:`ai.Model`. Field names are snake_case
    here and on the wire; fir converts them to its internal camelCase.
    """

    id: str
    name: str = ""
    base_url: str = ""
    reasoning: bool = False
    input: list[str] = field(default_factory=list)  # "text", "image"
    context_window: int = 0
    max_tokens: int = 0
    cost_input: float = 0.0
    cost_output: float = 0.0
    cost_cache_read: float = 0.0
    cost_cache_write: float = 0.0
    server_tools: list[str] = field(default_factory=list)
    compaction: bool = False
    reasoning_effort_values: list[str] = field(default_factory=list)
    swe_score: float = 0.0
    swe_inferred: bool = False


@dataclass
class Provider:
    """A hosted AI provider contributed by an extension.

    Pass instances to :func:`register_provider`. Decorate streaming /
    listing / custom-id handlers with :func:`provider_stream`,
    :func:`provider_list_models`, :func:`provider_resolve_custom_id` —
    the decorators key off ``id``.

    Attributes
    ----------
    id
        Unique provider identifier (e.g. ``"echo"``, ``"my-corp-llm"``).
        Must match ``[a-z][a-z0-9-]*`` and not collide with built-in
        providers (unless the extension is shipped under the ``builtin``
        scope).
    api
        Wire-protocol selector for streaming dispatch.

        - ``""`` (default): fir allocates a synthetic ``ext:<id>`` Api
          and routes streams back via ``provider/stream/start``. Pair
          with a :func:`provider_stream` handler that does the actual
          streaming in Python.
        - A built-in wire protocol id (e.g. ``"openai-completions"``,
          ``"anthropic-messages"``): fir reuses its in-process stream
          function for that protocol. The extension ships only metadata
          (display name, env keys, OAuth wiring, model catalogue) — no
          ``@provider_stream`` handler is needed. This is how built-in
          providers can be migrated out of core into an extension while
          keeping the wire code in Go.

    See ``docs/extension-protocol.md`` § Hosted providers for the full
    wire reference.
    """

    id: str
    api: str = ""
    display_name: str = ""
    short_name: str = ""
    priority: int = 0
    default_model_id: str = ""
    key_link: str = ""
    env_keys: EnvKeys = field(default_factory=EnvKeys)
    oauth_provider_id: str = ""
    claims_model_id_globs: list[str] = field(default_factory=list)
    refuse_fuzzy_match: bool = False
    supports_live_list: bool = False
    supports_custom_id: bool = False
    models: list[Model] = field(default_factory=list)


# Wire-shape TypedDicts: what crosses provider/* JSON-RPC. They mirror
# ai.AssistantMessageEvent's JSON form (camelCase keys — see pkg/ai/types.go).


# -- Api specs (extension-shipped wire protocols) --------------------------
#
# An extension may ship not just a hosted provider (a service) but also the
# wire-protocol *adapter* that talks to it, when that adapter is data-driven
# (endpoints, headers, an envelope template). The host dispatches the spec
# to a kind handler in pkg/ai/providers — currently only "decl-google" is
# supported (Cloud-Code-Assist Gemini family). New kinds are added by
# registering another apikind.Handler in core.


@dataclass
class DeclGoogleConditional:
    """One header-overlay rule applied when its match clause fires."""

    when_model_id_prefix: str = ""
    when_requires_reasoning: bool = False
    set: dict[str, str] = field(default_factory=dict)


@dataclass
class DeclGoogleApi:
    """A Cloud-Code-Assist Gemini wire-protocol adapter, shipped as data.

    Mirrors the runtime ``DeclGoogleConfig`` struct in
    ``pkg/ai/providers/declgoogle.go``. Serialised on the wire under
    ``ApiSpec(kind="decl-google", payload=…)``.

    Attributes
    ----------
    id
        Wire-protocol identifier (e.g. ``"my-corp-wire-v1"``). Must
        match ``[a-z][a-z0-9-]*`` and not collide with a built-in Api
        (unless the extension is shipped under the ``builtin`` scope).
    endpoints
        HTTPS bases tried in order on 403/404 cascade. Substitutions
        (``${var}``) supported.
    headers
        Per-request base header set (excluding ``Authorization`` /
        ``Content-Type`` / ``Accept``). Substitutions supported, e.g.
        ``"User-Agent": "myua/1.0 ${os}/${arch}"``.
    conditional_headers
        Header overlays applied when their ``when`` clause matches the
        resolved model.
    envelope
        JSON template for the outer request body (string form). The
        literal substring ``"$inner"`` is replaced by the inner Gemini
        request body. Empty string means "send the inner body as-is."
    system_instruction_prefix
        Texts prepended to ``systemInstruction.parts`` on every request.
    system_instruction_role
        Optional override for the role on the ``systemInstruction``
        object (e.g. ``"user"`` to inject the prefix as a user turn).
    reasoning_header_prefix
        Prefix the adapter looks for in ``options.headers`` when
        extracting thinking-config (e.g. ``"x-gemini-thinking-"``).
    """

    id: str
    endpoints: list[str] = field(default_factory=list)
    headers: dict[str, str] = field(default_factory=dict)
    conditional_headers: list[DeclGoogleConditional] = field(default_factory=list)
    envelope: str = ""
    system_instruction_prefix: list[str] = field(default_factory=list)
    system_instruction_role: str = ""
    reasoning_header_prefix: str = ""


class AssistantMessageEvent(TypedDict, total=False):
    """A single streaming event emitted by a provider stream generator.

    Keys mirror Go's ``ai.AssistantMessageEvent`` JSON shape (camelCase).
    Only ``type`` is required; the rest depend on the event variant.

    Common variants
    ---------------
    * ``{"type": "start", "partial": <AssistantMessage>}`` — stream opening
    * ``{"type": "text_start", "contentIndex": 0}``
    * ``{"type": "text_delta", "contentIndex": 0, "delta": "hi"}``
    * ``{"type": "text_end", "contentIndex": 0, "content": "hi"}``
    * ``{"type": "done", "reason": "stop", "message": <AssistantMessage>}``
    * ``{"type": "error", "reason": "error", "error": <AssistantMessage>}``

    The stream MUST end with exactly one ``done`` or ``error`` event;
    otherwise the runtime synthesises a terminal ``error`` for safety.
    """

    type: str
    contentIndex: int
    delta: str
    content: str
    toolCall: dict
    reason: str
    partial: dict
    message: dict
    error: dict


class ProviderStreamStartParams(TypedDict, total=False):
    """Params delivered with an inbound ``provider/stream/start`` request."""

    provider_id: str
    stream_id: str
    model: dict
    prompt: dict   # ai.Context — system prompt + messages + tools
    options: dict  # ai.StreamOptions


class ProviderListModelsParams(TypedDict, total=False):
    """Params delivered with ``provider/listModels``."""

    provider_id: str
    base_url: str
    api_key: str


class ProviderListModelsResult(TypedDict, total=False):
    """Result returned to ``provider/listModels``."""

    model_ids: list[str]


class ProviderResolveCustomIDParams(TypedDict, total=False):
    """Params delivered with ``provider/resolveCustomId``."""

    provider_id: str
    model_id: str


class InitParams(TypedDict, total=False):
    """Params delivered with the inbound ``init`` request."""

    version: str
    cwd: str
    config_dirs: list[str]


class InitResult(TypedDict, total=False):
    """Response payload for the ``init`` handshake."""

    name: str
    tools: list[ToolSpec]
    commands: list[CommandSpec]
    events: list[str]
    auth_providers: list[AuthProviderSpec]
    providers: list   # list[dict] — see Provider/register_provider
    tool_name_map: dict   # str -> str
    cli_verbs: list[str]


# -- tool_call request ------------------------------------------------------


class ToolCallParams(TypedDict, total=False):
    """Params delivered with an inbound ``tool_call`` request."""

    tool_call_id: str
    name: str
    params: dict   # caller-defined; per-tool schema


# -- hook payloads ----------------------------------------------------------


class ToolCallHookParams(TypedDict, total=False):
    """Params for ``hook/tool_call``."""

    tool_call_id: str
    tool_name: str
    params: dict


class ToolCallHookResult(TypedDict, total=False):
    """Return shape of a ``hook/tool_call`` handler.

    Returning ``None`` (or an empty dict) allows the call to proceed.
    """

    block: bool
    reason: str


class CommandHookParams(TypedDict, total=False):
    """Params for ``hook/command``."""

    name: str
    args: list[str]


class CommandHookResult(TypedDict, total=False):
    """Return shape of a slash-command handler."""

    message: str
    print_response: bool


# -- event payloads ---------------------------------------------------------
#
# Many event payloads have no fields (notification with no params); those use
# an empty TypedDict for symmetry with the typed handler signatures.


class _Empty(TypedDict, total=False):
    """Empty event payload (no fields)."""


class SessionStartParams(TypedDict, total=False):
    session_id: str
    session_data: dict


class SessionShutdownParams(_Empty):
    """Params for ``session_shutdown`` (always empty)."""


class SessionEndParams(TypedDict, total=False):
    """Params for ``session_end``."""

    reason: str
    error: str


class SessionNamedParams(TypedDict, total=False):
    """Params for ``session_named``."""

    name: str


class PlanInfo(TypedDict, total=False):
    """The ``plan`` field of a ``session_update`` event."""

    total: int
    completed: int
    metadata: dict


class SessionUpdateParams(TypedDict, total=False):
    """Params for the generic ``session_update`` event."""

    type: str   # "session_named" | "plan_update"
    session_name: str
    plan: PlanInfo


class AgentLifecycleParams(_Empty):
    """Empty params for agent_*/turn_*/message_start events."""


class MessageEndCost(TypedDict, total=False):
    input: float
    output: float
    cache_read: float
    cache_write: float
    total: float


class MessageEndUsage(TypedDict, total=False):
    input: int
    output: int
    cache_read: int
    cache_write: int
    total_tokens: int
    cost: MessageEndCost


class MessageEndParams(TypedDict, total=False):
    role: str
    provider: str
    model: str
    stop_reason: str
    response_id: str
    usage: MessageEndUsage


class ToolExecutionStartParams(TypedDict, total=False):
    tool_call_id: str
    tool_name: str


class ToolExecutionEndParams(TypedDict, total=False):
    tool_call_id: str
    tool_name: str
    is_error: bool
    error_text: str


# -- bridge method (extension → fir) params/results -------------------------
#
# Param shapes — used as the body of ctx._call() — and result shapes — what
# the call returns.


class OkResult(TypedDict, total=False):
    """Generic ``{"ok": true}`` ack."""

    ok: bool


class NotifyParams(TypedDict, total=False):
    message: str
    level: str   # "info" | "warning" | "error"


class ExecParams(TypedDict, total=False):
    command: str
    args: list[str]


class ExecResult(TypedDict, total=False):
    stdout: str
    stderr: str
    exit_code: int


class SendMessageParams(TypedDict, total=False):
    custom_type: str
    content: object   # any JSON-serialisable value
    display: bool
    deliver_as: str
    trigger_turn: bool


class SendUserMessageParams(TypedDict, total=False):
    content: str
    deliver_as: str


class SetSessionNameParams(TypedDict, total=False):
    name: str


class SetLabelParams(TypedDict, total=False):
    entry_id: str
    label: str


class ClearLabelParams(TypedDict, total=False):
    entry_id: str


class SetModelParams(TypedDict, total=False):
    provider: str
    id: str


class SetStatusParams(TypedDict, total=False):
    status: str


class SideQueryParams(TypedDict, total=False):
    question: str
    model: str
    provider: str
    effort: str   # "off" | "minimal" | "low" | "medium" | "high" | "xhigh" | "max"


class SideQueryResult(TypedDict, total=False):
    ok: bool
    text: str


class SetSessionDataParams(TypedDict, total=False):
    key: str
    value: str


class GetSessionDataParams(TypedDict, total=False):
    key: str


class GetSessionDataResult(TypedDict, total=False):
    value: str
    ok: bool


class GetSessionFileResult(TypedDict, total=False):
    path: str


class GetSessionNameResult(TypedDict, total=False):
    name: str


class GetSessionIDResult(TypedDict, total=False):
    id: str


class CallToolParams(TypedDict, total=False):
    name: str
    params: dict


class ListToolsItem(TypedDict, total=False):
    name: str
    description: str
    parameters: dict


class PrependContextParams(TypedDict, total=False):
    content: str


class ReportProgressParams(TypedDict, total=False):
    message: str


# -- CLI verb wire shapes ---------------------------------------------------


class CLIInvokeParams(TypedDict, total=False):
    verb: str
    argv: list[str]
    cwd: str
    stdin_is_tty: bool
    stdout_is_tty: bool
    stderr_is_tty: bool


class CLIInvokeResult(TypedDict, total=False):
    exit_code: int


class CLIStdinParams(TypedDict, total=False):
    data: str
    eof: bool


class CLISignalParams(TypedDict, total=False):
    name: str


class CLIStdoutParams(TypedDict, total=False):
    data: str


# -- Auth provider wire shapes ---------------------------------------------


class AuthCredentials(TypedDict, total=False):
    access: str
    refresh: str
    expires: int


class AuthLoginParams(TypedDict, total=False):
    provider_id: str


class AuthRefreshParams(TypedDict, total=False):
    provider_id: str
    credentials: AuthCredentials


class AuthAPIKeyParams(TypedDict, total=False):
    provider_id: str
    credentials: AuthCredentials


class AuthListModelsParams(TypedDict, total=False):
    provider_id: str
    credentials: AuthCredentials


class AuthModifyModelsParams(TypedDict, total=False):
    provider_id: str
    credentials: AuthCredentials
    models: list


class AuthLoginResult(TypedDict, total=False):
    credentials: AuthCredentials


class AuthAPIKeyResult(TypedDict, total=False):
    api_key: str


class AuthModelsResult(TypedDict, total=False):
    models: list | None


class PKCEResult(TypedDict, total=False):
    verifier: str
    challenge: str


class CallbackServerParams(TypedDict, total=False):
    addr: str
    path: str
    state: str


class CallbackServerResult(TypedDict, total=False):
    addr: str
    redirect_uri: str


class CallbackResult(TypedDict, total=False):
    code: str
    state: str


class AuthOpenURLParams(TypedDict, total=False):
    url: str
    short_url: str
    instructions: str


class AuthProgressParams(TypedDict, total=False):
    message: str


class AuthPromptParams(TypedDict, total=False):
    message: str
    placeholder: str
    allow_empty: bool


class AuthPromptResult(TypedDict, total=False):
    value: str


# -- handler-shape aliases --------------------------------------------------
#
# These are documentation aliases; we keep the registries weakly typed
# (Callable) so existing extensions that don't import these aliases stay
# valid. Use them in your own code to get full IDE support, e.g.::
#
#     ToolHandler = fir_ext.ToolHandlerType
#     def my_tool(params: dict, ctx: fir_ext.Context) -> fir_ext.ToolResult: ...

_T_Params = TypeVar("_T_Params")

if TYPE_CHECKING:
    from collections.abc import Callable as _Callable

    ToolHandlerType = _Callable[[dict, "Context"], Any]
    """``(params, ctx) -> dict | str | ToolResult`` — see :func:`tool`."""

    EventHandlerType = _Callable[[dict, "Context"], None]
    """``(params, ctx) -> None`` — see :func:`on`."""

    HookHandlerType = _Callable[[dict, "Context"], Any]
    """``(params, ctx) -> Optional[dict]`` — see :func:`on`."""

    CommandHandlerType = _Callable[[list[str], "Context"], Optional[CommandHookResult]]
    """``(args, ctx) -> Optional[CommandHookResult]`` — see :func:`command`."""

    CLIVerbHandlerType = _Callable[[list[str], "Host"], int]
    """``(argv, host) -> exit_code`` — see :func:`cli_verb`."""


# Public re-exports for the typed surface. Existing extensions don't need to
# import these — TypedDicts are plain ``dict`` at runtime — but adding them
# to ``__all__`` keeps autocomplete and documentation tooling happy.
__all__ = [
    "AgentLifecycleParams",
    "AssistantMessageEvent",
    "AuthAPIKeyParams",
    "AuthAPIKeyResult",
    "AuthContext",
    # Auth
    "AuthCredentials",
    "AuthListModelsParams",
    "AuthLoginParams",
    "AuthLoginResult",
    "AuthModelsResult",
    "AuthModifyModelsParams",
    "AuthOpenURLParams",
    "AuthProgressParams",
    "AuthPromptParams",
    "AuthPromptResult",
    "AuthProviderSpec",
    "AuthRefreshParams",
    # CLI verbs
    "CLIInvokeParams",
    "CLIInvokeResult",
    "CLISignalParams",
    "CLIStdinParams",
    "CLIStdoutParams",
    "CallToolParams",
    "CallbackResult",
    "CallbackServerParams",
    "CallbackServerResult",
    "ClearLabelParams",
    "CommandHookParams",
    "CommandHookResult",
    "CommandSpec",
    # Content / tool result
    "ContentBlock",
    "Context",
    "DeclGoogleApi",
    "DeclGoogleConditional",
    "DisplayHint",
    "EnvKeys",
    "ExecParams",
    "ExecResult",
    "GetSessionDataParams",
    "GetSessionDataResult",
    "GetSessionFileResult",
    "GetSessionIDResult",
    "GetSessionNameResult",
    "Host",
    "InitParams",
    "InitResult",
    "ListToolsItem",
    "MessageEndCost",
    "MessageEndParams",
    "MessageEndUsage",
    "Model",
    "NotifyParams",
    # Bridge methods
    "OkResult",
    "PKCEResult",
    "PlanInfo",
    "PrependContextParams",
    "Provider",
    "ProviderListModelsParams",
    "ProviderListModelsResult",
    "ProviderResolveCustomIDParams",
    "ProviderStreamStartParams",
    "ReportProgressParams",
    "SendMessageParams",
    "SendUserMessageParams",
    "SessionEndParams",
    "SessionNamedParams",
    "SessionShutdownParams",
    # Events
    "SessionStartParams",
    "SessionUpdateParams",
    "SetLabelParams",
    "SetModelParams",
    "SetSessionDataParams",
    "SetSessionNameParams",
    "SetStatusParams",
    "SideQueryParams",
    "SideQueryResult",
    # Init / definitions
    "TitleArgSpec",
    "ToolCallHookParams",
    "ToolCallHookResult",
    # Tool calls / hooks
    "ToolCallParams",
    "ToolError",
    "ToolExecutionEndParams",
    "ToolExecutionStartParams",
    "ToolResult",
    "ToolSpec",
    "auth_api_key",
    "auth_list_models",
    "auth_modify_models",
    "auth_post_exchange",
    "auth_provider",
    "auth_refresh",
    "cli_verb",
    "command",
    "config_path",
    "declare_oauth_provider",
    "is_cancelled",
    "load_config",
    "on",
    "on_cli_signal",
    "provider_list_models",
    "provider_resolve_custom_id",
    "provider_stream",
    "register_api",
    "register_provider",
    "register_tool_name_map",
    "run",
    # Decorators / lifecycle
    "tool",
]


# ---------------------------------------------------------------------------
# Global registries (populated by decorators, consumed by run())
# ---------------------------------------------------------------------------

_tools: list[dict[str, Any]] = []
_tool_handlers: dict[str, Callable] = {}
_hook_handlers: dict[str, Callable] = {}
_event_handlers: dict[str, Callable] = {}
_commands: list[dict[str, Any]] = []
_command_handlers: dict[str, Callable] = {}

# CLI verb registry — top-level `fir <verb>` names this extension claims.
# Populated by @cli_verb decorator; declared at runtime so fir's verb-dispatcher
# (which reads frontmatter, *not* init result) need only spawn us once. The
# init result echoes the list back for diagnostic purposes.
_cli_verb_handlers: dict[str, Callable] = {}
_cli_signal_handlers: list[Callable] = []

# Auth provider registries
_auth_providers: list[dict[str, Any]] = []
_auth_login_handlers: dict[str, Callable] = {}
_auth_refresh_handlers: dict[str, Callable] = {}
_auth_api_key_handlers: dict[str, Callable] = {}
_auth_list_models_handlers: dict[str, Callable] = {}
_auth_modify_models_handlers: dict[str, Callable] = {}
_auth_post_exchange_handlers: dict[str, Callable] = {}

# Hosted-provider registries — populated by register_provider() and the
# provider_* decorators, consumed by run() to build the init payload and to
# dispatch provider/* RPCs.
_providers: list[dict[str, Any]] = []
_provider_stream_handlers: dict[str, Callable] = {}
_provider_list_models_handlers: dict[str, Callable] = {}
_provider_resolve_custom_id_handlers: dict[str, Callable] = {}

# Wire-protocol Api specs contributed by this extension. Wire shape:
# ``{id, kind, payload}`` per spec, dispatched by fir to a kind handler.
_apis: list[dict[str, Any]] = []

# Per-stream cancel events for provider/stream/cancel. Keyed by stream_id;
# generators may consult :func:`is_cancelled` to abort early.
_stream_cancels: dict[str, threading.Event] = {}
_stream_cancels_lock = threading.Lock()

# Static tool-name map: fir tool name → canonical provider-side tool name.
# Collected once at init and sent to fir in the handshake result under
# ``tool_name_map``. Consumed by provider adapters (e.g. anthropic OAuth
# mode) to translate tool names to and from the LLM. Only extensions that
# bridge fir tools to a provider-specific naming scheme should populate it
# (see ``register_tool_name_map``).
_tool_name_map: dict[str, str] = {}

# Init-handshake state populated when fir sends "init". See load_config()
# / config_path() below for the recommended way to use this.
cwd: str = ""  # project working directory (set during init)
config_dirs: list[str] = []  # priority-ordered config dirs (highest first)
ext_name: str = ""  # name this extension registered with (set during init)


def load_config(filename: str | None = None) -> dict[str, Any] | None:
    """Read this extension's JSON config file from the host-provided dirs.

    Searches ``config_dirs`` in priority order (highest first) and returns the
    parsed JSON contents of the first existing file. Returns ``None`` when no
    file is found.

    ``filename`` defaults to ``f"{ext_name}.json"``.
    """
    name = filename or (f"{ext_name}.json" if ext_name else None)
    if not name:
        return None
    for d in config_dirs:
        p = os.path.join(d, name)
        if os.path.isfile(p):
            try:
                with open(p, encoding="utf-8") as f:
                    return json.load(f)
            except (OSError, ValueError):
                return None
    return None


def config_path(filename: str | None = None) -> str | None:
    """Return the highest-priority path for writing this extension's config.

    Always points at ``config_dirs[0]/{filename}``. ``filename`` defaults to
    ``f"{ext_name}.json"``. Returns ``None`` when no config dirs were
    advertised by the host (e.g. running outside fir).

    The caller is responsible for creating the parent directory if needed.
    """
    name = filename or (f"{ext_name}.json" if ext_name else None)
    if not name or not config_dirs:
        return None
    return os.path.join(config_dirs[0], name)

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


def cli_verb(name: str, summary: str = "") -> Callable:
    """Register a top-level ``fir <name>`` CLI verb handler.

    The decorated function receives ``(argv: list[str], host: Host)`` where
    ``argv`` is the list of arguments after the verb name and ``host`` is a
    minimal output/input helper bound to fir's real TTY (since the
    extension's own stdio carries the JSON-RPC bridge). The handler should
    return an integer exit code.

    Example::

        @fir_ext.cli_verb("greet", summary="Say hello")
        def greet(argv, host):
            who = argv[0] if argv else "world"
            host.println(f"hello {who}")
            return 0

    The verb name **must** also be declared in the extension's frontmatter
    under ``cli_verbs:`` so fir can dispatch without spawning every
    extension on every CLI invocation::

        # ---
        # name: my-ext
        # cli_verbs: greet, summarise
        # ---
    """
    del summary  # reserved for future help-text integration

    def decorator(fn: Callable) -> Callable:
        _cli_verb_handlers[name] = fn
        return fn

    return decorator


def on_cli_signal(fn: Callable) -> Callable:
    """Register a handler called when fir forwards a signal during a CLI verb.

    Receives ``(name: str, host: Host)`` where ``name`` is the signal name
    (e.g. ``"interrupt"``, ``"quit"``, ``"terminated"``, ``"window size changes"``).
    Multiple handlers may be registered; all are invoked in order.

    Use this to clean up and exit promptly when the user hits Ctrl-C or
    Ctrl-\\ during ``fir <verb>``.
    """
    _cli_signal_handlers.append(fn)
    return fn


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


def declare_oauth_provider(
    *,
    provider_id: str,
    name: str,
    client_id: str,
    authorize_url: str,
    token_url: str,
    scope: str = "",
    client_secret: str = "",
    callback_addr: str = "",
    callback_path: str = "",
    disable_callback_server: bool = False,
    manual_redirect_uri: str = "",
    auth_params_extra: dict[str, str] | None = None,
    token_body_json: bool = False,
    token_body_extra: dict[str, str] | None = None,
    token_headers: dict[str, str] | None = None,
    open_url_instructions: str = "",
    short_url_base: str = "",
    has_post_exchange: bool | None = None,
    has_custom_refresh: bool | None = None,
    uses_callback_server: bool = True,
) -> None:
    """Declare a standard authorization-code+PKCE OAuth provider.

    fir drives the entire flow — PKCE generation, callback server, browser
    open, code exchange, and token refresh — using the static config
    supplied here. The extension only needs to register the optional
    hooks it actually requires:

    * :func:`auth_post_exchange` — enrich credentials after the token
      endpoint returns (e.g. extract an account ID from a JWT).
    * :func:`auth_api_key` — override the trivial "access token = api key"
      default.
    * :func:`auth_list_models` — provider-specific model discovery.
    * :func:`auth_modify_models` — inject provider-specific HTTP headers
      onto outbound model requests (User-Agent impersonation, etc.).
    * :func:`auth_refresh` — replace the default standard refresh with a
      custom flow.

    Compared to the imperative :func:`auth_provider` (which receives a
    bare ``ctx`` and orchestrates the whole flow itself), this avoids
    one JSON-RPC round-trip per generic step (PKCE, callback, browser,
    exchange, parse) and dramatically shrinks the extension surface.

    Parameters
    ----------
    provider_id : str
        Stable provider identifier (matches the ``auth_providers``
        frontmatter value).
    name : str
        Human-readable display name.
    client_id : str
        OAuth client identifier (RFC 6749 §2.2).
    authorize_url : str
        Authorization endpoint URL (RFC 6749 §3.1).
    token_url : str
        Token endpoint URL (RFC 6749 §3.2).
    scope : str, optional
        Space-separated list of requested scopes.
    client_secret : str, optional
        OAuth client secret. Native apps (RFC 8252) typically omit this.
    callback_addr : str, optional
        ``host:port`` the local callback server binds to. Defaults to
        ``"127.0.0.1:0"`` (auto-pick).
    callback_path : str, optional
        URL path of the callback endpoint. Defaults to ``"/callback"``.
    manual_redirect_uri : str, optional
        Redirect URI used when the local callback server cannot bind
        and the user must paste the code by hand. Empty means no
        manual fallback.
    auth_params_extra : dict[str, str], optional
        Extra query parameters appended to the authorization URL.
    token_body_json : bool, optional
        Encode the token-request body as JSON instead of
        ``application/x-www-form-urlencoded``. Anthropic requires this.
    token_body_extra : dict[str, str], optional
        Extra fields injected into the token-request body on both
        initial exchange and refresh. Useful for provider-specific
        knobs (e.g. an audience parameter) and for providers whose
        token endpoint requires fields RFC 6749 §4.1.3 does not list.
        Values may contain the literal placeholder ``"{state}"``,
        which fir replaces with the per-session OAuth state value
        before sending the Exchange request (Refresh has no
        per-session state and substitutes ``"{state}"`` with the
        empty string). Keys must not collide with pinoauth-owned
        form fields (``grant_type``, ``client_id``, ``client_secret``,
        ``code``, ``code_verifier``, ``redirect_uri``,
        ``refresh_token``, ``scope``); pinoauth rejects those at
        request time.
    token_headers : dict[str, str], optional
        Extra HTTP headers on the token request (e.g. custom
        User-Agent). Content-Type is owned by the body encoder.
    open_url_instructions : str, optional
        Human-readable text shown alongside the authorization URL.
    short_url_base : str, optional
        Base of a pre-created URL shortener (e.g.
        ``"https://tinyurl.com/fir-ant"``) whose stored target is the
        static portion of the authorize URL. fir appends the per-session
        params (state, code_challenge, redirect_uri) at click time; the
        shortener merges them with the stored target. Cuts worst-case
        auth URLs from ~600+ chars to ~200 — friendlier in terminals/QR
        codes. Empty to disable.
    has_post_exchange, has_custom_refresh : bool, optional
        Override the auto-detection of which hooks the extension
        provides. Normally left as ``None`` — the SDK populates these
        from whether the corresponding decorators were called.
    uses_callback_server : bool, optional
        Whether the provider's flow can complete without interactive
        prompts (used by ACP mode to decide whether to surface the
        provider). Defaults to True since the generic flow always
        starts a callback server.
    """
    _auth_providers.append(
        {
            "id": provider_id,
            "name": name,
            "uses_callback_server": uses_callback_server,
            "flow": {
                "client_id": client_id,
                "client_secret": client_secret,
                "authorize_url": authorize_url,
                "token_url": token_url,
                "scope": scope,
                "callback_addr": callback_addr,
                "callback_path": callback_path,
                "disable_callback_server": disable_callback_server,
                "manual_redirect_uri": manual_redirect_uri,
                "auth_params_extra": dict(auth_params_extra) if auth_params_extra else {},
                "token_body_json": token_body_json,
                "token_body_extra": dict(token_body_extra) if token_body_extra else {},
                "token_headers": dict(token_headers) if token_headers else {},
                "open_url_instructions": open_url_instructions,
                "short_url_base": short_url_base,
                # has_post_exchange / has_custom_refresh are filled in at
                # init time from the decorator registries — see
                # _finalise_auth_specs.
                "_pending_provider_id": provider_id,
                "_explicit_post_exchange": has_post_exchange,
                "_explicit_custom_refresh": has_custom_refresh,
            },
        }
    )


def auth_post_exchange(provider: str) -> Callable:
    """Register a post-exchange enrichment handler for a declarative auth
    provider.

    Called after the initial code exchange and after each *default*
    refresh (the one fir runs via ``pinoauth.Refresh``). When the provider
    spec sets ``has_custom_refresh=True`` the extension's ``auth/refresh``
    hook owns the entire refresh including any post-exchange enrichment,
    so this hook is **not** invoked on top of a custom refresh.

    Receives ``params = {"provider_id": ..., "token": {access_token,
    refresh_token, expires_at, token_type, scope, raw}, "previous_credentials":
    {access, refresh, expires, extra}}`` (``previous_credentials`` is
    populated only on refresh) and must return ``{"credentials": {"access":
    ..., "refresh": ..., "expires": ..., "extra": ...}}`` (the expires field
    is epoch milliseconds; matches fir's stored shape).

    Use this when the provider's token response carries provider-specific
    fields you need to extract — e.g. extracting ``chatgpt_account_id`` from
    a JWT, capturing an embedded API key, or applying a refresh-window
    safety buffer.
    """

    def decorator(fn: Callable) -> Callable:
        _auth_post_exchange_handlers[provider] = fn
        return fn

    return decorator


def _finalise_auth_specs() -> None:
    """Fill in HasPostExchange / HasCustomRefresh on flow specs based on
    which decorators the extension actually used. Called once during the
    init handshake.
    """
    for spec in _auth_providers:
        flow = spec.get("flow")
        if not isinstance(flow, dict):
            continue
        pid = flow.pop("_pending_provider_id", spec.get("id", ""))
        explicit_pe = flow.pop("_explicit_post_exchange", None)
        explicit_cr = flow.pop("_explicit_custom_refresh", None)
        flow["has_post_exchange"] = (
            bool(explicit_pe) if explicit_pe is not None else pid in _auth_post_exchange_handlers
        )
        flow["has_custom_refresh"] = (
            bool(explicit_cr) if explicit_cr is not None else pid in _auth_refresh_handlers
        )


# ---------------------------------------------------------------------------
# Hosted-provider helpers
# ---------------------------------------------------------------------------


def _provider_to_wire(p: Provider) -> dict[str, Any]:
    """Convert a :class:`Provider` dataclass to its on-the-wire dict form."""
    out: dict[str, Any] = {"id": p.id}
    if p.api:
        out["api"] = p.api
    if p.display_name:
        out["display_name"] = p.display_name
    if p.short_name:
        out["short_name"] = p.short_name
    if p.priority:
        out["priority"] = p.priority
    if p.default_model_id:
        out["default_model_id"] = p.default_model_id
    if p.key_link:
        out["key_link"] = p.key_link
    ek: dict[str, Any] = {}
    if p.env_keys.primary:
        ek["primary"] = p.env_keys.primary
    if p.env_keys.fallbacks:
        ek["fallbacks"] = list(p.env_keys.fallbacks)
    if p.env_keys.authenticated:
        ek["authenticated"] = True
    if ek:
        out["env_keys"] = ek
    if p.oauth_provider_id:
        out["oauth_provider_id"] = p.oauth_provider_id
    if p.claims_model_id_globs:
        out["claims_model_id_globs"] = list(p.claims_model_id_globs)
    if p.refuse_fuzzy_match:
        out["refuse_fuzzy_match"] = True
    if p.supports_live_list:
        out["supports_live_list"] = True
    if p.supports_custom_id:
        out["supports_custom_id"] = True
    if p.models:
        models_wire: list[dict[str, Any]] = []
        for m in p.models:
            mw: dict[str, Any] = {"id": m.id}
            if m.name:
                mw["name"] = m.name
            if m.base_url:
                mw["base_url"] = m.base_url
            if m.reasoning:
                mw["reasoning"] = True
            if m.input:
                mw["input"] = list(m.input)
            if m.context_window:
                mw["context_window"] = m.context_window
            if m.max_tokens:
                mw["max_tokens"] = m.max_tokens
            if m.cost_input:
                mw["cost_input"] = m.cost_input
            if m.cost_output:
                mw["cost_output"] = m.cost_output
            if m.cost_cache_read:
                mw["cost_cache_read"] = m.cost_cache_read
            if m.cost_cache_write:
                mw["cost_cache_write"] = m.cost_cache_write
            if m.server_tools:
                mw["server_tools"] = list(m.server_tools)
            if m.compaction:
                mw["compaction"] = True
            if m.reasoning_effort_values:
                mw["reasoning_effort_values"] = list(m.reasoning_effort_values)
            if m.swe_score:
                mw["swe_score"] = m.swe_score
            if m.swe_inferred:
                mw["swe_inferred"] = True
            models_wire.append(mw)
        out["models"] = models_wire
    return out


def register_provider(provider: Provider) -> None:
    """Declare a hosted AI provider this extension contributes.

    Adds the provider to the init-handshake payload so fir registers a
    synthetic ``ext:<id>`` API and routes streaming completions for the
    declared models back to this extension. Pair with
    :func:`provider_stream` (required) and, optionally,
    :func:`provider_list_models` / :func:`provider_resolve_custom_id`.

    Idempotent on ``provider.id`` — later calls overwrite the earlier
    spec, which is convenient when reloading during development.
    """
    if not isinstance(provider, Provider):
        raise TypeError("register_provider expects a Provider instance")
    wire = _provider_to_wire(provider)
    for i, existing in enumerate(_providers):
        if existing.get("id") == provider.id:
            _providers[i] = wire
            return
    _providers.append(wire)


def _decl_google_api_to_wire(api: DeclGoogleApi) -> dict[str, Any]:
    """Serialise a DeclGoogleApi to the JSON payload fir expects."""
    out: dict[str, Any] = {}
    if api.endpoints:
        out["endpoints"] = list(api.endpoints)
    if api.headers:
        out["headers"] = dict(api.headers)
    if api.conditional_headers:
        ch_wire = []
        for ch in api.conditional_headers:
            when: dict[str, Any] = {}
            if ch.when_model_id_prefix:
                when["model_id_prefix"] = ch.when_model_id_prefix
            if ch.when_requires_reasoning:
                when["requires_reasoning"] = True
            ch_wire.append({"when": when, "set": dict(ch.set)})
        out["conditional_headers"] = ch_wire
    if api.envelope:
        out["envelope"] = api.envelope
    if api.system_instruction_prefix:
        out["system_instruction_prefix"] = [
            {"text": t} for t in api.system_instruction_prefix
        ]
    if api.system_instruction_role:
        out["system_instruction_role"] = api.system_instruction_role
    if api.reasoning_header_prefix:
        out["reasoning_header_prefix"] = api.reasoning_header_prefix
    return out


def register_api(api: DeclGoogleApi) -> None:
    """Declare a wire-protocol Api this extension contributes.

    The host dispatches the spec to a kind handler keyed by the type of
    ``api``. Currently only :class:`DeclGoogleApi` is supported (kind
    ``"decl-google"``); pair it with the matching kind handler in
    ``pkg/ai/providers/extkind_declgoogle.go``.

    A typical builtin extension that ships both the wire protocol *and*
    the hosted provider that uses it (e.g. one extension shipping both
    ``api="my-wire"`` and ``provider id="my-wire"``) calls
    :func:`register_api` *before* :func:`register_provider` so that any
    same-extension cross-reference resolves cleanly.

    Idempotent on ``api.id``.
    """
    if isinstance(api, DeclGoogleApi):
        kind = "decl-google"
        payload = _decl_google_api_to_wire(api)
    else:
        raise TypeError(f"register_api: unsupported spec type {type(api).__name__}")

    spec: dict[str, Any] = {"id": api.id, "kind": kind}
    if payload:
        spec["payload"] = payload
    for i, existing in enumerate(_apis):
        if existing.get("id") == api.id:
            _apis[i] = spec
            return
    _apis.append(spec)


def provider_stream(provider_id: str) -> Callable:
    """Register the streaming handler for a hosted provider.

    The decorated function MUST be a generator that takes
    ``(params: ProviderStreamStartParams, ctx: Context)`` and yields
    :class:`AssistantMessageEvent` dicts. It MUST end with a terminal
    ``done`` (success) or ``error`` event; the runtime otherwise
    synthesises an ``error`` event so the host stream cleans up.

    Cancellation: the runtime sets a per-stream :class:`threading.Event`
    when fir sends ``provider/stream/cancel``. Long-running generators
    should periodically check ``fir_ext.is_cancelled(stream_id)`` and
    yield a terminal ``error``/``done`` to bail out promptly.

    Example::

        @fir_ext.provider_stream("echo")
        def echo_stream(params, ctx):
            text = "hello"
            yield {"type": "text_start", "contentIndex": 0}
            yield {"type": "text_delta", "contentIndex": 0, "delta": text}
            yield {"type": "text_end", "contentIndex": 0, "content": text}
            yield {"type": "done", "reason": "stop", "message": {...}}
    """

    def decorator(fn: Callable) -> Callable:
        _provider_stream_handlers[provider_id] = fn
        return fn

    return decorator


def provider_list_models(provider_id: str) -> Callable:
    """Register the live model lister for a hosted provider.

    The decorated function takes ``(params: ProviderListModelsParams,
    ctx: Context)`` and returns a list of model ID strings (or a
    :class:`ProviderListModelsResult` dict). Only invoked when the
    provider's :class:`Provider` was declared with
    ``supports_live_list=True``.
    """

    def decorator(fn: Callable) -> Callable:
        _provider_list_models_handlers[provider_id] = fn
        return fn

    return decorator


def provider_resolve_custom_id(provider_id: str) -> Callable:
    """Register a custom-ID resolver for a hosted provider.

    The decorated function takes ``(params:
    ProviderResolveCustomIDParams, ctx: Context)`` and returns a wire-form
    :class:`Model` dict (or ``None`` to fall back). Only meaningful when
    the provider was declared with ``supports_custom_id=True``.

    Reserved for future host wiring; currently called by tests only.
    """

    def decorator(fn: Callable) -> Callable:
        _provider_resolve_custom_id_handlers[provider_id] = fn
        return fn

    return decorator


def is_cancelled(stream_id: str) -> bool:
    """Return ``True`` if fir has asked us to cancel this provider stream.

    Long-running :func:`provider_stream` generators should poll this
    between yields to abort promptly when the user interrupts the turn.
    """
    with _stream_cancels_lock:
        evt = _stream_cancels.get(stream_id)
    return bool(evt and evt.is_set())


def register_tool_name_map(mapping: dict[str, str]) -> None:
    """Declare a static mapping from fir tool names to canonical provider-side
    tool names.

    Consumed by fir once at init time (sent back as ``tool_name_map`` in the
    handshake result). Provider adapters use it to translate tool names to
    and from the LLM. For example, the anthropic-auth extension uses this
    to map fir's ``bash_kill`` to Claude Code's ``KillShell`` when running
    OAuth'd (Claude Pro/Max) sessions.

    Calling this multiple times merges into the existing map, later calls
    overriding earlier values for the same key.

    Parameters
    ----------
    mapping : dict
        Keys are fir tool names (e.g. ``"bash_kill"``), values are canonical
        provider names (e.g. ``"KillShell"``).
    """
    for k, v in mapping.items():
        if not isinstance(k, str) or not isinstance(v, str) or not k or not v:
            continue
        _tool_name_map[k] = v


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
        # tool_call_id is set by the SDK dispatcher for the lifetime of
        # a tool_call handler, "" otherwise. Mirrors the value the host
        # stamps on observable cards via put_observable; handlers can
        # read it to persist the same id in their own state.
        self.tool_call_id: str = ""

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
    ) -> ExecResult:
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
        result = self._call("exec", {"command": command, "args": args or []}, timeout=timeout)
        if isinstance(result, dict):
            return result  # type: ignore[return-value]
        return {"stdout": "", "stderr": "", "exit_code": 0}

    def send_message(
        self,
        custom_type: str,
        content: Any,
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
            "trigger_turn": trigger_turn,
        }
        if deliver_as is not None:
            params["deliver_as"] = deliver_as
        self._call("send_message", params)

    def send_user_message(self, content: str, deliver_as: str | None = None) -> None:
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

    def put_observable(self, key: str, slug: str, detail: str = "") -> None:
        """Publish an observable card.

        Observable cards are a per-session sidecar of state summaries
        that surface to humans and sibling agents through
        ``observe_session``. See ``docs/design/observable-cards.md``.

        Parameters
        ----------
        key : str
            Identifier within this extension's namespace. Replacing a
            card with the same key overwrites in place.
        slug : str
            Short headline (≤24 chars; host truncates rune-safely).
        detail : str, optional
            Pre-rendered plain text, expanded by ``observe_session
            --ext <name>``.

        Source and entry_id are stamped by the host — extensions
        cannot impersonate other sources or fake an entry_id, and
        cannot READ other extensions' cards in v1.
        """
        self._call("put_observable", {"key": key, "slug": slug, "detail": detail})

    def clear_observable(self, key: str) -> None:
        """Remove a card previously published by this extension.

        Cannot clear other extensions' cards.
        """
        self._call("clear_observable", {"key": key})

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

    def get_session_file(self) -> str:
        """Return the absolute path to the session's JSONL transcript on disk.

        Returns an empty string for in-memory (non-persisted) sessions.

        The transcript is created at session start and appended to as events
        occur, so it can be ``tail -F``'d from byte 0 to follow the session
        live without missing the first turn. This is the foundation for
        ``fir observe``: the observation extension announces this path
        (via a sidecar file) so external observers can read the transcript
        directly without any further IPC into fir.
        """
        result = self._call("get_session_file", {})
        if isinstance(result, dict):
            value = result.get("path", "")
            if isinstance(value, str):
                return value
        return ""

    def get_session_name(self) -> str:
        """Return the session's display name, or ``""`` if unset.

        Set by the user (via ``/name`` or the ``set_session_name`` API) or
        emitted as a ``session_named`` event when fir auto-names a session.
        """
        result = self._call("get_session_name", {})
        if isinstance(result, dict):
            value = result.get("name", "")
            if isinstance(value, str):
                return value
        return ""

    def get_session_id(self) -> str:
        """Return the unique session ID.

        Also available as ``session_id`` in the ``session_start`` event params,
        but this method allows retrieval at any point during the session.
        """
        result = self._call("get_session_id", {})
        if isinstance(result, dict):
            value = result.get("id", "")
            if isinstance(value, str):
                return value
        return ""

    def set_label(self, entry_id: str, label: str) -> None:
        """Set a label on a session entry."""
        self._call("set_label", {"entry_id": entry_id, "label": label})

    def clear_label(self, entry_id: str) -> None:
        """Clear a label from a session entry."""
        self._call("clear_label", {"entry_id": entry_id})

    def set_model(self, provider: str, model_id: str) -> bool:
        """Change the current model. Returns True on success."""
        result = self._call("set_model", {"provider": provider, "id": model_id})
        if isinstance(result, dict):
            return bool(result.get("ok"))
        return False

    def continue_session(self) -> None:
        """Trigger the agent to continue without injecting any message."""
        self._call("continue_session", timeout=60.0)

    def restart_session(self, prompt: str, prepend_context: str = "") -> None:
        """Abort the in-flight stream and start a fresh session.

        Aborts any current LLM stream synchronously, clears the session
        (LLM history, plan, system-prompt rebuild), clears UI state,
        optionally injects ``prepend_context`` into the fresh session as
        a ``[SYS_EXT]``-wrapped user message, and submits ``prompt`` as
        the first user-typed message of the new session.

        This is the primitive behind the ``self_handoff`` tool. It is
        only supported in modes that register a restart callback
        (interactive). In other modes the call returns a JSON-RPC error.

        Note: when called from inside a tool handler, the tool's result
        will be discarded — the calling turn is being aborted. The new
        session begins with ``prepend_context`` (if any, marked
        ``[SYS_EXT]``) followed by ``prompt``.

        Parameters
        ----------
        prompt : str
            The first user-typed message of the fresh session. Typically
            a short instruction.
        prepend_context : str, optional
            Briefing content injected ahead of ``prompt`` as a
            ``[SYS_EXT]``-wrapped user message via
            :py:meth:`AgentSession.PrependContext`. Empty string (the
            default) skips injection.
        """
        params: dict[str, Any] = {"prompt": prompt}
        if prepend_context:
            params["prepend_context"] = prepend_context
        self._call("restart_session", params)

    def side_query(
        self,
        question: str,
        timeout: float = 120.0,
        model: str | None = None,
        provider: str | None = None,
        effort: str | None = None,
    ) -> str:
        """Ask a side question using the current session context.

        Makes a one-shot LLM call with no tools and no history persistence.
        Returns the full response text. Blocks until the response is complete.

        Optional overrides:
          model    Model id to route the side query to (e.g. ``"claude-opus-4-x"``).
                   When omitted, the agent's current model is used.
          provider Provider id (e.g. ``"anthropic"``). Optional even when
                   ``model`` is set; only required to disambiguate when the
                   same model id is registered under multiple providers.
          effort   Reasoning effort override. One of ``"off"``, ``"minimal"``,
                   ``"low"``, ``"medium"``, ``"high"``, ``"xhigh"``, ``"max"``.

        These are used by the ``aside`` extension to implement the
        "advisor" pattern — escalating a side query to a stronger model.
        """
        params: dict[str, Any] = {"question": question}
        if model:
            params["model"] = model
        if provider:
            params["provider"] = provider
        if effort:
            params["effort"] = effort
        result = self._call("side_query", params, timeout=timeout)
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
    ) -> ToolResult:
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
        ToolResult
            ``{"content": [...], "is_error": bool}``. On RPC-level errors
            an entry with ``is_error=True`` and a text content block is
            returned.
        """
        result = self._call(
            "call_tool",
            {"name": name, "params": params or {}},
            timeout=timeout,
        )
        if isinstance(result, dict):
            return result  # type: ignore[return-value]
        return {"content": [{"type": "text", "text": str(result)}], "is_error": False}

    def list_tools(self, timeout: float = 10.0) -> list[ListToolsItem]:
        """Return info about all registered tools.

        Returns
        -------
        list of ListToolsItem
            Each item has ``name`` (str), ``description`` (str, optional),
            and ``parameters`` (dict, optional — JSON Schema).
        """
        result = self._call("list_tools", {}, timeout=timeout)
        if isinstance(result, list):
            return result  # type: ignore[return-value]
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

    def agent_info(self, timeout: float = 5.0) -> dict[str, Any]:
        """Return a structured snapshot of the current agent runtime.

        Fields include version, mode, session, model, context usage,
        thinking level, message counts, token totals, and cost.

        Returns a free-form dict — fir's :class:`session.Introspection`
        evolves over time, so we keep this loosely typed by design.
        """
        result = self._call("agent.info", {}, timeout=timeout)
        if isinstance(result, dict):
            return result
        return {}


class AuthContext(Context):
    """Extended context for auth provider handlers with OAuth helper methods.

    Inherits all standard Context methods and adds auth-specific helpers
    that call back into fir's OAuth infrastructure.
    """

    def generate_pkce(self, timeout: float = 10.0) -> PKCEResult:
        """Generate a PKCE code verifier and challenge.

        Returns
        -------
        PKCEResult
            ``{"verifier": "...", "challenge": "..."}``
        """
        return self._call("auth/generate_pkce", {}, timeout=timeout)

    def start_callback_server(
        self,
        addr: str = "127.0.0.1:0",
        path: str = "/callback",
        state: str = "",
        timeout: float = 10.0,
    ) -> CallbackServerResult:
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
        CallbackServerResult
            ``{"addr": "127.0.0.1:NNNNN", "redirect_uri": "http://localhost:NNNNN/callback"}``
        """
        return self._call(
            "auth/start_callback_server",
            {"addr": addr, "path": path, "state": state},
            timeout=timeout,
        )

    def await_callback(self, timeout: float = 300.0) -> CallbackResult:
        """Block until the callback server receives an auth code.

        Returns
        -------
        CallbackResult
            ``{"code": "...", "state": "..."}``
        """
        return self._call("auth/await_callback", {}, timeout=timeout)

    def stop_callback_server(self, timeout: float = 10.0) -> None:
        """Stop the local callback server."""
        self._call("auth/stop_callback_server", {}, timeout=timeout)

    def open_url(self, url: str, short_url: str = "", instructions: str = "") -> None:
        """Ask fir to open a URL in the user's browser and/or display it.

        ``short_url`` is an optional pre-shortened form of ``url`` (e.g. via a
        public URL shortener that forwards click-time query params). Modes
        typically present it prominently with ``url`` as a fallback. Pass an
        empty string when no short form is available.
        """
        self._call(
            "auth/open_url",
            {"url": url, "short_url": short_url, "instructions": instructions},
        )

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
# CLI verb host — extension → fir stdio for `fir <verb>` invocations
# ---------------------------------------------------------------------------


class Host:
    """Drives fir's real TTY during a CLI verb invocation.

    A verb handler's stdio is owned by the JSON-RPC bridge, so any output
    the verb wants the user to see has to flow through fir. ``Host`` is
    the thin wrapper around the necessary notifications and stdin queue.

    Methods:
      print/println       write to fir's stdout (no flush needed; each call
                          is one notification)
      eprint/eprintln     write to fir's stderr
      readline            block until fir delivers the next stdin line, or
                          return None on EOF
      stdin_lines         iterator over readline() until EOF — convenient
                          for ``for line in host.stdin_lines(): ...``
      wake                synthesise EOF locally so a blocked readline
                          returns immediately (e.g. from a cli_signal
                          handler that wants the verb to exit promptly)
      argv / cwd          per-invocation context, set before the handler runs
      stdout_is_tty / stderr_is_tty / stdin_is_tty
                          tty-ness flags reported by fir at invoke time

    """

    def __init__(self, out: WriteStream) -> None:
        self._out = out
        self._stdin_q: list[str] = []
        self._stdin_eof = False
        self._stdin_cv = threading.Condition()
        self.argv: list[str] = []
        self.cwd: str = ""
        self.stdin_is_tty: bool = False
        self.stdout_is_tty: bool = False
        self.stderr_is_tty: bool = False

    # -- output -------------------------------------------------------------

    def print(self, *args: Any, sep: str = " ", end: str = "") -> None:
        """Write to fir's real stdout. No newline appended unless given."""
        text = sep.join(str(a) for a in args) + end
        if text:
            _write_message(
                {"jsonrpc": "2.0", "method": "cli_stdout", "params": {"data": text}},
                self._out,
            )

    def println(self, *args: Any, sep: str = " ") -> None:
        """Like ``print`` but appends a newline."""
        self.print(*args, sep=sep, end="\n")

    def eprint(self, *args: Any, sep: str = " ", end: str = "") -> None:
        """Write to fir's real stderr."""
        text = sep.join(str(a) for a in args) + end
        if text:
            _write_message(
                {"jsonrpc": "2.0", "method": "cli_stderr", "params": {"data": text}},
                self._out,
            )

    def eprintln(self, *args: Any, sep: str = " ") -> None:
        """Like ``eprint`` but appends a newline."""
        self.eprint(*args, sep=sep, end="\n")

    # -- input --------------------------------------------------------------

    def _push_stdin(self, data: str | None) -> None:
        """Internal: enqueue a line from fir, or signal EOF when data is None."""
        with self._stdin_cv:
            if data is None:
                self._stdin_eof = True
            else:
                self._stdin_q.append(data)
            self._stdin_cv.notify_all()

    def wake(self) -> None:
        """Wake any pending ``readline`` immediately by pushing EOF.

        Useful from a ``cli_signal`` handler that wants the verb's blocking
        ``readline(timeout=...)`` to return promptly so the verb can exit
        cleanly (e.g. restore alt-screen / cursor) instead of waiting out
        the rest of its poll interval. After ``wake()`` further reads
        return ``None``; do not call this if the verb intends to keep
        reading user input.
        """
        self._push_stdin(None)

    def readline(self, timeout: float | None = None) -> str | None:
        """Read one line from fir's stdin (already terminated by ``\\n``).

        Returns ``None`` at EOF. ``timeout`` (seconds) limits the wait;
        on timeout returns ``None`` even if more data may arrive later.
        """
        with self._stdin_cv:
            if not self._stdin_q and not self._stdin_eof:
                self._stdin_cv.wait(timeout=timeout)
            if self._stdin_q:
                return self._stdin_q.pop(0)
            return None

    def stdin_lines(self):
        """Yield each stdin line until EOF. Convenience for handlers that
        consume the entire stream."""
        while True:
            line = self.readline()
            if line is None:
                return
            yield line


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
    global ext_name
    ext_name = name or "python-ext"
    inp = input_stream or sys.stdin
    out = output_stream or sys.stdout

    # Pending outbound requests (extension→fir)
    pending: dict[int, threading.Event] = {}
    results: dict[int, Any] = {}

    ctx = Context(output_stream=out, pending=pending, results=results)
    _cli_host = Host(out=out)

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
            # Surface tool_call_id on ctx for the handler's lifetime
            # (cleared in finally so an exception doesn't leak it).
            prev_tool_call_id = ctx.tool_call_id
            ctx.tool_call_id = params.get("tool_call_id", "") or ""
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
            finally:
                ctx.tool_call_id = prev_tool_call_id
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

            elif method == "auth/post_exchange":
                handler = _auth_post_exchange_handlers.get(provider_id)
                if handler is None:
                    _write_message(
                        _make_error(
                            msg_id,
                            -32601,
                            f"No post_exchange handler for provider: {provider_id}",
                        ),
                        out_stream,
                    )
                    return
                result = handler(params, auth_ctx)
                if isinstance(result, dict) and "access" in result:
                    result = {"credentials": result}
                _write_message(_make_response(msg_id, result), out_stream)

            else:
                _write_message(
                    _make_error(msg_id, -32601, f"Unknown auth method: {method}"), out_stream
                )

        except Exception as exc:
            _write_message(_make_error(msg_id, -32000, str(exc)), out_stream)

    def _send_provider_event(stream_id: str, event: dict[str, Any]) -> None:
        _write_message(
            {
                "jsonrpc": "2.0",
                "method": "provider.stream.event",
                "params": {"stream_id": stream_id, "event": event},
            },
            out,
        )

    def _run_provider_stream(
        provider_id: str, stream_id: str, params: dict[str, Any],
        cancel: threading.Event,
    ) -> None:
        """Drive a @provider_stream generator and forward its events.

        ``cancel`` is pre-registered in ``_stream_cancels`` by the caller so
        a `provider/stream/cancel` arriving before this worker starts is
        not lost. We own its removal in the finally block.
        """
        handler = _provider_stream_handlers.get(provider_id)
        if handler is None:
            _send_provider_event(stream_id, {
                "type": "error",
                "reason": "error",
                "error": {
                    "role": "assistant",
                    "stopReason": "error",
                    "errorMessage": f"no provider_stream handler for {provider_id!r}",
                },
            })
            with _stream_cancels_lock:
                _stream_cancels.pop(stream_id, None)
            return
        terminal_seen = False
        gen = None
        try:
            gen = handler(params, ctx)
            if gen is None:
                return
            for event in gen:
                if not isinstance(event, dict):
                    continue
                ev_type = event.get("type", "")
                if ev_type in ("done", "error"):
                    terminal_seen = True
                _send_provider_event(stream_id, event)
                if cancel.is_set() and not terminal_seen:
                    # Generator hasn't noticed cancellation yet — synthesise
                    # a terminal error and stop iterating so we don't leak.
                    break
        except Exception as exc:
            _send_provider_event(stream_id, {
                "type": "error",
                "reason": "error",
                "error": {
                    "role": "assistant",
                    "stopReason": "error",
                    "errorMessage": f"{type(exc).__name__}: {exc}",
                },
            })
            terminal_seen = True
        finally:
            # Close the generator so its own try/finally runs even when we
            # bail out early (cancellation or exception).
            if gen is not None and hasattr(gen, "close"):
                import contextlib

                with contextlib.suppress(Exception):
                    gen.close()
            if not terminal_seen:
                _send_provider_event(stream_id, {
                    "type": "error",
                    "reason": "aborted" if cancel.is_set() else "error",
                    "error": {
                        "role": "assistant",
                        "stopReason": "aborted" if cancel.is_set() else "error",
                        "errorMessage": (
                            "stream cancelled" if cancel.is_set()
                            else "provider generator exited without terminal event"
                        ),
                    },
                })
            with _stream_cancels_lock:
                _stream_cancels.pop(stream_id, None)

    def _handle_provider_request(
        method: str, msg_id: Any, params: dict[str, Any]
    ) -> None:
        """Dispatch a provider/* RPC from fir."""
        try:
            if method == "provider/stream/start":
                provider_id = params.get("provider_id", "")
                stream_id = params.get("stream_id", "")
                if not stream_id:
                    _write_message(
                        _make_error(msg_id, -32602, "missing stream_id"), out
                    )
                    return
                # Pre-register the cancel event before spawning the worker
                # so a fast provider/stream/cancel can't be dropped on the
                # floor while the worker is still starting up.
                cancel = threading.Event()
                with _stream_cancels_lock:
                    _stream_cancels[stream_id] = cancel
                # Ack immediately so the host doesn't bound latency on the
                # generator's first yield. Events flow asynchronously via
                # provider.stream.event notifications.
                _write_message(_make_response(msg_id, {}), out)
                worker = threading.Thread(
                    target=_run_provider_stream,
                    args=(provider_id, stream_id, params, cancel),
                    daemon=True,
                )
                worker.start()
                _track_worker(worker)
                return

            if method == "provider/stream/cancel":
                stream_id = params.get("stream_id", "")
                with _stream_cancels_lock:
                    evt = _stream_cancels.get(stream_id)
                if evt is not None:
                    evt.set()
                _write_message(_make_response(msg_id, {"ok": True}), out)
                return

            if method == "provider/listModels":
                provider_id = params.get("provider_id", "")
                handler = _provider_list_models_handlers.get(provider_id)
                if handler is None:
                    _write_message(
                        _make_error(
                            msg_id, -32601,
                            f"no provider_list_models handler for {provider_id!r}",
                        ),
                        out,
                    )
                    return
                result = handler(params, ctx)
                if isinstance(result, list):
                    result = {"model_ids": list(result)}
                elif not isinstance(result, dict):
                    result = {"model_ids": []}
                _write_message(_make_response(msg_id, result), out)
                return

            if method == "provider/resolveCustomId":
                provider_id = params.get("provider_id", "")
                handler = _provider_resolve_custom_id_handlers.get(provider_id)
                if handler is None:
                    _write_message(_make_response(msg_id, None), out)
                    return
                result = handler(params, ctx)
                _write_message(_make_response(msg_id, result), out)
                return

            _write_message(
                _make_error(msg_id, -32601, f"unknown provider method: {method}"),
                out,
            )
        except Exception as exc:
            _write_message(_make_error(msg_id, -32000, str(exc)), out)

    def _dispatch(msg: dict[str, Any]) -> None:
        method = msg.get("method", "")
        msg_id = msg.get("id")
        params = msg.get("params", {})

        # --- init handshake (always synchronous) ---
        if method == "init":
            global cwd, config_dirs
            cwd = params.get("cwd", "") or ""
            config_dirs = list(params.get("config_dirs") or [])
            init_result: dict[str, Any] = {
                "name": ext_name,
                "tools": list(_tools),
                "commands": list(_commands),
                "events": subscribed_events,
            }
            if _auth_providers:
                _finalise_auth_specs()
                init_result["auth_providers"] = list(_auth_providers)
            if _apis:
                init_result["apis"] = list(_apis)
            if _providers:
                init_result["providers"] = list(_providers)
            if _tool_name_map:
                init_result["tool_name_map"] = dict(_tool_name_map)
            if _cli_verb_handlers:
                init_result["cli_verbs"] = sorted(_cli_verb_handlers.keys())
            resp = _make_response(msg_id, init_result)
            _write_message(resp, out)
            return

        # --- cli_invoke: run a top-level `fir <verb>` handler ---
        if method == "cli_invoke":
            verb = params.get("verb", "")
            handler = _cli_verb_handlers.get(verb)
            if handler is None:
                _write_message(
                    _make_error(msg_id, -32601, f"No handler for cli verb: {verb}"),
                    out,
                )
                return
            host = _cli_host
            host.argv = list(params.get("argv") or [])
            host.cwd = params.get("cwd", "") or ""
            host.stdin_is_tty = bool(params.get("stdin_is_tty"))
            host.stdout_is_tty = bool(params.get("stdout_is_tty"))
            host.stderr_is_tty = bool(params.get("stderr_is_tty"))

            def _run_verb(h=handler, p=params, mid=msg_id):
                try:
                    code = h(host.argv, host)
                    if not isinstance(code, int):
                        code = 0
                except SystemExit as exc:
                    if isinstance(exc.code, int):
                        code = exc.code
                    elif exc.code is None:
                        code = 0
                    else:
                        code = 1
                except Exception:
                    import traceback
                    traceback.print_exc(file=sys.stderr)
                    code = 1
                _write_message(_make_response(mid, {"exit_code": code}), out)

            t = threading.Thread(target=_run_verb, daemon=True)
            t.start()
            _track_worker(t)
            return

        # --- cli_stdin: forwarded stdin from fir's TTY ---
        if method == "cli_stdin":
            if params.get("eof"):
                _cli_host._push_stdin(None)
            else:
                data = params.get("data", "")
                if isinstance(data, str):
                    _cli_host._push_stdin(data)
            return

        # --- cli_signal: forwarded signal during a verb invocation ---
        if method == "cli_signal":
            sig = params.get("name", "")

            def _run_sig_handlers(name=sig):
                # Snapshot via tuple so concurrent registration mutations
                # during signal storms don't trip iteration.
                handlers = tuple(_cli_signal_handlers)
                for h in handlers:
                    try:
                        h(name, _cli_host)
                    except Exception:  # noqa: PERF203 — per-handler isolation
                        import traceback
                        traceback.print_exc(file=sys.stderr)

            threading.Thread(target=_run_sig_handlers, daemon=True).start()
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

        # --- provider/* RPCs: hosted-provider streaming + listing ---
        if method.startswith("provider/"):
            t = threading.Thread(
                target=_handle_provider_request, args=(method, msg_id, params), daemon=True
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
