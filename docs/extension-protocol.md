# Extension Wire Protocol

This document is the authoritative specification for the fir external-process
extension protocol.  It is intended for:

- Authors implementing an extension SDK in a language other than Python.
- Contributors working on the Go extension host (`pkg/extension/`).
- Anyone who needs to understand exactly what bytes cross the stdio pipes.

For a user-facing overview, quickstart, and discovery rules see
[extensions.md](extensions.md).  For a Python SDK reference see the module
docstring of `pkg/extension/sdk/python/fir_ext.py`.  For a complete working
example see `pkg/resources/builtin_extensions/demo.py`.

The wire shapes documented below are also expressed as concrete typed
structures in code:

- **Go side** – `pkg/extension/types.go` declares one struct per JSON-RPC
  method param/result and per event payload (e.g. `OkResult`,
  `SideQueryResult`, `MessageEndPayload`, `ToolExecutionEndPayload`,
  `SessionStartPayload`).  The bridge unmarshals inbound params into these
  types and marshals typed results back out, instead of building ad-hoc
  `map[string]any` literals.
- **Python SDK** – `fir_ext` re-exports a `TypedDict` for every shape
  documented here (e.g. `fir_ext.ToolResult`, `fir_ext.MessageEndParams`,
  `fir_ext.ToolCallHookParams`).  At runtime they are plain `dict`s, so
  existing extensions keep working unchanged; annotating handlers with
  these types gives full IDE/type-checker support.

---

## Transport

```
┌──────────┐  stdin (JSON-RPC)  ┌───────────────┐
│          │ ─────────────────► │               │
│   fir    │                    │   extension   │
│          │ ◄───────────────── │   process     │
└──────────┘  stdout (JSON-RPC) └───────────────┘
                                  stderr → fir log
```

- **Encoding** – UTF-8.
- **Framing** – one JSON-RPC 2.0 object per line, terminated by `\n`.  No
  Content-Length header.  No batching.
- **Direction** – fir writes to the extension's **stdin**; the extension writes
  to its **stdout**.  The extension's **stderr** is captured line-by-line and
  forwarded to fir's structured log at INFO level under the key `extension
  stderr`.

---

## Message Types

Three standard JSON-RPC 2.0 message shapes are used.

### Request (requires a Response)

```json
{"jsonrpc":"2.0","id":1,"method":"init","params":{}}
```

| Field | Type | Description |
|-------|------|-------------|
| `jsonrpc` | `"2.0"` | Always the literal string `"2.0"`. |
| `id` | integer | Unique request ID.  The response must echo this value. |
| `method` | string | Method name. |
| `params` | object | Method parameters. May be omitted for parameter-less methods. |

### Response

```json
{"jsonrpc":"2.0","id":1,"result":{...}}
```

or, on error:

```json
{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"something went wrong"}}
```

The `id` must match the corresponding Request.

### Notification (no Response expected)

```json
{"jsonrpc":"2.0","method":"event/session_start","params":{}}
```

No `id` field.  The receiver must not send a response.

---

## Init Handshake

Immediately after spawning the process, fir sends an `init` **request**.  The
extension must respond within **5 seconds** or fir kills the process and marks
it as failed.

### fir → extension

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "init",
  "params": {
    "version": "1",
    "cwd": "/path/to/project",
    "config_dirs": ["/path/to/project/.fir", "/home/user/.config/fir"]
  }
}
```

| Param | Description |
|-------|-------------|
| `version` | Protocol version string.  Currently `"1"`. |
| `cwd` | Project working directory. |
| `config_dirs` | Priority-ordered list of directories the extension may use to read/write its per-extension config file (highest priority first). Typically `[projectDir/.fir, ~/.config/fir]`. Use the SDK helpers `load_config()` / `config_path()` rather than accessing this directly. |

