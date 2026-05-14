---
builtin: true
name: extension-creator
description: Create or modify a fir extension — write a Python script in .fir/extensions/ using the fir_ext SDK with tool handlers and event subscriptions.
override: true
---

# Extensions

Extensions are standalone scripts in `.fir/extensions/` (project-local) or `~/.config/fir/extensions/` (global). They communicate with fir over JSON-RPC 2.0 on stdio.

## Creating a Python Extension

Use the bundled `fir_ext` SDK (no install needed). Template:

```python
#!/usr/bin/env python3
# ---
# name: <extension-name>
# description: <what the extension does>
# ---
"""<description>."""

import fir_ext


@fir_ext.tool(
    name="<tool_name>",
    description="<what the tool does>",
    parameters={
        "type": "object",
        "properties": {
            "<param>": {"type": "string", "description": "<desc>"},
        },
        "required": ["<param>"],
    },
)
def my_tool(params, ctx):
    # Do work...
    return {
        "content": [{"type": "text", "text": "result"}],
        "is_error": False,
    }


@fir_ext.on("session_start")
def on_start(params, ctx):
    ctx.set_status("extension loaded")


fir_ext.run(name="<extension-name>")
```

## Key APIs

### Decorators

- `@fir_ext.tool(name, description, parameters)` — register a tool the AI can call. Handler receives `(params, ctx)` and returns `{"content": [...], "is_error": bool}`.
- `@fir_ext.on("event_name")` — subscribe to events (`session_start`, `agent_end`) or hooks (`hook/tool_call`). Hook handlers return `None` to allow or a dict to modify behavior.
- `fir_ext.ToolError(message)` — raise inside a tool handler to return a structured error.

### Context Methods (`ctx`)

| Method | Description |
|--------|-------------|
| `ctx.notify(message, level="info")` | Show a notification (`info`, `warning`, `error`) |
| `ctx.exec(command, args=None, timeout=10.0)` | Run a command; returns `{stdout, stderr, exit_code}` |
| `ctx.send_message(custom_type, content, display=False)` | Inject a custom message into the session |
| `ctx.send_user_message(content)` | Inject a user-role message into the session |
| `ctx.set_status(text)` | Set footer status text |
| `ctx.set_session_name(name)` | Set session display name |
| `ctx.set_label(entry_id, label)` | Set a label on a session entry |
| `ctx.clear_label(entry_id)` | Clear a label |
| `ctx.get_active_tools()` | Get list of active tool names |
| `ctx.set_active_tools(tools)` | Set active tools |
| `ctx.set_model(model)` | Change the current model |
| `ctx.call_tool(name, params=None, timeout=60)` | Call any registered tool by name; returns `{content, is_error}`. Result never enters conversation history. |
| `ctx.side_query(question, timeout=120)` | Ephemeral side-query LLM call using current session context; returns the response text. Nothing is saved to history. |
| `ctx.continue_session()` | Trigger the agent to continue without injecting a message |
| `ctx.set_session_data(key, value)` | Store a key/value pair persisted across `/reexec` |
| `ctx.get_session_data(key)` | Retrieve a previously stored value |

### Event Loop

Always call `fir_ext.run(name="<name>")` at the end of the script.

## Checklist

- [ ] Create `.fir/extensions/<name>.py` with a shebang (`#!/usr/bin/env python3`) **and make it executable immediately**: `chmod +x .fir/extensions/<name>.py`. Discovery silently skips non-executable files. Always `chmod +x` right after creating the file.
- [ ] Add comment frontmatter (`# ---` / `# ---`) with at least `name:`. Files without frontmatter are ignored by discovery.
- [ ] Define tools with `@fir_ext.tool(...)`, slash commands with `@fir_ext.command(...)`, and/or event handlers with `@fir_ext.on(...)`. The init handshake reports them to fir — there is no need to declare them again in frontmatter.
- [ ] End with `fir_ext.run(name="<name>")`.
- [ ] **Never put test files in the extensions directory** — use `pkg/resources/testdata/` for Python tests or `pkg/extension/integration/` for Go integration tests.
- [ ] Test by running fir with `--debug` and checking for init handshake success.
- [ ] On first use, fir will prompt to trust the extension (project-local only).
- [ ] **If you add, remove, or change any extension API surface** (bridge methods, SDK functions, context methods, events, hooks), update the demo extension (`pkg/resources/builtin_extensions/demo.py`) and its test (`pkg/extension/sdk/python/demo_ext_test.py`) to exercise the new surface. The demo is the canonical reference for the full API.

## Reference Extension

`pkg/resources/builtin_extensions/demo.py` is the **canonical reference** for the entire extension API. It exercises every outbound call (`ctx.*`), every event/hook, tool registration, and slash commands. Consult it first when building a new extension. Its companion test (`pkg/extension/sdk/python/demo_ext_test.py`) is the protocol-level test suite.

## Frontmatter Reference

| Field | Required | Description |
|-------|----------|-------------|
| `name` | yes | Extension identifier |
| `description` | no | One-line description shown in `fir extensions list` |
| `modes` | no | Restrict to specific modes: `tui`, `text`, `json`, `acp` |
| `builtin` | no | `true` for bundled extensions (used internally) |
| `demo` | no | `true` to skip unless explicitly enabled |

## Non-Python Extensions

Any executable works — just implement the JSON-RPC 2.0 protocol on stdio. See `docs/extension-protocol.md` for the full wire-protocol reference (message shapes, all bridge methods, events, hooks). For anything non-trivial, Python with `fir_ext` is recommended.

## Troubleshooting

- **Not loading?** Check execute permission and shebang line. Run `fir --debug`.
- **Init timeout (5s)?** Don't do heavy work before `fir_ext.run()`.
- **Tool timeout (30s)?** Use `ctx.notify()` for progress on long operations.
- **`fir_ext` not found?** Check `PYTHONPATH` isn't overridden. Run `fir --debug`.
