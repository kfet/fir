# External Process Extensions

External process extensions let you extend fir with scripts written in **any language** — Python, bash, Node.js, Ruby, or anything else that can read and write newline-delimited JSON on stdio. Each extension runs as a child process that communicates with fir over **JSON-RPC 2.0**.

Unlike built-in Go extensions (which compile into the fir binary and use the `extension.Register()` API), external process extensions are standalone executables discovered at startup. They can register tools the AI can call, subscribe to lifecycle events, intercept hooks, and call back into fir to run commands, send messages, or change settings.

## Quick Start

### 1. Create the extensions directory

```bash
mkdir -p .fir/extensions
```

### 2. Write an extension

Create `.fir/extensions/wordcount.py`:

```python
#!/usr/bin/env python3
"""Count words in a file — a simple fir extension."""

import fir_ext

@fir_ext.tool(
    name="count_words",
    description="Count the number of words in a file",
    parameters={
        "type": "object",
        "properties": {
            "path": {
                "type": "string",
                "description": "Path to the file to count words in",
            }
        },
        "required": ["path"],
    },
)
def count_words(params, ctx):
    path = params["path"]
    try:
        with open(path) as f:
            text = f.read()
        words = len(text.split())
        lines = text.count("\n")
        return {
            "content": [
                {
                    "type": "text",
                    "text": f"{path}: {words} words, {lines} lines",
                }
            ],
            "is_error": False,
        }
    except FileNotFoundError:
        return {
            "content": [{"type": "text", "text": f"File not found: {path}"}],
            "is_error": True,
        }

@fir_ext.on("session_start")
def on_session_start(params, ctx):
    ctx.set_status("wordcount extension loaded")

fir_ext.run(name="wordcount")
```

### 3. Make it executable

```bash
chmod +x .fir/extensions/wordcount.py
```

### 4. Trust and use it

The next time you start fir in this project, it discovers the extension, prompts you to trust it (first time only), performs a handshake, and registers the `count_words` tool. The AI can then use it like any built-in tool.

## How It Works

```
┌──────────┐  stdin (JSON-RPC)  ┌───────────────┐
│          │ ─────────────────► │               │
│   fir    │                    │   extension   │
│          │ ◄───────────────── │   process     │
└──────────┘  stdout (JSON-RPC) └───────────────┘
                                  stderr → fir log
```

fir spawns the extension as a child process. Messages flow over **stdin/stdout** as newline-delimited JSON-RPC 2.0. The extension's **stderr** is captured and forwarded to fir's log.

## Protocol Reference

All messages are JSON-RPC 2.0 objects, one per line (newline-delimited).

### Init Handshake

Immediately after spawning the process, fir sends an `init` request. The extension must respond within **5 seconds**.

**fir → extension:**
```json
{"jsonrpc":"2.0","id":1,"method":"init","params":{"version":"1","cwd":"/path/to/project"}}
```

**extension → fir:**
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "name": "my-extension",
    "tools": [
      {
        "name": "count_words",
        "description": "Count words in a file",
        "parameters": {"type": "object", "properties": {"path": {"type": "string"}}, "required": ["path"]}
      }
    ],
    "events": ["session_start", "hook/tool_call"]
  }
}
```

| Field | Description |
|-------|-------------|
| `name` | Display name for the extension |
| `tools` | Array of tool definitions (JSON Schema `parameters`) to register |
| `events` | Event and hook names the extension wants to receive |

### Tool Calls (fir → extension)

When the AI invokes a tool registered by the extension, fir sends a `tool_call` request:

```json
{"jsonrpc":"2.0","id":2,"method":"tool_call","params":{"tool_call_id":"tc_abc","name":"count_words","params":{"path":"README.md"}}}
```

The extension responds with a tool result:

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "content": [{"type": "text", "text": "README.md: 142 words"}],
    "is_error": false
  }
}
```

Tool calls time out after **30 seconds** by default.

### Hooks (fir → extension)

Hooks are requests that let extensions intercept and optionally modify fir behavior. They are sent as JSON-RPC requests (they have an `id` and expect a response). Hook method names start with `hook/`.

```json
{"jsonrpc":"2.0","id":3,"method":"hook/tool_call","params":{"tool":"bash","args":{"command":"rm -rf /"}}}
```

