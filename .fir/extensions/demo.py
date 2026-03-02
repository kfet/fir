#!/usr/bin/env python3
"""demo.py — comprehensive example exercising the full fir extension API.

Outbound calls demonstrated (extension → fir):
  notify · exec · set_status · set_session_name ·
  set_label · clear_label · get_active_tools · set_active_tools ·
  set_model · send_message · send_user_message

Inbound surface demonstrated (fir → extension):
  • Tool registration: word_count, shell_run, list_tools, pin_tools,
    change_model, inject_message
  • hook/tool_call: blocks tools whose name starts with "blocked:"
  • All ten events: session_start, session_shutdown, agent_start, agent_end,
    turn_start, turn_end, message_start, message_end,
    tool_execution_start, tool_execution_end
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


fir_ext.run(name="demo")
