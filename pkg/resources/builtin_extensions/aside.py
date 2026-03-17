#!/usr/bin/env python3
# ---
# name: aside
# description: Ephemeral side queries and multi-tool orchestration — off the record
# builtin: true
# commands: aside: Ask a side question or run tools ephemerally (e.g. /aside what does that error mean?)
# ---
"""aside.py — ephemeral side queries and multi-tool orchestration.

Provides an ``/aside`` slash command and an ``aside`` tool that let the agent
(or user) ask a side question or execute a list of tool calls, collect their
outputs without polluting the main conversation, and synthesise the results
via a one-shot LLM call.

Everything happens *off to the side* — ephemerally, without entering history.
Whether it's a quick question or a multi-tool data gather, it's an aside.

Architecture:
  1. If no tools are provided, run a pure side query (like the old /btw).
  2. Otherwise, execute each tool sequentially via ``ctx.call_tool()``.
     - Results are held in local Python memory — never enter history.
  3. Build a synthesis prompt from collected outputs + user instructions.
  4. Run ``ctx.side_query()`` to get an ephemeral LLM summary.
  5. Return only the summary.
"""

from __future__ import annotations

import fir_ext

# ---------------------------------------------------------------------------
# Helper: extract text from a tool result
# ---------------------------------------------------------------------------


def _result_text(result: dict) -> str:
    """Extract text content from a call_tool result dict."""
    content = result.get("content", [])
    if isinstance(content, list):
        parts = []
        for block in content:
            if isinstance(block, dict):
                text = block.get("text") or block.get("Text", "")
                if text:
                    parts.append(text)
            elif isinstance(block, str):
                parts.append(block)
        return "\n".join(parts)
    if isinstance(content, str):
        return content
    return str(result)


def _build_synthesis_prompt(
    results: list[dict],
    instructions: str,
) -> str:
    """Build the prompt sent to side_query() for synthesis."""
    parts = [
        "You are processing the outputs of multiple tool calls. "
        "Below are the results, followed by instructions on what "
        "to return.\n"
    ]
    for i, r in enumerate(results, 1):
        name = r["name"]
        error_tag = " [ERROR]" if r.get("is_error") else ""
        parts.append(f"--- Tool {i}: {name}{error_tag} ---")
        parts.append(r["output"])
        parts.append("")
    parts.append("--- Instructions ---")
    parts.append(instructions)
    return "\n".join(parts)


# ---------------------------------------------------------------------------
# Core: run an aside — side query with optional tool calls
# ---------------------------------------------------------------------------


def _run_aside(
    tools: list[dict],
    instructions: str,
    ctx: fir_ext.Context,
) -> dict:
    """Execute *tools*, collect outputs, synthesise via side_query().

    Parameters
    ----------
    tools : list of dict
        Each entry has ``"name"`` (str) and optional ``"params"`` (dict).
        If empty, runs a pure side query (like the old /btw).
    instructions : str
        Synthesis instructions for the LLM.
    ctx : fir_ext.Context
        Extension context for call_tool / side_query.

    Returns
    -------
    dict
        Structured tool result with ``content`` and ``is_error``.
    """
    if not instructions:
        return _error("instructions are required")

    # No tools — pure ephemeral side query.
    if not tools:
        try:
            synthesis = ctx.side_query(instructions)
        except Exception as exc:
            return _side_query_error(exc)
        return {
            "content": [{"type": "text", "text": synthesis}],
            "is_error": False,
        }

    # Validate tool names and params upfront.
    available = ctx.list_tools()
    tool_index = {t["name"]: t for t in available}
    # Build a case-insensitive lookup so that provider-transformed names
    # (e.g. "Read" from Anthropic OAuth) resolve to internal names ("read").
    tool_index_lower = {t["name"].lower(): t for t in available}
    available_names = sorted(tool_index.keys())

    # Normalise tool names in-place before validation.
    for spec in tools:
        name = spec.get("name", "")
        if name and name not in tool_index and name.lower() in tool_index_lower:
            spec["name"] = tool_index_lower[name.lower()]["name"]

    errors = []
    for i, spec in enumerate(tools, 1):
        name = spec.get("name", "")
        if not name:
            errors.append(f"tools[{i}]: name is required")
            continue
        if name not in tool_index:
            errors.append(
                f"tools[{i}]: tool {name!r} not found. Available: {', '.join(available_names)}"
            )
            continue
        # Validate required params against schema.
        schema = tool_index[name].get("parameters") or {}
        required = schema.get("required") or []
        params = spec.get("params") or {}
        missing = [r for r in required if r not in params]
        if missing:
            errors.append(f"tools[{i}] ({name}): missing required params: " + ", ".join(missing))

    if errors:
        return _error("Validation failed:\n" + "\n".join(errors))

    results: list[dict] = []

    for spec in tools:
        name = spec["name"]
        params = spec.get("params") or {}

        # Call the tool via the bridge.
        try:
            result = ctx.call_tool(name, params)
        except Exception as exc:
            results.append(
                {
                    "name": name,
                    "output": f"error calling tool: {exc}",
                    "is_error": True,
                }
            )
            continue

        is_error = result.get("is_error", False)
        output = _result_text(result)
        results.append(
            {
                "name": name,
                "output": output,
                "is_error": is_error,
            }
        )

    # Synthesise collected outputs.
    prompt = _build_synthesis_prompt(results, instructions)
    try:
        synthesis = ctx.side_query(prompt)
    except Exception as exc:
        return _side_query_error(exc)

    return {
        "content": [{"type": "text", "text": synthesis}],
        "is_error": False,
    }