The extension can return data to influence fir's behavior, or return `null` to allow the default:

```json
{"jsonrpc":"2.0","id":3,"result":{"blocked":true,"reason":"Dangerous command"}}
```

To subscribe to a hook, include its full name (e.g. `"hook/tool_call"`) in the `events` array during init.

### Events (fir → extension)

Events are JSON-RPC **notifications** (no `id`, no response expected). They are fire-and-forget.

```json
{"jsonrpc":"2.0","method":"event/session_start","params":{"session_id":"abc123"}}
```

To receive events, include the event name (without the `event/` prefix) in the `events` array during init. For example, listing `"session_start"` in events causes fir to send `event/session_start` notifications.

### Extension → fir Calls

Extensions can call back into fir by sending JSON-RPC requests on stdout. fir responds on stdin.

| Method | Params | Description |
|--------|--------|-------------|
| `notify` | `{message, level}` | Show a notification (`level`: `"info"`, `"warning"`, `"error"`) |
| `exec` | `{command, args}` | Run a command; returns `{stdout, stderr, exit_code}` |
| `send_message` | `{custom_type, content, display}` | Inject a custom message into the session |
| `send_user_message` | `{content}` | Inject a user message into the session |
| `set_session_name` | `{name}` | Set the session display name |
| `set_label` | `{entry_id, label}` | Set a label on a session entry |
| `clear_label` | `{entry_id}` | Clear a label from a session entry |
| `get_active_tools` | *(none)* | Returns array of active tool names |
| `set_active_tools` | `{names}` | Set which tools are active |
| `set_model` | `{provider, id}` | Change the current model; returns `{ok}` |
| `set_status` | `{key, text}` | Set persistent status text in the UI footer |

**Example — running a command from an extension:**

```json
{"jsonrpc":"2.0","id":1001,"method":"exec","params":{"command":"wc","args":["-l","README.md"]}}
```

fir responds:

```json
{"jsonrpc":"2.0","id":1001,"result":{"stdout":"42 README.md\n","stderr":"","exit_code":0}}
```

## Python SDK Reference

fir bundles a Python SDK (`fir_ext`) that handles all protocol details. It is automatically available to extensions — no `pip install` needed.

### `@fir_ext.tool(name, description, parameters)`

Register a tool. The decorated function receives `(params: dict, ctx: Context)` and should return a result dict with `content` and `is_error` fields matching the tool result schema.

```python
@fir_ext.tool(
    name="greet",
    description="Greet someone",
    parameters={
        "type": "object",
        "properties": {"name": {"type": "string"}},
        "required": ["name"],
    },
)
def greet(params, ctx):
    return {
        "content": [{"type": "text", "text": f"Hello, {params['name']}!"}],
        "is_error": False,
    }
```

### `@fir_ext.on(event_or_hook_name)`

Register an event or hook handler. Use bare names for events, `hook/` prefix for hooks:

```python
@fir_ext.on("session_start")
def on_start(params, ctx):
    ctx.notify("Extension loaded!")

@fir_ext.on("hook/tool_call")
def on_tool_call(params, ctx):
    # Return None to allow, or a dict to modify behavior
    return None
```

### `fir_ext.ToolError(message, code=-32000)`

Raise inside a tool handler to return a structured error:

```python
@fir_ext.tool(name="divide", description="Divide two numbers", parameters={...})
def divide(params, ctx):
    if params["b"] == 0:
        raise fir_ext.ToolError("Division by zero")
    return {"content": [{"type": "text", "text": str(params["a"] / params["b"])}], "is_error": False}
```

### `fir_ext.run(name=None)`

Start the event loop. Call this at the end of your script. The `name` parameter sets the extension name reported during init (defaults to `"python-ext"`).

### Context Methods

The `ctx` object passed to every handler provides these methods:

