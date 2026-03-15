#!/usr/bin/env python3
# ---
# demo: true
# events: hook/tool_call, session_start, session_shutdown, agent_start, agent_end, turn_start, turn_end, message_start, message_end, tool_execution_start, tool_execution_end
# commands: demo-echo: Echo arguments back as a TUI message
# ---
"""demo.py — comprehensive example exercising the full fir extension API.

THIS IS THE CANONICAL REFERENCE for the extension API surface.  When you
add, remove, or change a bridge method, SDK function, event, hook, or
context method, update this file and its companion test
(pkg/extension/sdk/python/demo_ext_test.py) to exercise the new surface.

Outbound calls demonstrated (extension → fir):
  notify · exec · set_status · set_session_name ·
  set_label · clear_label · get_active_tools · set_active_tools ·
  set_model · send_message · send_user_message

Inbound surface demonstrated (fir → extension):
  • Tool registration: word_count, shell_run, list_tools, pin_tools,
    change_model, inject_message, batch_example
  • hook/tool_call: blocks tools whose name starts with "blocked:"
  • All ten events: session_start, session_shutdown, agent_start, agent_end,
    turn_start, turn_end, message_start, message_end,
    tool_execution_start, tool_execution_end

Batch tool demonstration:
  The batch_example tool shows how extensions can use ctx.call_tool()
  and ctx.btw() to orchestrate multi-tool workflows.  It calls tools
  directly (Read, Bash), collects results in local memory, and
  synthesises via an ephemeral LLM call — raw output never enters
  conversation history.
"""

import fir_ext

# ---------------------------------------------------------------------------
# Tools
# ---------------------------------------------------------------------------


@fir_ext.tool(
    name="word_count",
    description="Count words in a string. Calls set_label and notify.",
    parameters={
        "type": "object",
        "properties": {"text": {"type": "string", "description": "Text to count"}},
        "required": ["text"],
    },
)
def word_count(params, ctx):
    words = params.get("text", "").split()
    n = len(words)
    ctx.set_label("last_wc", str(n))          # set_label
    ctx.notify(f"word_count: {n} words")       # notify
    return {"count": n, "words": words}


@fir_ext.tool(
    name="shell_run",
    description="Run a shell command and return its output. Calls exec.",
    parameters={
        "type": "object",
        "properties": {
            "command": {"type": "string", "description": "Executable name"},
            "args": {
                "type": "array",
                "items": {"type": "string"},
                "description": "Arguments (optional)",
            },
        },
        "required": ["command"],
    },
)
def shell_run(params, ctx):
    return ctx.exec(params["command"], params.get("args", []))


@fir_ext.tool(
    name="list_tools",
    description="Return the names of currently active tools. Calls get_active_tools.",
    parameters={"type": "object", "properties": {}},
)
def list_tools(params, ctx):
    tools = ctx.get_active_tools()  # get_active_tools
    return {"tools": tools}


@fir_ext.tool(
    name="pin_tools",
    description="Restrict active tools to the given list. Calls set_active_tools.",
    parameters={
        "type": "object",
        "properties": {
            "tools": {
                "type": "array",
                "items": {"type": "string"},
                "description": "Tool names to keep active",
            }
        },
        "required": ["tools"],
    },
)
def pin_tools(params, ctx):
    ctx.set_active_tools(params["tools"])  # set_active_tools
    return {"ok": True}


@fir_ext.tool(
    name="change_model",
    description="Switch to a different model. Calls set_model.",
    parameters={
        "type": "object",
        "properties": {
            "provider": {"type": "string"},
            "model": {"type": "string"},
        },
        "required": ["provider", "model"],
    },
)
def change_model(params, ctx):
    ok = ctx.set_model(params["provider"], params["model"])  # set_model
    return {"ok": ok}


@fir_ext.tool(
    name="inject_message",
    description=(
        "Inject a message into the session. "
        "kind='custom' calls send_message; kind='user' calls send_user_message."
    ),
    parameters={
        "type": "object",
        "properties": {
            "kind": {"type": "string", "enum": ["custom", "user"]},
            "content": {"type": "string"},
        },
        "required": ["kind", "content"],
    },
)
def inject_message(params, ctx):
    if params["kind"] == "user":
        ctx.send_user_message(params["content"])           # send_user_message
    else:
        ctx.send_message("demo_note", params["content"])   # send_message
    return {"ok": True}


