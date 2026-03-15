#!/usr/bin/env python3
# ---
# name: batch
# description: Execute multiple tools and synthesise their outputs ephemerally
# builtin: true
# commands: batch: Run tools and synthesise results (e.g. /batch read 3 files and summarise)
# ---
"""batch.py — ephemeral multi-tool orchestration via the fir extension SDK.

Provides a `/batch` slash command and a `batch_run` tool that let the agent
(or user) execute a list of tool calls, collect their outputs without
polluting the main conversation, and synthesise the results via a one-shot
LLM call.

This is the extension counterpart to the built-in Go `batch` tool.  It
demonstrates how extensions can use ``ctx.call_tool()`` and ``ctx.btw()``
to build rich orchestration workflows in Python.

Architecture:
  1. Parse the tool list from the request.
  2. Execute each tool sequentially via ``ctx.call_tool()``.
     - Results are held in local Python memory — never enter history.
  3. Build a synthesis prompt from collected outputs + user instructions.
  4. Run ``ctx.btw()`` to get an ephemeral LLM summary.
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
    """Build the prompt sent to btw() for synthesis."""
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
# Core: run a batch of tool calls and synthesise
# ---------------------------------------------------------------------------


def _run_batch(
    tools: list[dict],
    instructions: str,
    ctx: fir_ext.Context,
    description: str = "",
) -> dict:
    """Execute *tools*, collect outputs, synthesise via btw().

    Parameters
    ----------
    tools : list of dict
        Each entry has ``"name"`` (str) and optional ``"params"`` (dict).
    instructions : str
        Synthesis instructions for the LLM.
    ctx : fir_ext.Context
        Extension context for call_tool / btw.
    description : str, optional
        Human-readable label for progress messages.

    Returns
    -------
    dict
        Structured tool result with ``content`` and ``is_error``.
    """
    if not tools:
        return _error("tools list is empty")
    if not instructions:
        return _error("instructions are required")

    results: list[dict] = []

    for i, spec in enumerate(tools, 1):
        name = spec.get("name", "")
        if not name:
            results.append({
                "name": f"(unnamed tool #{i})",
                "output": "error: tool name is required",
                "is_error": True,
            })
            continue

        params = spec.get("params") or {}

        # Call the tool via the bridge.
        try:
            result = ctx.call_tool(name, params)
        except Exception as exc:
            results.append({
                "name": name,
                "output": f"error calling tool: {exc}",
                "is_error": True,
            })
            continue

        is_error = result.get("is_error", False)
        output = _result_text(result)
        results.append({
            "name": name,
            "output": output,
            "is_error": is_error,
        })

    # Synthesise collected outputs.
    prompt = _build_synthesis_prompt(results, instructions)
    try:
        synthesis = ctx.btw(prompt)
    except Exception as exc:
        return _error(f"synthesis failed: {exc}")

    return {
        "content": [{"type": "text", "text": synthesis}],
        "is_error": False,
    }


def _error(msg: str) -> dict:
    return {
        "content": [{"type": "text", "text": msg}],
        "is_error": True,
    }


# ---------------------------------------------------------------------------
# Tool: batch_run
# ---------------------------------------------------------------------------


@fir_ext.tool(
    name="batch_run",
    description=(
        "Execute multiple tools and synthesise their outputs via an "
        "ephemeral LLM call. Raw tool outputs never enter conversation "
        "history — only the synthesis is returned.\n\n"
        "Use when you need to gather data from several tools and want "
        "a concise summary without bloating context."
    ),
    parameters={
        "type": "object",
        "properties": {
            "description": {
                "type": "string",
                "description": "Brief label for this batch (shown in UI).",
            },
            "tools": {
                "type": "array",
                "description": "Ordered list of tool calls.",
                "items": {
                    "type": "object",
                    "properties": {
                        "name": {
                            "type": "string",
                            "description": "Tool name.",
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
                "description": (
                    "Instructions for the LLM that synthesises "
                    "collected outputs."
                ),
            },
        },
        "required": ["tools", "instructions"],
    },
)
def batch_run(params: dict, ctx: fir_ext.Context):
    tools = params.get("tools", [])
    instructions = params.get("instructions", "")
    description = params.get("description", "")
    return _run_batch(tools, instructions, ctx, description)


# ---------------------------------------------------------------------------
# Command: /batch
# ---------------------------------------------------------------------------


@fir_ext.command(
    name="batch",
    description=(
        "Run tools and synthesise results. "
        "Usage: /batch <natural language description of what to do>"
    ),
)
def cmd_batch(args: list[str], ctx: fir_ext.Context):
    """Handle /batch — pass the request to the agent as a user message
    instructing it to use the batch_run tool."""
    text = " ".join(args).strip()
    if not text:
        return {
            "message": (
                "Usage: /batch <description>\n"
                "Example: /batch read the 5 largest .go files "
                "and summarise their purpose"
            ),
        }
    prompt = (
        f"Use the batch_run tool to accomplish the following. "
        f"Build the appropriate tool list and instructions, "
        f"then call batch_run:\n\n{text}"
    )
    ctx.send_user_message(prompt)
    return {}


fir_ext.run(name="batch")
