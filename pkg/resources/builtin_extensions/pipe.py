#!/usr/bin/env python3
# ---
# name: pipe
# description: Chain multiple tool calls in one turn — no intermediate LLM round-trips
# builtin: true
# ---
"""pipe.py — chain multiple tool calls in a single agent turn.

Lets the agent execute a list of {tool, params} steps sequentially without an
LLM round-trip between each one. Step parameters can reference outputs of
previous steps via simple ``{{prev}}`` / ``{{step:N}}`` / ``{{step:N.field}}``
substitution tokens.

If only one step is provided, the raw tool result is returned unchanged
(transparent passthrough). For multiple steps, results are concatenated into a
markdown block, one section per step.
"""

from __future__ import annotations

import json
import re
from typing import TYPE_CHECKING, Any

import fir_ext

if TYPE_CHECKING:
    from collections.abc import Mapping


# Match tool outputs at this size before substitution feed-forward and final
# markdown rendering, to mirror aside.py and keep the JSON-RPC envelope sane.
_MAX_OUTPUT_LEN = 50 * 1024


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _result_text(result: Mapping[str, Any]) -> str:
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


def _truncate(text: str) -> str:
    """Cap *text* at _MAX_OUTPUT_LEN, appending a marker when cut."""
    if len(text) > _MAX_OUTPUT_LEN:
        return text[:_MAX_OUTPUT_LEN] + "\n... (truncated)"
    return text


# Match {{prev}}, {{prev.field}}, {{step:N}}, {{step:N.field}}.
_TOKEN_RE = re.compile(r"\{\{\s*(prev|step:(\d+))(?:\.([A-Za-z_][\w.]*))?\s*\}\}")


def _lookup_field(text: str, path: str) -> str:
    """Try to parse text as JSON and walk a dotted field path through dict
    keys. Returns the raw text on parse failure or any missing/non-dict
    segment. Array indexing is intentionally not supported — keep it simple."""
    try:
        data = json.loads(text)
    except (ValueError, TypeError):
        return text
    cur: Any = data
    for part in path.split("."):
        if isinstance(cur, dict) and part in cur:
            cur = cur[part]
        else:
            return text
    if isinstance(cur, str):
        return cur
    return json.dumps(cur)


def _substitute(value: Any, prior: list[str]) -> Any:
    """Recursively substitute template tokens in strings inside a params value."""
    if isinstance(value, str):
        def repl(m: re.Match[str]) -> str:
            kind = m.group(1)
            field = m.group(3)
            if kind == "prev":
                text = prior[-1] if prior else ""
            else:
                idx = int(m.group(2))
                # Out-of-range step index → empty string.
                text = prior[idx] if 0 <= idx < len(prior) else ""
            if field:
                return _lookup_field(text, field)
            return text

        return _TOKEN_RE.sub(repl, value)
    if isinstance(value, dict):
        return {k: _substitute(v, prior) for k, v in value.items()}
    if isinstance(value, list):
        return [_substitute(v, prior) for v in value]
    return value


def _error(msg: str) -> dict:
    return {"content": [{"type": "text", "text": msg}], "is_error": True}


# ---------------------------------------------------------------------------
# Core orchestration
# ---------------------------------------------------------------------------


def _run_pipe(steps: list[dict], label: str, ctx: fir_ext.Context) -> dict:
    if not isinstance(steps, list) or not steps:
        return _error("pipe: steps must be a non-empty array")

    # Shape validation up front so a bad spec fails fast.
    for i, step in enumerate(steps):
        if not isinstance(step, dict):
            return _error(f"pipe: steps[{i}] must be an object")
        if not step.get("tool"):
            return _error(f"pipe: steps[{i}] missing 'tool'")

    # Resolve and validate tool names + required params against the live
    # registry before firing any call. Mirrors aside.py's approach.
    try:
        available = ctx.list_tools()
    except Exception:
        available = []
    tool_index = {t["name"]: t for t in available if t.get("name")}
    tool_index_lower = {t["name"].lower(): t for t in available if t.get("name")}
    available_names = sorted(tool_index.keys())

    # Normalise tool names in-place (case-insensitive) so downstream code
    # uses the canonical name.
    for step in steps:
        name = step["tool"]
        if name not in tool_index and name.lower() in tool_index_lower:
            step["tool"] = tool_index_lower[name.lower()]["name"]

    errors: list[str] = []
    for i, step in enumerate(steps):
        name = step["tool"]
        if name not in tool_index:
            errors.append(
                f"steps[{i}]: tool {name!r} not found. "
                f"Available: {', '.join(available_names) if available_names else '(none)'}"
            )
            continue
        schema = tool_index[name].get("parameters") or {}
        required = schema.get("required") or []
        params = step.get("params") or {}
        missing = [r for r in required if r not in params]
        if missing:
            errors.append(
                f"steps[{i}] ({name}): missing required params: " + ", ".join(missing)
            )

    if errors:
        return _error("pipe validation failed:\n" + "\n".join(errors))

    prior_text: list[str] = []
    results: list[dict] = []
    any_error = False

    for i, step in enumerate(steps):
        name = step["tool"]
        raw_params = step.get("params") or {}
        params = _substitute(raw_params, prior_text)
        cont = bool(step.get("continue_on_error", False))

        progress = f"pipe[{i + 1}/{len(steps)}] {name}"
        if label:
            progress = f"{label}: {progress}"
        ctx.report_progress(progress)

        try:
            result = ctx.call_tool(name, params)
        except Exception as exc:
            text = _truncate(f"error calling tool: {exc}")
            results.append({"name": name, "output": text, "is_error": True})
            prior_text.append(text)
            any_error = True
            if cont:
                continue
            return _format_error(i, name, results)

        is_error = bool(result.get("is_error", False))
        text = _truncate(_result_text(result))
        results.append({"name": name, "output": text, "is_error": is_error})
        prior_text.append(text)
        if is_error:
            any_error = True
            if not cont:
                return _format_error(i, name, results)

    return _format_success(results, any_error)


