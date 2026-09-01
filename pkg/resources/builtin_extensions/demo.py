#!/usr/bin/env python3
# ---
# name: demo
# explicit: true
# cli_verbs: demo-cli
# ---
"""demo.py — comprehensive example exercising the full fir extension API.

THIS IS THE CANONICAL REFERENCE for the extension API surface.  When you
add, remove, or change a bridge method, SDK function, event, hook, or
context method, update this file and its companion test
(pkg/extension/sdk/python/demo_ext_test.py) to exercise the new surface.

Outbound calls demonstrated (extension → fir):
  notify · exec · set_status · set_session_name ·
  set_label · clear_label ·
  set_model · send_message · send_user_message ·
  set_session_data · get_session_data · get_session_file · get_session_name · get_session_id · continue_session · side_query · call_tool ·
  list_tools · list_extensions · available_models ·
  report_progress · restart_session · reload_extension · reload_mcp ·
  prepend

Inbound surface demonstrated (fir → extension):
  • Tool registration: word_count, shell_run, list_tools, pin_tools,
    change_model, inject_message, restart_demo, batch_example
  • hook/tool_call: blocks tools whose name starts with "blocked:"
  • All eleven events: session_start, session_shutdown, agent_start, agent_end,
    turn_start, turn_end, message_start, message_end,
    tool_execution_start, tool_execution_end, provider_error

Type annotations:
  Handlers in this file use the TypedDicts re-exported from fir_ext
  (e.g. ``fir_ext.ToolCallHookParams``, ``fir_ext.MessageEndParams``,
  ``fir_ext.ExecResult``) so that this demo doubles as a typed-API
  reference. The TypedDicts are plain ``dict`` at runtime, so the
  annotations are purely for tooling.

Batch tool demonstration:
  The batch_example tool shows how extensions can use ctx.call_tool(),
  ctx.report_progress(), and ctx.side_query() to orchestrate multi-tool
  workflows.  It calls tools
  directly (Read, Bash), collects results in local memory, and
  synthesises via an ephemeral LLM call — raw output never enters
  conversation history.
"""

import sys
import threading
import time as _time
from typing import Optional

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
    display_hint={
        "title_args": [{"name": "text", "style": "accent"}],
    },
)
def word_count(params: dict, ctx: fir_ext.Context) -> dict:
    words = params.get("text", "").split()
    n = len(words)
    ctx.set_label("last_wc", str(n))  # set_label
    ctx.notify(f"word_count: {n} words")  # notify
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
    display_hint={
        "title_args": [{"name": "command", "style": "accent"}],
        "result_max_lines": 15,
    },
)
def shell_run(params: dict, ctx: fir_ext.Context) -> fir_ext.ExecResult:
    return ctx.exec(params["command"], params.get("args", []))


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
    display_hint={
        "title_args": [
            {"name": "provider"},
            {"name": "model", "style": "accent"},
        ],
    },
)
def change_model(params: dict, ctx: fir_ext.Context) -> dict:
    ok = ctx.set_model(params["provider"], params["model"])  # set_model
    return {"ok": ok}


@fir_ext.tool(
    name="inject_message",
    description=(
        "Inject a message into the session. "
        "kind='custom' calls send_message; kind='user' calls send_user_message; "
        "kind='abort' calls send_user_message(deliver_as='abort') to cancel the "
        "current turn."
    ),
    parameters={
        "type": "object",
        "properties": {
            "kind": {"type": "string", "enum": ["custom", "user", "abort"]},
            "content": {"type": "string"},
        },
        "required": ["kind", "content"],
    },
)
def inject_message(params: dict, ctx: fir_ext.Context) -> fir_ext.OkResult:
    if params["kind"] == "user":
        ctx.send_user_message(params["content"])  # send_user_message
    elif params["kind"] == "abort":
        ctx.send_user_message("", deliver_as="abort")  # abort current turn
    else:
        ctx.send_message("demo_note", params["content"])  # send_message
    return {"ok": True}


