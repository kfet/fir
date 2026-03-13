---
builtin: true
name: extension-creator
description: Create or modify a fir extension — write a Python script in .fir/extensions/ using the fir_ext SDK with tool handlers and event subscriptions.
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
# events: <comma-separated list of events/hooks this extension subscribes to>
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

### Event Loop

Always call `fir_ext.run(name="<name>")` at the end of the script.

## Checklist

- [ ] Create `.fir/extensions/<name>.py` with a shebang (`#!/usr/bin/env python3`).
- [ ] Add comment frontmatter (`# ---` / `# ---`) with at least `name:`. Files without frontmatter are ignored by discovery.
- [ ] Declare **all** subscribed events/hooks in the `events:` frontmatter field (e.g. `events: agent_end, turn_end, hook/tool_call`). This enables lazy loading — the extension process is only started when a matching event fires.
- [ ] If the extension registers slash commands, declare them in `commands:` frontmatter (e.g. `commands: my-cmd: Run something`). This lets fir show the command in `/help` before the extension is started.
- [ ] Make it executable: `chmod +x .fir/extensions/<name>.py`.
- [ ] Define tools with `@fir_ext.tool(...)` and/or event handlers with `@fir_ext.on(...)`.
- [ ] End with `fir_ext.run(name="<name>")`.
- [ ] **Never put test files in the extensions directory** — use `pkg/resources/testdata/` for Python tests or `pkg/extension/integration/` for Go integration tests.
- [ ] Test by running fir with `--debug` and checking for init handshake success.
- [ ] On first use, fir will prompt to trust the extension (project-local only).

## Frontmatter Reference

| Field | Required | Description |
|-------|----------|-------------|
| `name` | yes | Extension identifier |
| `description` | no | One-line description shown in `fir extensions list` |
| `events` | recommended | Comma-separated events/hooks the extension subscribes to. Enables lazy loading. |
| `commands` | if applicable | Slash commands: `name1: desc1, name2: desc2` |
| `modes` | no | Restrict to specific modes: `tui`, `text`, `json`, `acp` |
| `builtin` | no | `true` for bundled extensions (used internally) |
| `demo` | no | `true` to skip unless explicitly enabled |

## Non-Python Extensions

Any executable works — just implement the JSON-RPC 2.0 protocol on stdio. See `docs/extensions.md` for the full protocol reference. For anything non-trivial, Python with `fir_ext` is recommended.

## Troubleshooting

- **Not loading?** Check execute permission and shebang line. Run `fir --debug`.
- **Init timeout (5s)?** Don't do heavy work before `fir_ext.run()`.
- **Tool timeout (30s)?** Use `ctx.notify()` for progress on long operations.
- **`fir_ext` not found?** Check `PYTHONPATH` isn't overridden. Run `fir --debug`.
