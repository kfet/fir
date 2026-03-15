#!/usr/bin/env python3
# ---
# demo: true
# events: hook/tool_call, session_start, session_shutdown, agent_start, agent_end, turn_start, turn_end, message_start, message_end, tool_execution_start, tool_execution_end
# commands: demo-echo: Echo arguments back as a TUI message
# ---
"""demo.py — comprehensive example exercising the full fir extension API.

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
  The batch_example tool shows how extensions can build higher-level
  orchestration on top of the built-in `batch` tool. It constructs a
  batch tool-call payload programmatically — gathering files and commands —
  then asks the agent to call `batch` with the assembled payload.
  This pattern lets extensions compose multi-tool workflows while keeping
  raw I/O ephemeral.
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
        "Demonstrate the batch tool pattern: build a multi-tool payload "
        "programmatically and return it for the agent to pass to `batch`. "
        "Give it a directory path and it assembles Read calls for key files "
        "plus a git-status check, with instructions to summarise the project."
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
    """Build a batch payload that reads common project files + git status.

    This shows how an extension can *compose* tool calls for the built-in
    batch tool. The extension doesn't execute the tools itself — it returns
    a ready-made payload the agent feeds to ``batch``.

    The pattern:
      1. Extension builds the tool list programmatically (can use ctx.exec
         to discover which files exist, filter, etc.)
      2. Extension returns the assembled batch payload.
      3. Agent calls ``batch`` with that payload — raw I/O stays ephemeral.
    """
    directory = params.get("directory", ".")
    extra = params.get("extra_instructions", "")

    # Discover which key files exist so we only read what's there.
    probe = ctx.exec("sh", [
        "-c",
        f'cd {directory} && for f in README.md CHANGELOG.md go.mod package.json '
        f'Cargo.toml pyproject.toml Makefile; do [ -f "$f" ] && echo "$f"; done',
    ])
    found_files = [f for f in probe.get("stdout", "").strip().splitlines() if f]

    # Assemble the tool list.
    # Read each discovered file (first 40 lines to keep it brief).
    tool_calls = [
        {"name": "Read", "params": {"path": f"{directory}/{f}", "limit": 40}}
        for f in found_files
    ]

    # Always include a git status check.
    git_cmd = (
        f"cd {directory} && git status --short 2>/dev/null"
        " || echo 'not a git repo'"
    )
    tool_calls.append({
        "name": "Bash",
        "params": {"command": git_cmd},
    })

    # Build instructions for the synthesis LLM.
    instructions = (
        "You are summarising a software project. Based on the file contents "
        "and git status above, provide:\n"
        "1. A one-line project description\n"
        "2. The language/framework stack\n"
        "3. Build system (if identifiable)\n"
        "4. Current git status (clean, dirty, number of changes)\n"
        "5. Any notable recent changes from the changelog\n"
        "Keep the summary concise — max 10 lines."
    )
    if extra:
        instructions += f"\n\nAdditional focus: {extra}"

    # Return the assembled payload — the agent should pass this to `batch`.
    return {
        "content": [
            {
                "type": "text",
                "text": (
                    "Here is a ready-made payload for the `batch` tool. "
                    "Call `batch` with these exact parameters:\n\n"
                    f'{{"description": "project summary for {directory}", '
                    f'"tools": {_json_compact(tool_calls)}, '
                    f'"instructions": {_json_compact(instructions)}}}'
                ),
            }
        ],
        "is_error": False,
    }


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