| Method | Description |
|--------|-------------|
| `ctx.notify(message, level="info")` | Show a notification in fir |
| `ctx.exec(command, timeout_sec=30)` | Run a shell command; returns `{stdout, stderr, exit_code}` |
| `ctx.send_message(role, content)` | Inject a message into the session |
| `ctx.set_status(text)` | Set footer status text |
| `ctx.set_session_name(name)` | Set session display name |
| `ctx.set_label(entry_id, label)` | Set a label on a session entry |
| `ctx.clear_label(entry_id)` | Clear a label |
| `ctx.get_active_tools()` | Get list of active tool names |
| `ctx.set_active_tools(tools)` | Set active tools |
| `ctx.set_model(model)` | Change the current model |

All context methods are synchronous — they send a JSON-RPC request to fir and block until fir responds (10-second timeout).

## Discovery

fir looks for extensions in two locations at startup:

| Location | Scope | Description |
|----------|-------|-------------|
| `.fir/extensions/` | Project | Extensions for the current project only |
| `~/.config/fir/extensions/` | Global | Extensions available in all projects |

Extensions must be **executable files** (have the execute permission bit set). The extension name is derived from the filename without its extension (e.g., `wordcount.py` → `wordcount`).

**Shadowing:** If a project-local extension has the same name as a global one, the project-local version takes precedence.

### Optional mode targeting

Extensions can opt into specific fir modes with comment frontmatter at the top of the script:

```python
#!/usr/bin/env python3
# ---
# modes: tui, acp
# ---
```

Supported values include `tui`/`interactive`, `text`, `json`, `rpc`, and `acp`.
If omitted, the extension runs in all modes.

### Non-Python Extensions

Any executable works. For a bash extension, write the JSON-RPC protocol manually:

```bash
#!/usr/bin/env bash
# .fir/extensions/hello.sh — minimal bash extension

read -r init_request
id=$(echo "$init_request" | jq -r '.id')
echo "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"name\":\"hello\",\"tools\":[],\"events\":[]}}"

# Main loop — read and handle messages
while IFS= read -r line; do
    method=$(echo "$line" | jq -r '.method // empty')
    # Handle messages as needed...
done
```

For anything non-trivial, using the Python SDK (or writing your own SDK in your language) is strongly recommended over manual protocol handling.

## Security

### Project-Local Trust

Project-local extensions (in `.fir/extensions/`) require explicit trust before fir will run them. This prevents malicious code from executing when you clone an untrusted repository.

On first encounter, fir:

1. Computes a **SHA-256 hash** of the extension file
2. Prompts you to review and approve the extension
3. Records the approval in `~/.config/fir/trusted-extensions.json`

If the extension file changes (different hash), fir will prompt for re-approval.

**Global extensions** (in `~/.config/fir/extensions/`) are always trusted — you installed them yourself.

### Trust Store

Trust records are stored in `~/.config/fir/trusted-extensions.json` and keyed by `projectDir:extensionName`. Each entry records the approved SHA-256 hash and timestamp.

## Troubleshooting

### Checking Extension Logs

Extension stderr output is captured by fir and logged. Run fir with debug logging to see it:

```bash
fir --debug
```

Look for lines containing `extension stderr` for output from your extension process, and `started extension` to confirm successful initialization.

### Common Issues

**Extension not loading:**
- Check the file is executable: `chmod +x .fir/extensions/my_ext.py`
- Check it has a valid shebang line: `#!/usr/bin/env python3`
- Check `fir --debug` output for error messages during startup

**Init handshake timeout (5 seconds):**
- The extension must respond to the `init` request within 5 seconds
- Make sure your script doesn't do heavy work before entering the event loop
- Ensure `fir_ext.run()` is called (Python SDK) — this handles the handshake

**Tool call timeout (30 seconds):**
- Tool calls time out after 30 seconds by default
- For long-running tools, do the work asynchronously and return progress updates via `ctx.notify()`

**Extension crashes / restarts:**
- fir automatically restarts crashed extensions with exponential backoff (1s, 2s, 4s, …, up to 30s)
- After 5 consecutive failures, fir gives up and logs an error
- Check stderr logs for crash details

**Trust prompt not appearing:**
- Only project-local extensions require trust. Global extensions are auto-trusted.
- If a previously trusted extension was modified, fir will re-prompt due to hash mismatch

**`fir_ext` module not found:**
- fir automatically extracts and configures the Python SDK path. Check that `PYTHONPATH` isn't being overridden in your shell
- Run `fir --debug` and look for "failed to extract SDKs" messages