### extension → fir

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "name": "my-ext",
    "tools": [...],
    "commands": [...],
    "events": [...]
  }
}
```

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Display name shown in fir's UI and logs. |
| `tools` | array | Tool definitions to register (see [Tool Definitions](#tool-definitions)). |
| `commands` | array | Slash-command definitions (see [Commands](#commands)). May be `[]`. |
| `events` | array of strings | Event and hook names to subscribe to.  Bare names for events (e.g. `"session_start"`); `hook/` prefix for hooks (e.g. `"hook/tool_call"`).  May be `[]`. |

### Tool Definitions

Each entry in `tools` is an object:

```json
{
  "name": "count_words",
  "description": "Count the words in a string",
  "parameters": {
    "type": "object",
    "properties": {
      "text": {"type": "string", "description": "Input text"}
    },
    "required": ["text"]
  },
  "display_hint": {
    "title_args": [{"name": "text", "style": "accent"}],
    "result_max_lines": 10,
    "use_box": false
  }
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `name` | ✓ | Unique tool name registered with the LLM. |
| `description` | — | Human/LLM-readable description. |
| `parameters` | — | JSON Schema `object` describing inputs.  Defaults to `{"type":"object","properties":{}}`. |
| `display_hint` | — | TUI rendering hints (see below). |

**`display_hint` fields:**

| Field | Description |
|-------|-------------|
| `title_args` | Array of `{name, style, label?}` objects.  Controls which parameters appear on the collapsed header line.  `style` may be `"path"`, `"pattern"`, `"accent"`, or `""` (plain). |
| `result_max_lines` | Collapsed line count before the output is truncated in the TUI (default 10). |
| `use_box` | Render output in a bordered box (default false). |

### Commands

Each entry in `commands` is:

```json
{"name": "my-cmd", "description": "Do something useful"}
```

---

## Tool Calls  (fir → extension)

When the AI invokes a tool registered by the extension during init, fir sends a
`tool_call` **request**.  Timeout: **30 seconds**.

### Request

```json
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
```

| Param | Description |
|-------|-------------|
| `tool_call_id` | Opaque ID assigned by the LLM for this invocation. |
| `name` | Tool name, matching the `name` from the init result. |
| `params` | Object containing the tool's input arguments. |

### Success Response

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "content": [
      {"type": "text", "text": "2 words"}
    ],
    "is_error": false
  }
}
```

| Field | Description |
|-------|-------------|
| `content` | Array of content blocks.  Each block is `{"type":"text","text":"..."}`.  Only the `text` type is used today. |
| `is_error` | `true` → the result is reported to the LLM as a tool error. |

If `content` is a plain string or non-structured JSON, fir wraps it
automatically into a single text block.

### Error Response

Return a JSON-RPC error to report a structured failure:

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "error": {"code": -32000, "message": "file not found"}
}
```

Standard JSON-RPC error codes: `-32700` parse error, `-32600` invalid request,
`-32601` method not found, `-32602` invalid params, `-32603` internal error.
Application-defined errors use `-32000` through `-32099`.

---

## Hooks  (fir → extension, Requests)

Hooks are interceptor points where fir pauses and asks the extension whether to
proceed.  They are sent as **requests** (they have an `id`).  Hook method names
start with `hook/`.

### hook/tool_call

Fired **before every tool call**, including built-in tools.  Timeout:
**5 seconds**.

```json
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
```

**Allow** (return `null` or an empty object):

```json
{"jsonrpc":"2.0","id":3,"result":null}
```

**Block**:

```json
{"jsonrpc":"2.0","id":3,"result":{"block":true,"reason":"dangerous command"}}
```

If multiple extensions are registered, fir collects all responses; any single
`block: true` blocks the call.

### hook/command

