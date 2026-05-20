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

For the full wire-protocol specification — message framing, init handshake, tool call/hook/event schemas, all bridge methods, and process lifecycle — see [extension-protocol.md](extension-protocol.md).

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

### `@fir_ext.command(name, description="")`

Register a slash command that users can type in the TUI as `/name`. The
decorated function receives `(args: list[str], ctx: Context)` and may return a
dict with an optional `"message"` key shown in the TUI, or `None`.

```python
@fir_ext.command(name="summary", description="Summarise project status")
def cmd_summary(args, ctx):
    return {"message": "All checks passing."}
```

The command name is reported during the init handshake; no frontmatter
declaration is required.

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

The `ctx` object passed to every handler provides methods for calling back into
fir: `notify`, `exec`, `set_status`, `set_session_name`, `set_label`,
`clear_label`, `get_active_tools`, `set_active_tools`, `set_model`,
`send_message`, `send_user_message`, `set_session_data`, `get_session_data`,
`continue_session`, `side_query`, `call_tool`, `list_tools`, and `prepend`.

All methods are synchronous — they send a JSON-RPC request to fir and block
until fir responds. Full signatures, parameters, and return values are
documented in the `fir_ext.py` module docstring and in
[extension-protocol.md](extension-protocol.md).

## Discovery

fir looks for extensions in two locations at startup:

| Location | Scope | Description |
|----------|-------|-------------|
| `.fir/extensions/` | Project | Extensions for the current project only |
| `<agent-dir>/extensions/` (`~/.config/fir/extensions/` by default) | Global | Extensions available in all projects; override the agent dir with `FIR_AGENT_DIR` or `--agent-dir` |

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

**Global extensions** (in `<agent-dir>/extensions/`) are always trusted — you installed them yourself.

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