def _error(msg: str) -> dict:
    return {
        "content": [{"type": "text", "text": msg}],
        "is_error": True,
    }


def _side_query_error(exc: Exception) -> dict:
    """Return a structured is_error result for a side_query LLM failure.

    The error message uses the 'side-query: ...' prefix that SideQuery
    attaches, so the main LLM receives a clear, attributable message rather
    than a raw API error string.  Context-overflow errors get an extra hint
    so the LLM knows to simplify the request.
    """
    msg = str(exc)
    hint = ""
    _overflow_markers = (
        "context window",
        "context length",
        "maximum context",
        "token limit",
        "too many tokens",
        "exceeds",
    )
    if any(m in msg.lower() for m in _overflow_markers):
        hint = " (context window full — try fewer tools or a simpler question)"
    return _error(f"aside LLM call failed{hint}: {msg}")


def _side_query_error_text(exc: Exception) -> str:
    """Convenience wrapper: return the error text from _side_query_error."""
    return _side_query_error(exc)["content"][0]["text"]


# ---------------------------------------------------------------------------
# Tool: aside
# ---------------------------------------------------------------------------


@fir_ext.tool(
    name="aside",
    description=(
        "Ephemeral side query with optional multi-tool orchestration. "
        "Everything happens off to the side — nothing enters conversation "
        "history, only the synthesis is returned.\n\n"
        "With tools: executes them, collects outputs, synthesises via LLM.\n"
        "Without tools: runs a pure side question against current context.\n\n"
        "Use when you need to gather data from several tools and want "
        "a concise summary without bloating context, or when you want "
        "to ask a quick side question."
    ),
    parameters={
        "type": "object",
        "properties": {
            "title": {
                "type": "string",
                "description": "Brief label for this aside (shown in UI).",
            },
            "tools": {
                "type": "array",
                "description": "Ordered list of tool calls. Omit for a pure side question.",
                "items": {
                    "type": "object",
                    "properties": {
                        "name": {
                            "type": "string",
                            "description": "Name of the tool to call.",
                        },
                        "params": {
                            "type": "object",
                            "description": "Tool parameters.",
                        },
                    },
                    "required": ["name"],
                },
            },
            "instructions": {
                "type": "string",
                "description": "Instructions for the LLM that synthesises collected outputs, or the side question to ask.",
            },
        },
        "required": ["title", "instructions"],
    },
    display_hint={
        "title_args": [{"name": "title", "style": "accent"}],
    },
)
def aside(params: dict, ctx: fir_ext.Context):
    tools = params.get("tools", [])
    instructions = params.get("instructions", "")
    return _run_aside(tools, instructions, ctx)


# ---------------------------------------------------------------------------
# Command: /aside
# ---------------------------------------------------------------------------


@fir_ext.command(
    name="aside",
    description=(
        "Ask a side question or run tools ephemerally. "
        "Usage: /aside <question or description of what to do>"
    ),
)
def cmd_aside(args: list[str], ctx: fir_ext.Context):
    """Handle /aside — either a direct side question or a tool orchestration request."""
    text = " ".join(args).strip()
    if not text:
        return {
            "message": (
                "Usage: /aside <question or description>\n\n"
                "Examples:\n"
                "  /aside what does that error mean?\n"
                "  /aside read the 5 largest .go files and summarise their purpose"
            ),
        }

    # Heuristic: if the text looks like a direct question (short, no tool
    # keywords), handle it as a pure side query like the old /btw.
    # Otherwise, instruct the agent to use the aside tool with tools.
    words = text.split()
    looks_like_tool_request = any(
        kw in text.lower()
        for kw in ["read ", "file", "grep", "find ", "bash ", "run ", "execute", "search"]
    )

    if not looks_like_tool_request or len(words) <= 8:
        # Pure side question — answer directly.
        try:
            answer = ctx.side_query(text)
        except Exception as exc:
            return {"message": _side_query_error_text(exc)}
        return {"message": f"aside: {text}\n\n{answer}"}

    # Looks like a multi-tool request — delegate to agent.
    prompt = (
        f"Use the aside tool to accomplish the following. "
        f"Build the appropriate tool list and instructions, "
        f"then call aside:\n\n{text}"
    )
    ctx.send_user_message(prompt)
    return {}


fir_ext.run(name="aside")