@fir_ext.tool(
    name="batch_example",
    description=(
        "Demonstrate the call_tool + btw pattern: probe a project directory, "
        "call Read/Bash tools directly via call_tool(), then synthesise "
        "results with btw(). Everything stays ephemeral."
    ),
    parameters={
        "type": "object",
        "properties": {
            "directory": {
                "type": "string",
                "description": "Project directory to analyse",
            },
            "extra_instructions": {
                "type": "string",
                "description": "Additional instructions for the synthesis (optional)",
            },
        },
        "required": ["directory"],
    },
)
def batch_example(params, ctx):
    """Probe a project directory, read key files, and synthesise a summary.

    Demonstrates how extensions can orchestrate multiple tool calls using
    ``ctx.call_tool()`` and synthesise with ``ctx.btw()``.  All raw tool
    output is held in local Python memory — it never enters conversation
    history.

    The pattern:
      1. Use ctx.exec() to discover which files exist.
      2. Use ctx.call_tool() to read each file and run git status.
      3. Build a synthesis prompt from collected outputs.
      4. Use ctx.btw() for an ephemeral LLM summary.
      5. Return only the summary.
    """
    directory = params.get("directory", ".")
    extra = params.get("extra_instructions", "")

    # Discover which key files exist.
    probe = ctx.exec("sh", [
        "-c",
        f'cd {directory} && for f in README.md CHANGELOG.md go.mod '
        f'package.json Cargo.toml pyproject.toml Makefile; '
        f'do [ -f "$f" ] && echo "$f"; done',
    ])
    found = [f for f in probe.get("stdout", "").strip().splitlines() if f]

    # Collect outputs from tool calls.
    outputs = []

    for fname in found:
        result = ctx.call_tool("Read", {
            "path": f"{directory}/{fname}",
            "limit": 40,
        })
        text = _extract_text(result)
        outputs.append(f"--- {fname} ---\n{text}")

    # Git status.
    git_cmd = (
        f"cd {directory} && git status --short 2>/dev/null"
        " || echo 'not a git repo'"
    )
    git_result = ctx.call_tool("Bash", {"command": git_cmd})
    outputs.append(f"--- git status ---\n{_extract_text(git_result)}")

    # Synthesise.
    instructions = (
        "You are summarising a software project. Based on the file "
        "contents and git status above, provide:\n"
        "1. A one-line project description\n"
        "2. The language/framework stack\n"
        "3. Build system (if identifiable)\n"
        "4. Current git status (clean, dirty, number of changes)\n"
        "5. Any notable recent changes from the changelog\n"
        "Keep the summary concise — max 10 lines."
    )
    if extra:
        instructions += f"\n\nAdditional focus: {extra}"

    prompt = "\n\n".join(outputs) + f"\n\n--- Instructions ---\n{instructions}"

    return ctx.btw(prompt)


def _extract_text(result):
    """Pull text content from a call_tool result."""
    content = result.get("content", [])
    if isinstance(content, list):
        parts = []
        for block in content:
            if isinstance(block, dict):
                t = block.get("text") or block.get("Text", "")
                if t:
                    parts.append(t)
        return "\n".join(parts)
    return str(content)


def _json_compact(obj):
    """JSON-encode with no unnecessary whitespace."""
    import json
    return json.dumps(obj, separators=(",", ":"))


# ---------------------------------------------------------------------------
# Hook
# ---------------------------------------------------------------------------


@fir_ext.on("hook/tool_call")
def on_hook_tool_call(params, ctx):
    """Block any tool whose name starts with 'blocked:'."""
    name = params.get("tool_name") or params.get("name", "")
    if name.startswith("blocked:"):
        return {"block": True, "reason": f"{name!r} is not permitted by demo extension"}
    return None


# ---------------------------------------------------------------------------
# Events
# ---------------------------------------------------------------------------


@fir_ext.on("session_start")
def on_session_start(params, ctx):
    ctx.set_status("demo ready")                      # set_status


@fir_ext.on("session_shutdown")
def on_session_shutdown(params, ctx):
    ctx.set_status("")                                 # set_status (clear)


@fir_ext.on("agent_start")
def on_agent_start(params, ctx):
    ctx.set_session_name("demo session")              # set_session_name


@fir_ext.on("agent_end")
def on_agent_end(params, ctx):
    ctx.notify("Agent finished", level="info")        # notify
    ctx.clear_label("last_wc")                        # clear_label


@fir_ext.on("turn_start")
def on_turn_start(params, ctx):
    pass   # subscribed; no outbound call needed


@fir_ext.on("turn_end")
def on_turn_end(params, ctx):
    pass


@fir_ext.on("message_start")
def on_message_start(params, ctx):
    pass


@fir_ext.on("message_end")
def on_message_end(params, ctx):
    pass


@fir_ext.on("tool_execution_start")
def on_tool_execution_start(params, ctx):
    name = params.get("tool_name", "")
    tcid = params.get("tool_call_id", "")
    if tcid:
        ctx.set_label(tcid, f"running:{name}")        # set_label


@fir_ext.on("tool_execution_end")
def on_tool_execution_end(params, ctx):
    tcid = params.get("tool_call_id", "")
    if tcid:
        ctx.clear_label(tcid)                         # clear_label


# ---------------------------------------------------------------------------
# Commands
# ---------------------------------------------------------------------------


@fir_ext.command(name="demo-echo", description="Echo arguments back as a TUI message")
def cmd_demo_echo(args, ctx):
    msg = " ".join(args) if args else "(no arguments)"
    return {"message": f"demo-echo: {msg}"}


fir_ext.run(name="demo")