@fir_ext.tool(
    name="restart_demo",
    description=(
        "Demonstrate ctx.restart_session(). Refuses to fire in real fir "
        "without confirm='yes-really' to avoid accidentally clearing the "
        "user's session — the canonical use is the self_handoff builtin."
    ),
    parameters={
        "type": "object",
        "properties": {
            "prompt": {"type": "string"},
            "confirm": {"type": "string"},
            "prepend_context": {"type": "string"},
        },
        "required": ["prompt"],
    },
)
def restart_demo(params: dict, ctx: fir_ext.Context) -> dict:
    if params.get("confirm") != "yes-really":
        return {"ok": False, "skipped": True}
    # restart_session: optional prepend_context demonstrates the
    # [SYS_EXT]-wrapped briefing carried into the new session.
    ctx.restart_session(params["prompt"], prepend_context=params.get("prepend_context", ""))
    return {"ok": True}


@fir_ext.tool(
    name="reload_ext_demo",
    description=(
        "Demonstrate ctx.reload_extension(name). Reloads exactly one "
        "extension by name — stops it, drops only its tools, and re-spawns "
        "the edited/new version. Builtins and self-reload are refused by fir."
    ),
    parameters={
        "type": "object",
        "properties": {
            "name": {"type": "string", "description": "Extension to reload"},
        },
        "required": ["name"],
    },
)
def reload_ext_demo(params: dict, ctx: fir_ext.Context) -> dict:
    # reload_extension: targeted single-extension reload. Returns a
    # JSON-RPC error for builtins / self-reload, surfaced here as a failure.
    ctx.reload_extension(params["name"])  # reload_extension
    return {"ok": True}


@fir_ext.tool(
    name="reload_mcp_demo",
    description=(
        "Demonstrate ctx.reload_mcp(). Re-reads MCP server configurations "
        "from disk (mcp.json, mcp.d/*.json) and applies the diff to running "
        "servers. Returns collisions (shadowed configs) and errors."
    ),
    parameters={
        "type": "object",
        "properties": {},
    },
)
def reload_mcp_demo(params: dict, ctx: fir_ext.Context) -> dict:
    # reload_mcp: re-reads MCP config from disk and reloads servers.
    # Returns a dict with 'collisions' and 'errors' arrays.
    return ctx.reload_mcp()  # reload_mcp