Fired when a user types a slash command that was registered by the extension.
Timeout: **10 seconds**.

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "method": "hook/command",
  "params": {
    "name": "my-cmd",
    "args": ["arg1", "arg2"]
  }
}
```

Response — optional `message` shown in the TUI:

```json
{"jsonrpc":"2.0","id":4,"result":{"message":"Done!"}}
```

Return `null` or `{}` to show nothing.

---

## Events  (fir → extension, Notifications)

Events are JSON-RPC **notifications** — no `id`, no response expected.  The
method name is `event/<event_name>`.

Subscribe by listing the **bare event name** (without the `event/` prefix) in
the `events` array of the init response.  For hooks, use the full `hook/` name.

### Event Reference

| Event name | `params` |
|------------|----------|
| `session_start` | `{"session_id": "...", "session_data": {"key": "value", ...}}` — `session_id` is the unique session identifier (always present, also retrievable any time via `get_session_id`).  `session_data` is the map previously stored via `set_session_data`, seeded from the reexec sidecar on `/reexec`; absent on a fresh session (no prior data). |
| `session_shutdown` | *(params key absent)* — session is about to stop; last chance for cleanup. |
| `session_named` | `{"name": "..."}` — fired when the session acquires a display name (on start if one already exists, or when it is set later). |
| `session_update` | `{"type": "session_named"│"plan_update", "session_name": "...", "plan": {"total": N, "completed": N, "metadata": {...}}}` — generic session state change. |
| `agent_start` | *(params key absent)* — LLM turn is starting. |
| `agent_end` | *(params key absent)* — LLM turn has finished. |
| `turn_start` | *(params key absent)* — streaming turn is starting. |
| `turn_end` | *(params key absent)* — streaming turn has finished. |
| `message_start` | *(params key absent)* — LLM message block is starting. |
| `message_end` | `{role, provider?, model?, stop_reason?, response_id?, usage?}` — LLM message block has finished. `role` is `"user"`, `"assistant"`, or `"toolResult"`. Assistant messages also carry `provider`, `model`, `stop_reason`, `response_id`, and a `usage` object: `{input, output, cache_read, cache_write, total_tokens, cost: {input, output, cache_read, cache_write, total}}`. Token counts are integers; cost values are USD floats from the upstream provider (zero when unavailable). |
| `tool_execution_start` | `{"tool_call_id": "...", "tool_name": "..."}` |
| `tool_execution_end` | `{"tool_call_id": "...", "tool_name": "...", "is_error": false}` |

---

## Extension → fir Calls

Extensions may call back into fir **at any time** by writing a JSON-RPC
**request** to stdout.  fir responds on stdin.

The default client-side timeout in the Python SDK is **10 seconds** unless
noted; this is only the SDK's wait; fir itself does not enforce an inbound
timeout on these calls.

### Method Reference

#### `notify`

Show a notification in the fir UI.

```json
{"jsonrpc":"2.0","id":1001,"method":"notify","params":{"message":"hello","level":"info"}}
```

`level` values: `"info"` | `"warning"` | `"error"`.

Response: `{"ok": true}`

---

#### `exec`

Run a subprocess.  fir spawns the command and returns its output.

```json
{"jsonrpc":"2.0","id":1002,"method":"exec","params":{"command":"wc","args":["-l","README.md"]}}
```

Response:

```json
{"stdout":"42 README.md\n","stderr":"","exit_code":0}
```

A non-zero `exit_code` is **not** an RPC error — the result is still returned
in `result`.  An RPC error is only returned if the command cannot be started.

---

#### `send_message`

Inject a custom-typed message into the session log.

```json
{
  "jsonrpc": "2.0",
  "id": 1003,
  "method": "send_message",
  "params": {
    "custom_type": "my_note",
    "content": {"anything": "goes here"},
    "display": true,
    "deliver_as": "steer",
    "trigger_turn": false
  }
}
```

| Param | Description |
|-------|-------------|
| `custom_type` | Arbitrary type tag used by the session renderer. |
| `content` | Any JSON-serialisable value. |
| `display` | When `true`, the message is shown in the TUI. |
| `deliver_as` | `"steer"` — inject as a steering message; `"followUp"` — queue for next turn.  Omit to append to the log only. |
| `trigger_turn` | When `true`, fir starts a new agent turn after injecting. |

Response: `{"ok": true}`

---

#### `send_user_message`

Inject a user-role message into the session.

```json
{"jsonrpc":"2.0","id":1004,"method":"send_user_message","params":{"content":"Hello","deliver_as":"steer"}}
```

`deliver_as`: `"steer"` | `"followUp"` | omit (default: triggers a new prompt
turn).

Response: `{"ok": true}`

---

#### `set_session_name`

Set the session's display name.

```json
{"jsonrpc":"2.0","id":1005,"method":"set_session_name","params":{"name":"my-session"}}
```

Response: `{"ok": true}`

---

#### `set_label` / `clear_label`

Tag or untag a session entry (e.g., a tool call by its `tool_call_id`).

```json
{"jsonrpc":"2.0","id":1006,"method":"set_label","params":{"entry_id":"toolu_abc123","label":"running:bash"}}
{"jsonrpc":"2.0","id":1007,"method":"clear_label","params":{"entry_id":"toolu_abc123"}}
```

Response: `{"ok": true}`

---

#### `get_active_tools`

Return the names of currently active tools.

```json
{"jsonrpc":"2.0","id":1008,"method":"get_active_tools"}
```

Response: `["Read", "Bash", "my_tool", ...]`

---

#### `set_active_tools`

Replace the active tool set with the given list.  Tools not in `names` are
deactivated for the current session.

```json
{"jsonrpc":"2.0","id":1009,"method":"set_active_tools","params":{"names":["Read","Bash"]}}
```

Response: `{"ok": true}`

---

#### `set_model`

Switch to a different model.

```json
{"jsonrpc":"2.0","id":1010,"method":"set_model","params":{"provider":"anthropic","id":"claude-opus-4-5"}}
```

Response: `{"ok": true}` on success, `{"ok": false}` if the provider has no
API key configured.

---

#### `set_status`

Set persistent status text in the UI footer.  Pass an empty string to clear.

```json
{"jsonrpc":"2.0","id":1011,"method":"set_status","params":{"status":"my-ext: ready"}}
```

Response: `{"ok": true}`

---

#### `continue_session`

Trigger a new agent turn without injecting any message.  SDK timeout: 60 s.

```json
{"jsonrpc":"2.0","id":1012,"method":"continue_session"}
```

Response: `{"ok": true}`

---

#### `restart_session`

Abort the in-flight stream and start a fresh session, with `prompt` as
the first user message of the new context. Used by the `self_handoff`
builtin extension to implement reliable session handoff.

| Param    | Type   | Notes                                                                  |
|----------|--------|-------------------------------------------------------------------------|
| `prompt` | string | Required. First user message of the fresh session (e.g. "Read /tmp/handoff.md and continue."). |

```json
{"jsonrpc":"2.0","id":1013,"method":"restart_session","params":{"prompt":"Read /tmp/handoff.md and continue."}}
```

Behaviour: fir calls `Agent.Abort()` synchronously (so the calling tool's
result writeback is short-circuited), then asynchronously waits for idle,
clears UI, calls `NewSessionCmd()`, and submits `prompt` via `Prompt()`.
This RPC is the primitive behind the `handoff` builtin extension's
`self_handoff` tool.

Response: `{"ok": true}` — but extensions should not rely on receiving it;
the calling agent turn is being torn down.

Returns a JSON-RPC error when the active mode does not register a restart
callback (e.g. ACP, headless). Currently only the interactive (TUI) mode
supports restart.

---

#### `side_query`

Make a one-shot LLM call using the current session context.  No tools, no
history persistence.  Blocks until the response is complete.  SDK timeout:
120 s.

Optional params override the agent's current model/effort for this single
call only — used by the `aside` extension to implement the "advisor"
pattern (escalating to a stronger model when stuck):

| Param      | Type   | Notes                                                              |
|------------|--------|--------------------------------------------------------------------|
| `question` | string | Required. The side question.                                        |
| `model`    | string | Optional. Model id (e.g. `claude-opus-4-x`).                       |
| `provider` | string | Optional. Provider id (e.g. `anthropic`); needed only to disambiguate. |
| `effort`   | string | Optional. Reasoning level: `off`/`minimal`/`low`/`medium`/`high`/`xhigh`/`max`. |

```json
{"jsonrpc":"2.0","id":1013,"method":"side_query","params":{"question":"Summarise this in one sentence."}}
{"jsonrpc":"2.0","id":1014,"method":"side_query","params":{"question":"Should I refactor this?","model":"claude-opus-4-x","effort":"high"}}
```

Response: `{"ok": true, "text": "A one-sentence summary."}`

---

#### `set_session_data` / `get_session_data`

Store or retrieve a string key/value pair in this extension's private session
data store.  Values survive `/reexec` (they are serialised into the reexec
sidecar and handed back in the `session_start` event params under
`session_data`).

```json
{"jsonrpc":"2.0","id":1014,"method":"set_session_data","params":{"key":"counter","value":"42"}}
{"jsonrpc":"2.0","id":1015,"method":"get_session_data","params":{"key":"counter"}}
```

`set_session_data` response: `{"ok": true}`

`get_session_data` response: `{"value": "42", "ok": true}` or `{"value": "", "ok": false}` when absent.

The data store is **per-extension** — two extensions cannot read each other's
keys.

---

#### `get_session_file`

Return the absolute path to the session's JSONL transcript on disk, or `""`
for in-memory (non-persisted) sessions.

```json
{"jsonrpc":"2.0","id":1016,"method":"get_session_file","params":{}}
```

Response: `{"path": "/Users/x/.fir/sessions/--Users-x-dev-foo--/2026-04-27T12-34-56Z_abc.jsonl"}`

The transcript file is created at session start (with its `SessionHeader`
line) and appended to as events occur, so external observers can `tail -F`
it from byte 0 without missing the first turn. This is the foundation of
the `fir observe` feature — the observation extension announces this path
in a sidecar file so observers read the transcript directly without any
further IPC into fir.

---

#### `get_session_name`

Return the session's display name, or `""` if unset. Set by the user (via
`/name` or `set_session_name`) or auto-emitted as a `session_named` event.

```json
{"jsonrpc":"2.0","id":1017,"method":"get_session_name","params":{}}
```

Response: `{"name": "my-feature"}`

---

#### `get_session_id`

Return the unique session identifier.  Also delivered in the
`session_start` event params under the `session_id` key — this method
allows retrieval at any point during the session lifetime.

```json
{"jsonrpc":"2.0","id":1018,"method":"get_session_id","params":{}}
```

Response: `{"id": "abc12345-6789-..."}`

---

#### `call_tool`

Execute any registered tool by name.  The call bypasses conversation history;
it is ephemeral.  SDK timeout: 60 s.

```json
{
  "jsonrpc": "2.0",
  "id": 1016,
  "method": "call_tool",
  "params": {
    "name": "Read",
    "params": {"path": "README.md", "limit": 20}
  }
}
```

Response:

```json
{
  "content": [{"type": "text", "text": "# My Project\n..."}],
  "is_error": false
}
```

On RPC-level error (tool not found, etc.) fir returns a JSON-RPC error rather
than setting `is_error`.

---

#### `list_tools`

Return name and parameter schema for all currently registered tools (built-in,
extension, and MCP).

```json
{"jsonrpc":"2.0","id":1017,"method":"list_tools","params":{}}
```

Response:

```json
[
  {"name": "Read", "description": "Read a file", "parameters": {...}},
  {"name": "Bash", "description": "Run a bash command", "parameters": {...}},
  ...
]
```

---

#### `prepend_context`

Add a `[SYS_EXT]` block to the session's system prompt.  The content is
injected as an authoritative extension of the system prompt and is visible to
the LLM on every turn.

```json
{"jsonrpc":"2.0","id":1018,"method":"prepend_context","params":{"content":"Project language: Go."}}
```

Response: `{"ok": true}`

#### `report_progress`

Send a transient progress message to the UI.  Updates the spinner text inside
the tool's display component (e.g. "Calling Read..." or "Synthesizing...").
Only meaningful while an extension tool is executing; ignored otherwise.

```json
{"jsonrpc":"2.0","id":1019,"method":"report_progress","params":{"message":"Calling Read..."}}
```

Response: `{"ok": true}`

#### `agent.info`

Return a structured snapshot of the current agent runtime. Served per-session
— the snapshot reflects only the session the calling extension is bound to.

Params: none (pass `{}`).

Response fields (shape is stable; unknown values use zero-equivalent defaults;
`context.tokens`/`percent` use `-1` to mean genuinely unknown, e.g. right
after compaction):

```json
{
  "version": "0.x.y",
  "mode": "interactive | acp | print",
  "cwd": "/abs/path",
  "session": {"id": "...", "file": "...", "name": "..."},
  "model":   {"id": "...", "provider": "...", "contextWindow": 0},
  "context": {"tokens": 0, "window": 0, "percent": 0.0, "compactMode": "off|client|server"},
  "thinking": {"current": "...", "available": ["..."]},
  "messages": {"user": 0, "assistant": 0, "toolCalls": 0, "toolResults": 0, "total": 0},
  "tokens":   {"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0},
  "cost": 0.0
}
```

```json
{"jsonrpc":"2.0","id":1020,"method":"agent.info","params":{}}
```

---

## Comment Frontmatter

Extension files may declare metadata in a comment frontmatter block placed
directly after an optional shebang line:

```python
#!/usr/bin/env python3
# ---
# name: my-ext
# modes: tui, acp
# ---
```

fir reads this block **before** starting the process and uses it for:

- Filtering by mode (`modes` key).
- Displaying the extension in listings.

The actual capability set (tools, commands, events the extension subscribes
to) is reported by the extension during the init handshake — there is no
parallel frontmatter declaration.  All extensions start eagerly in parallel.

### Frontmatter Keys

| Key | Description |
|-----|-------------|
| `name` | Override the filename-derived extension name. |
| `description` | One-line summary shown in listings. |
| `builtin` | When `true`, marks a builtin extension shipped with fir. |
| `explicit` | When `true`, the extension is **opt-in**: it is discovered (so `fir -e <name>` and listings find it), but is **not** auto-loaded. Use for example/demo extensions that should only run when the user asks for them by name. |
| `modes` | Comma-separated list of fir modes in which this extension should run.  Values: `tui` (alias `interactive`), `text`, `json`, `rpc`, `acp`.  Omit to run in all modes. |
| `auth_providers` | Comma-separated list of auth provider IDs registered by the extension (used by the auth-only manager to discover provider extensions before full startup). |
| `cli_verbs` | Comma-separated list of top-level `fir <verb>` names this extension claims. See [CLI Verbs](#cli-verbs) below. |

---

## CLI Verbs

Extensions can register top-level CLI verbs via the `cli_verbs:` frontmatter
key, so users can invoke extension functionality as `fir <verb> [args...]`
without going through a chat session. Design notes live in
`docs/design/extension-cli-verbs.md`.

### Frontmatter declaration

```python
# ---
# name: my-ext
# cli_verbs: greet, summarise
# ---
```

Frontmatter is read at startup without spawning the extension, so verb
lookup is free.

### Dispatch flow

1. `fir <verb> [args...]` matches a registered verb (built-in subcommands
   take precedence — collisions abort startup).
2. fir spawns the extension, performs the standard `init` handshake, and
   sends a `cli_invoke` request.
3. The extension's stdio is owned by the JSON-RPC bridge, so output flows
   through `cli_stdout` / `cli_stderr` notifications and input arrives via
   `cli_stdin` notifications. Signals received by fir are forwarded as
   `cli_signal` notifications.
4. The `cli_invoke` response carries `exit_code`, which becomes fir's exit
   code.

### Wire methods

#### cli_invoke (fir → ext, Request)

```json
{
  "jsonrpc": "2.0", "id": 2, "method": "cli_invoke",
  "params": {
    "verb": "greet",
    "argv": ["world"],
    "cwd": "/Users/x/proj",
    "stdin_is_tty":  true,
    "stdout_is_tty": true,
    "stderr_is_tty": true
  }
}
```

Response:

```json
{"jsonrpc":"2.0","id":2,"result":{"exit_code":0}}
```

#### cli_stdout / cli_stderr (ext → fir, Notification)

```json
{"jsonrpc":"2.0","method":"cli_stdout","params":{"data":"hello\n"}}
```

`data` is written verbatim to fir's stdout (or stderr). ANSI sequences,
trailing newlines, and partial lines all pass through unchanged.

#### cli_stdin (fir → ext, Notification)

```json
{"jsonrpc":"2.0","method":"cli_stdin","params":{"data":"line1\n"}}
{"jsonrpc":"2.0","method":"cli_stdin","params":{"eof":true}}
```

Lines are forwarded as fir reads from its real stdin. After EOF, no further
`cli_stdin` notifications arrive.

#### cli_signal (fir → ext, Notification)

```json
{"jsonrpc":"2.0","method":"cli_signal","params":{"name":"interrupt"}}
```

Forwarded for `SIGINT`, `SIGTERM`, `SIGQUIT`, `SIGHUP`, `SIGWINCH`. The
extension is expected to wind down (or terminate via `os._exit`).

### Constraints

- Verb dispatch runs **cold** — no Manager, no session. Bridge methods that
  need a live session (`send_user_message`, `set_label`, etc.) return
  `method-not-found`. Verbs that need filesystem state should compute paths
  from `$XDG_*` like the observe extension does.
- Two extensions claiming the same verb is a fatal startup error. Built-in
  fir subcommands cannot be shadowed.
- One JSON-RPC notification per output write — fine for line-oriented
  output (which is what every shipped verb does), inappropriate for
  high-frequency raw-mode TUIs.

---

## Discovery & Trust

### Search Order (highest → lowest precedence)

1. **Project-local** — `.fir/extensions/` in the current project.  Requires
   explicit trust (see below).
2. **User-global** — `~/.config/fir/extensions/`.  Always trusted.
3. **Package extras** — directories or individual files supplied by installed
   fir packages.

A project-local extension with the same name as a global one **shadows** the
global one.

### Trust Model

Project-local extensions require approval before fir will execute them.  On
first encounter fir:

1. Computes a SHA-256 hash of the file.
2. Prompts the user to review and approve it.
3. Records the approval in `~/.config/fir/trusted-extensions.json`.

If the file changes (different hash), fir re-prompts.

Trust records are JSON objects keyed by `<projectDir>:<extensionName>`,
each holding the approved SHA-256 hash and an approval timestamp.

Global extensions are auto-trusted because the user installed them explicitly.

---

## Process Lifecycle

```
spawn → init (5 s timeout) → run loop
  ↓ crash
restart with exponential backoff (1 s → 2 s → 4 s → … ≤ 30 s)
  ↓ 5 consecutive failures
give up, log error
```

- **Startup** – fir calls `exec.Command(path)` and wires up three pipes: stdin,
  stdout, stderr.
- **Shutdown** – fir sends **SIGTERM**, waits up to 2 seconds, then **SIGKILL**.
- **Restart** – on crash, exponential backoff starting at 1 s, doubling each
  time, capped at 30 s.  A successful restart resets the failure counter.
- **Max restarts** – after 5 consecutive failures fir gives up and logs
  `ErrTooManyFailures`.

---

## ID Allocation

- fir uses ID `1` for the `init` request.
- Subsequent fir-originated requests start from `100` and increment.
- The Python SDK uses IDs starting from `1000` for extension-originated
  requests.  Any non-overlapping range is fine; IDs only need to be unique
  within a single direction.

---

## Concurrency Notes

- fir processes inbound responses (from extension-originated requests) on the
  same read loop that handles fir-initiated requests.  Extensions **must not**
  block their main read loop while waiting for a fir response — doing so causes
  a deadlock.  The Python SDK avoids this by dispatching tool calls and hooks to
  worker threads.
- Multiple hooks from different extensions for the same event are called
  concurrently; fir waits for all before proceeding.
- Event notifications are fire-and-forget: fir does not wait for the extension
  to finish processing them.