def _format_error(idx: int, name: str, results: list[dict]) -> dict:
    body = _format_results_markdown(results)
    msg = f"pipe aborted at step {idx + 1} ({name}):\n\n{body}"
    return {"content": [{"type": "text", "text": msg}], "is_error": True}


def _format_success(results: list[dict], any_error: bool) -> dict:
    if len(results) == 1:
        # Transparent passthrough for a single step.
        r = results[0]
        return {
            "content": [{"type": "text", "text": r["output"]}],
            "is_error": bool(r["is_error"]),
        }
    return {
        "content": [{"type": "text", "text": _format_results_markdown(results)}],
        "is_error": any_error,
    }


def _format_results_markdown(results: list[dict]) -> str:
    parts: list[str] = []
    for i, r in enumerate(results, 1):
        tag = " [ERROR]" if r["is_error"] else ""
        parts.append(f"## Step {i}: {r['name']}{tag}")
        parts.append(r["output"])
        parts.append("")
    return "\n".join(parts).rstrip() + "\n"


# ---------------------------------------------------------------------------
# Tool registration
# ---------------------------------------------------------------------------


_PIPE_DESCRIPTION = (
    "Chain multiple tool calls in a single turn — no intermediate LLM "
    "round-trips. Steps run sequentially and can reference earlier outputs "
    "via {{prev}}, {{step:N}} (0-indexed), or {{step:N.field}} for JSON "
    "field access. Field paths walk dict keys only (no array indexing); "
    "an out-of-range {{step:N}} substitutes an empty string. Aborts on "
    "the first error unless that step has continue_on_error: true. Each "
    "step's output is capped at 50KB. Returns the raw result for a single "
    "step, or a markdown block of all step outputs otherwise.\n\n"
    "[SYS_EXT] Reach for pipe when you already know the full chain of "
    "tool calls upfront and intermediate outputs are bulky or only the "
    "final result matters — it skips the LLM round-trips and avoids "
    "polluting context with throwaway intermediate data."
)

_PIPE_PARAMETERS: dict[str, Any] = {
    "type": "object",
    "properties": {
        "label": {
            "type": "string",
            "description": "Optional short label shown in UI progress.",
        },
        "steps": {
            "type": "array",
            "minItems": 1,
            "description": "Ordered list of tool calls to execute.",
            "items": {
                "type": "object",
                "properties": {
                    "tool": {
                        "type": "string",
                        "description": "Name of the tool to call.",
                    },
                    "params": {
                        "type": "object",
                        "description": (
                            "Parameters for the tool. String values may "
                            "contain {{prev}}, {{step:N}}, or "
                            "{{step:N.field}} substitution tokens."
                        ),
                    },
                    "continue_on_error": {
                        "type": "boolean",
                        "description": (
                            "If true, continue the pipeline when this step "
                            "errors instead of aborting."
                        ),
                    },
                },
                "required": ["tool"],
            },
        },
    },
    "required": ["steps"],
}


@fir_ext.tool(
    name="pipe",
    description=_PIPE_DESCRIPTION,
    parameters=_PIPE_PARAMETERS,
    display_hint={
        "title_args": [{"name": "label", "style": "accent"}],
    },
)
def pipe(params: dict, ctx: fir_ext.Context):
    steps = params.get("steps") or []
    label = params.get("label", "") or ""
    return _run_pipe(steps, label, ctx)


fir_ext.run(name="pipe")