@fir_ext.tool(
    name="batch_example",
    description=(
        "Demonstrate the call_tool + side_query pattern: probe a project directory, "
        "call Read/Bash tools directly via call_tool(), then synthesise "
        "results with side_query(). Everything stays ephemeral."
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
    display_hint={
        "title_args": [{"name": "directory", "style": "path"}],
    },
    # This tool orchestrates several inner call_tool + side_query calls, so it
    # can legitimately run longer than the 30s default. Declare a generous
    # host-side timeout. INVARIANT: keep any inner ctx.call_tool(..., timeout=T)
    # <= this value (the inner calls below use the 60s default, well under 180).
    timeout=180,
)
def batch_example(params, ctx):
    """Probe a project directory, read key files, and synthesise a summary.

    Demonstrates how extensions can orchestrate multiple tool calls using
    ``ctx.call_tool()`` and synthesise with ``ctx.side_query()``.  All raw tool
    output is held in local Python memory — it never enters conversation
    history.

    The pattern:
      1. Use ctx.exec() to discover which files exist.
      2. Use ctx.call_tool() to read each file and run git status.
      3. Build a synthesis prompt from collected outputs.
      4. Use ctx.side_query() for an ephemeral LLM summary.
      5. Return only the summary.
    """
    directory = params.get("directory", ".")
    extra = params.get("extra_instructions", "")

    # Discover which key files exist.
    probe = ctx.exec(
        "sh",
        [
            "-c",
            f"cd {directory} && for f in README.md CHANGELOG.md go.mod "
            f"package.json Cargo.toml pyproject.toml Makefile; "
            f'do [ -f "$f" ] && echo "$f"; done',
        ],
    )
    found = [f for f in probe.get("stdout", "").strip().splitlines() if f]

    # Collect outputs from tool calls.
    outputs = []

    for fname in found:
        ctx.report_progress(f"Reading {fname}")
        result = ctx.call_tool(
            "Read",
            {
                "path": f"{directory}/{fname}",
                "limit": 40,
            },
        )
        text = _extract_text(result)
        outputs.append(f"--- {fname} ---\n{text}")

    # Git status.
    ctx.report_progress("Running git status")
    git_cmd = f"cd {directory} && git status --short 2>/dev/null || echo 'not a git repo'"
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

    # Streaming side_query — surface partial thinking text via report_progress.
    # Falls back to the blocking flavor if the host doesn't recognise
    # `stream:true` (older fir releases). This is the same pattern as the
    # aside extension; demo.py keeps it tiny on purpose.
    if hasattr(ctx, "side_query_stream"):
        stream = ctx.side_query_stream(prompt)
        partial = ""
        usage = ""
        for delta in stream:
            if delta.type == "text":
                partial += delta.text
                ctx.report_progress(f"synthesising… ({len(partial)} chars)")
            elif delta.type == "usage":
                # Terminal token accounting, including prompt-cache hit/write
                # sizes — the same numbers the aside extension footers with.
                usage = (
                    f"in {delta.tokens_in} · read {delta.cache_read} · "
                    f"write {delta.cache_write} · out {delta.tokens_out}"
                )
        if stream.error is not None:
            return {
                "content": [{"text": f"side_query failed: {stream.error}"}],
                "is_error": True,
            }
        text = (stream.result or {}).get("text", partial)
        return f"{text}\n\n[{usage}]" if usage else text

    return ctx.side_query(prompt)


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
def on_hook_tool_call(
    params: fir_ext.ToolCallHookParams, ctx: fir_ext.Context
) -> Optional[fir_ext.ToolCallHookResult]:
    """Block any tool whose name starts with 'blocked:'."""
    name = params.get("tool_name") or params.get("name", "")
    if name.startswith("blocked:"):
        return {"block": True, "reason": f"{name!r} is not permitted by demo extension"}
    return None


# ---------------------------------------------------------------------------
# Events
# ---------------------------------------------------------------------------


@fir_ext.on("session_start")
def on_session_start(params: fir_ext.SessionStartParams, ctx: fir_ext.Context) -> None:
    ctx.set_status("demo ready")  # set_status
    ctx.set_session_data("started", "true")  # set_session_data
    ctx.list_tools()  # list_tools (read-only discovery)
    ctx.list_extensions()  # list_extensions (which extensions are actually live)
    ctx.available_models()  # available_models (live-availability)
    ctx.prepend("Demo extension is active.")  # prepend
    ctx.agent_info()  # agent_info
    ctx.get_session_file()  # get_session_file
    ctx.get_session_name()  # get_session_name
    ctx.get_session_id()  # get_session_id
    ctx.put_observable("hello", "ready", "demo extension up")  # put_observable
    ctx.clear_observable("hello")  # clear_observable


@fir_ext.on("session_shutdown")
def on_session_shutdown(params: fir_ext.SessionShutdownParams, ctx: fir_ext.Context) -> None:
    _ = ctx.get_session_data("started")  # get_session_data
    ctx.set_status("")  # set_status (clear)


@fir_ext.on("agent_start")
def on_agent_start(params: fir_ext.AgentLifecycleParams, ctx: fir_ext.Context) -> None:
    ctx.set_session_name("demo session")  # set_session_name
    ctx.get_session_name()  # get_session_name


@fir_ext.on("agent_end")
def on_agent_end(params: fir_ext.AgentLifecycleParams, ctx: fir_ext.Context) -> None:
    ctx.notify("Agent finished", level="info")  # notify
    ctx.clear_label("last_wc")  # clear_label


@fir_ext.on("turn_start")
def on_turn_start(params: fir_ext.AgentLifecycleParams, ctx: fir_ext.Context) -> None:
    pass  # subscribed; no outbound call needed


@fir_ext.on("turn_end")
def on_turn_end(params: fir_ext.AgentLifecycleParams, ctx: fir_ext.Context) -> None:
    ctx.continue_session()  # continue_session


@fir_ext.on("message_start")
def on_message_start(params: fir_ext.AgentLifecycleParams, ctx: fir_ext.Context) -> None:
    pass


@fir_ext.on("message_end")
def on_message_end(params: fir_ext.MessageEndParams, ctx: fir_ext.Context) -> None:
    # Assistant messages now carry provider/model/usage so observers can
    # meter token + cost spend without parsing the transcript. User and
    # tool-result messages get only `role`. Older fir builds emitted no
    # params at all, so always treat fields as best-effort.
    if not params:
        return
    role = params.get("role", "")
    if role != "assistant":
        return
    u = params.get("usage") or {}
    cost = (u.get("cost") or {}).get("total", 0.0)
    print(
        f"demo: message_end role={role} "
        f"model={params.get('provider', '')}/{params.get('model', '')} "
        f"tokens={u.get('total_tokens', 0)} cost=${cost:.4f}",
        file=sys.stderr,
    )


@fir_ext.on("tool_execution_start")
def on_tool_execution_start(params: fir_ext.ToolExecutionStartParams, ctx: fir_ext.Context) -> None:
    name = params.get("tool_name", "")
    tcid = params.get("tool_call_id", "")
    if tcid:
        ctx.set_label(tcid, f"running:{name}")  # set_label


@fir_ext.on("tool_execution_end")
def on_tool_execution_end(params: fir_ext.ToolExecutionEndParams, ctx: fir_ext.Context) -> None:
    tcid = params.get("tool_call_id", "")
    if tcid:
        ctx.clear_label(tcid)  # clear_label


@fir_ext.on("provider_error")
def on_provider_error(params: fir_ext.ProviderErrorParams, ctx: fir_ext.Context) -> None:
    # Emitted when a turn ends in a provider/LLM error. `retryable` tells you
    # whether it is a transient class (overloaded/rate-limit/5xx/transport)
    # worth auto-resuming, vs a terminal error (auth/400/context-length).
    # `retry_after_ms` carries a provider-indicated delay when parseable.
    if not params:
        return
    print(
        f"demo: provider_error kind={params.get('kind', '')} "
        f"retryable={params.get('retryable', False)} "
        f"retry_after_ms={params.get('retry_after_ms', 0)} "
        f"model={params.get('provider', '')}/{params.get('model', '')}",
        file=sys.stderr,
    )


# ---------------------------------------------------------------------------
# Commands
# ---------------------------------------------------------------------------


@fir_ext.command(name="demo-echo", description="Echo arguments back as a TUI message")
def cmd_demo_echo(args: list, ctx: fir_ext.Context) -> fir_ext.CommandHookResult:
    msg = " ".join(args) if args else "(no arguments)"
    return {"message": f"demo-echo: {msg}"}


@fir_ext.command(
    name="demo-markdown",
    description="Echo arguments back as a high-contrast markdown response",
)
def cmd_demo_markdown(args: list, ctx: fir_ext.Context) -> fir_ext.CommandHookResult:
    """Demonstrate print_response + markdown: prose rendered in a high-contrast
    accent-bordered box in the main conversation area (see /advise)."""
    msg = " ".join(args) if args else "(no arguments)"
    return {
        "message": f"**demo-markdown:** {msg}",
        "print_response": True,
        "markdown": True,
    }


@fir_ext.tool(
    name="show_config_dirs",
    description="Return the host-advertised config dirs and demo's config path/contents.",
    parameters={"type": "object", "properties": {}},
)
def show_config_dirs(params, ctx):
    """Demonstrate fir_ext.config_dirs / load_config() / config_path()."""
    return {
        "config_dirs": list(fir_ext.config_dirs),
        "config_path": fir_ext.config_path(),
        "config": fir_ext.load_config(),
    }


# ---------------------------------------------------------------------------
# CLI verb (demonstrates `fir <verb>` dispatch — see docs/design/extension-cli-verbs.md)
# ---------------------------------------------------------------------------


@fir_ext.cli_verb("demo-cli", summary="Echo argv back via host.println")
def cli_demo(argv: list, host: fir_ext.Host) -> int:
    """Echo argv back through fir's real stdout. Returns 0.

    With ``--wake-after``, schedules a delayed ``host.wake()`` from a
    background thread that EOFs the stdin queue early — exercises the
    SDK's ``Host.wake`` method.
    """
    host.println("demo-cli argv:", *argv)
    if "--wake-after" in argv:
        # Force readline to return immediately even if no input is piped.
        # Tests use this to verify wake() unblocks a pending read.
        threading.Timer(0.05, host.wake).start()
        line = host.readline(timeout=2.0)
        host.println("demo-cli woke:", "EOF" if line is None else repr(line))
        return 0
    if not host.stdin_is_tty:
        # Forward stdin lines if piped in.
        for line in host.stdin_lines():
            host.println("demo-cli stdin:", line.rstrip("\n"))
    return 0


# ---------------------------------------------------------------------------
# Hosted provider (demonstrates extension-shipped AI provider)
# ---------------------------------------------------------------------------
#
# A minimal "echo" provider: no network, no model — proves the
# provider/* JSON-RPC surface end-to-end. fir registers a synthetic
# ``ext:echo`` Api and routes streaming completions for the declared
# models back to this extension via the @provider_stream handler below.

fir_ext.register_provider(
    fir_ext.Provider(
        id="echo",
        display_name="Echo",
        short_name="Echo",
        env_keys=fir_ext.EnvKeys(primary="ECHO_API_KEY"),
        default_model_id="echo-1",
        models=[
            fir_ext.Model(
                id="echo-1",
                name="Echo 1",
                context_window=10_000,
                max_tokens=4_096,
                input=["text"],
            ),
        ],
        supports_live_list=True,
    )
)


def _last_user_text(prompt: dict) -> str:
    """Pull the most recent user-message text from an ai.Context payload."""
    msgs = prompt.get("messages") or []
    for m in reversed(msgs):
        if m.get("role") != "user":
            continue
        content = m.get("content")
        if isinstance(content, str):
            return content
        if isinstance(content, list):
            parts: list[str] = [
                str(block.get("text", ""))
                for block in content
                if isinstance(block, dict) and block.get("type") == "text"
            ]
            if parts:
                return "\n".join(parts)
    return ""


@fir_ext.provider_stream("echo")
def echo_stream(params: fir_ext.ProviderStreamStartParams, ctx: fir_ext.Context):
    """Generator: emits a fake streamed completion echoing the last user message."""
    stream_id = params.get("stream_id", "")
    model_obj = params.get("model") or {}
    prompt = params.get("prompt") or {}

    last = _last_user_text(prompt)
    text = f"Echo: {last}" if last else "Echo: (no input)"

    final_msg = {
        "role": "assistant",
        "content": [{"type": "text", "text": text}],
        "api": model_obj.get("api", "ext:echo"),
        "provider": model_obj.get("provider", "echo"),
        "model": model_obj.get("id", "echo-1"),
        "usage": {
            "input": 1,
            "output": len(text),
            "cacheRead": 0,
            "cacheWrite": 0,
            "totalTokens": 1 + len(text),
            "cost": {
                "input": 0.0,
                "output": 0.0,
                "cacheRead": 0.0,
                "cacheWrite": 0.0,
                "total": 0.0,
            },
        },
        "stopReason": "stop",
        "timestamp": int(_time.time() * 1000),
    }

    yield {"type": "start", "partial": {**final_msg, "content": []}}
    yield {"type": "text_start", "contentIndex": 0}
    # Emit a couple of deltas so streaming is observable.
    if text:
        for chunk in (text[: len(text) // 2], text[len(text) // 2 :]):
            if fir_ext.is_cancelled(stream_id):
                # Cooperative cancel: terminate with an aborted error rather
                # than continuing to emit text_end + done.
                yield {
                    "type": "error",
                    "reason": "aborted",
                    "error": {
                        **final_msg,
                        "stopReason": "aborted",
                        "errorMessage": "cancelled by host",
                    },
                }
                return
            if chunk:
                yield {"type": "text_delta", "contentIndex": 0, "delta": chunk}
    yield {"type": "text_end", "contentIndex": 0, "content": text}
    yield {"type": "done", "reason": "stop", "message": final_msg}


@fir_ext.provider_list_models("echo")
def echo_list_models(params: fir_ext.ProviderListModelsParams, ctx: fir_ext.Context):
    """Stub live-list — returns the same single model the static catalogue declares."""
    return ["echo-1"]


fir_ext.run(name="demo")
