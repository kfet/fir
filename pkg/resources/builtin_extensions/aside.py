#!/usr/bin/env python3
# ---
# name: aside
# description: Ephemeral side queries and multi-tool orchestration — off the record
# builtin: true
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

Advisor escalation
------------------

The ``aside`` tool grows an extra ``escalate: bool`` parameter when an
advisor model is in effect.  Setting it to ``true`` routes the side query
to the advisor instead of the executor's own model — the "advisor
strategy" pattern: a small, fast executor that escalates hard decisions
to a stronger advisor without entering history.

By default fir's bundled top-tier Anthropic Opus is used as the advisor,
so the feature works out of the box with zero config (Anthropic auth
required).  Override or disable it with one line:

    /aside-advisor anthropic/claude-opus-4-x          # pin a model
    /aside-advisor anthropic/claude-opus-4-x:high     # with effort
    /aside-advisor                                    # show current
    /aside-advisor off                                # disable escalation

Stored in the highest-priority config dir advertised by the host (project-local
``.fir/aside.json`` overrides ``~/.config/fir/aside.json``).  Read lazily on
first use so the host's config dirs are available from the init handshake.
The ``escalate`` parameter only appears in the tool schema when an advisor
is in effect, so users who explicitly disable it see no extra surface.
Changes take effect on the next session start.

Steering the executor
---------------------

The executor is guided toward the advisor pattern via the built-in
``aside-advisor`` skill rather than a session_start prepend.  The skill's
``[SYS_EXT]`` description appears in the base system prompt's
``<available_skills>`` block on every turn, making it more persistent than
a prepended history message which can drift away during compaction.
The skill body provides detailed examples and decision guidance.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import fir_ext

# ---------------------------------------------------------------------------
# Advisor configuration — read once at module load
# ---------------------------------------------------------------------------

_CONFIG_FILENAME = "aside.json"

# Default advisor when no config file exists. Always points at the highest
# Anthropic Opus tier baked into fir's model registry. Drift is caught by
# DefaultAdvisorTracksHighestAnthropicOpus in pkg/resources/testdata/aside_test.py
# — bump this constant when fir adds a newer Opus.
_DEFAULT_ADVISOR_SPEC = "anthropic/claude-opus-4-7"


def _config_path() -> Path | None:
    """Highest-priority path for reading/writing aside.json. None when the
    SDK has no config dirs (e.g. tests that haven't seeded any)."""
    p = fir_ext.config_path(_CONFIG_FILENAME)
    return Path(p) if p else None


def _read_existing_config() -> dict | None:
    """Read the existing aside.json from the highest-priority dir that has
    one. Returns the parsed dict, or None if no file exists / unparsable."""
    return fir_ext.load_config(_CONFIG_FILENAME)


def _load_advisor_config() -> dict[str, str] | None:
    """Read advisor model config from aside.json in the host-provided dirs.

    Returns a dict with ``provider``, ``model`` and optional ``effort`` keys,
    or ``None`` if no advisor is configured.  Malformed files are ignored
    silently — the extension falls back to the default in that case.

    Resolution order:
      1. Explicit ``"advisor": null`` (or ``"advisor": "off"``) → no advisor.
      2. Explicit ``"advisor": "<spec>"`` → use it (validated; falls through
         to default on parse failure).
      3. File missing or unparsable → use the bundled default.
    """
    data = _read_existing_config()
    if isinstance(data, dict) and "advisor" in data:
        advisor = data["advisor"]
        # Explicit opt-out.
        if advisor is None or (isinstance(advisor, str) and advisor.strip().lower() in ("", "off", "none")):
            return None
        if isinstance(advisor, str):
            parsed = _parse_advisor_spec(advisor)
            if parsed is not None:
                return parsed
        # Malformed entry — fall through to default rather than disable.

    # Default: highest Anthropic Opus baked into fir at build time.
    return _parse_advisor_spec(_DEFAULT_ADVISOR_SPEC)


def _parse_advisor_spec(spec: str) -> dict[str, str] | None:
    """Parse a ``provider/model[:effort]`` advisor spec string.

    Returns a dict with ``provider``, ``model`` and optional ``effort``,
    or ``None`` if the spec is malformed (missing ``/``).
    """
    spec = spec.strip()
    if "/" not in spec:
        return None
    head, _, effort = spec.partition(":")
    provider, _, model = head.partition("/")
    provider = provider.strip()
    model = model.strip()
    effort = effort.strip()
    if not provider or not model:
        return None
    out: dict[str, str] = {"provider": provider, "model": model}
    if effort:
        out["effort"] = effort
    return out


def _format_advisor_spec(cfg: dict[str, str]) -> str:
    """Inverse of _parse_advisor_spec — render a config dict back to a string."""
    base = f"{cfg['provider']}/{cfg['model']}"
    effort = cfg.get("effort")
    return f"{base}:{effort}" if effort else base


# Lazily-loaded advisor config — populated on first access (after the init
# handshake has set fir_ext.config_dirs). Tests that want to inject a value
# can assign to _ADVISOR directly.
_ADVISOR_UNSET = object()
_ADVISOR: Any = _ADVISOR_UNSET


def _advisor() -> dict[str, str] | None:
    global _ADVISOR
    if _ADVISOR is _ADVISOR_UNSET:
        _ADVISOR = _load_advisor_config()
    return _ADVISOR


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
    escalate: bool = False,
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
    escalate : bool
        When True (and an advisor model is configured), route the side query
        to the advisor model instead of the agent's current model.  Ignored
        when no advisor is configured.

    Returns
    -------
    dict
        Structured tool result with ``content`` and ``is_error``.
    """
    if not instructions:
        return _error("instructions are required")

    # Resolve advisor override if escalate is requested and configured.
    advisor_used: dict[str, str] | None = None
    sq_model: str | None = None
    sq_provider: str | None = None
    sq_effort: str | None = None
    advisor = _advisor()
    if escalate and advisor is not None:
        sq_model = advisor["model"]
        sq_provider = advisor["provider"]
        sq_effort = advisor.get("effort")
        advisor_used = advisor

    # No tools — pure ephemeral side query.
    if not tools:
        try:
            synthesis = ctx.side_query(instructions, model=sq_model, provider=sq_provider, effort=sq_effort)
        except Exception as exc:
            return _side_query_error(exc)
        return {
            "content": [{"type": "text", "text": _prefix_advisor(synthesis, advisor_used)}],
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
        title = spec.get("title", "")
        params = spec.get("params") or {}

        # Report progress to the UI spinner.
        label = f"Calling {name}" + (f" — {title}" if title else "")
        ctx.report_progress(label)

        # Call the tool via the bridge.
        try:
            result = ctx.call_tool(name, params)
        except Exception as exc:
            results.append(
                {
                    "name": name,
                    "title": title,
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
                "title": title,
                "output": output,
                "is_error": is_error,
            }
        )

    # Synthesise collected outputs.
    ctx.report_progress("Synthesizing...")
    prompt = _build_synthesis_prompt(results, instructions)
    try:
        synthesis = ctx.side_query(prompt, model=sq_model, provider=sq_provider, effort=sq_effort)
    except Exception as exc:
        return _side_query_error(exc)

    # Include raw tool outputs in details for TUI display (not sent to LLM).
    # Truncate individual outputs to avoid bloating the JSON-RPC response.
    max_output_len = 50 * 1024  # 50KB per tool output
    tool_outputs = []
    for r in results:
        output = r["output"]
        if len(output) > max_output_len:
            output = output[:max_output_len] + "\n... (truncated)"
        tool_outputs.append({
            "name": r["name"],
            "title": r.get("title", ""),
            "output": output,
            "is_error": r.get("is_error", False),
        })

    return {
        "content": [{"type": "text", "text": _prefix_advisor(synthesis, advisor_used)}],
        "is_error": False,
        "details": {"tool_outputs": tool_outputs},
    }


def _prefix_advisor(text: str, advisor: dict[str, str] | None) -> str:
    """Prefix the synthesis with a single trace line when escalation was used.

    The trace makes advisor invocations visible to both user and agent —
    the agent sees that the response came from a stronger model, and the
    user sees what was billed.
    """
    if advisor is None:
        return text
    spec = _format_advisor_spec(advisor)
    return f"[advisor: {spec}]\n\n{text}"


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


def _aside_tool_description() -> str:
    """Build the aside tool description, growing escalation guidance only when configured."""
    base = (
        "Ephemeral side query with optional multi-tool orchestration. "
        "Everything happens off to the side — nothing enters conversation "
        "history, only the synthesis is returned.\n\n"
        "With tools: executes them, collects outputs, synthesises via LLM.\n"
        "Without tools: runs a pure ephemeral side question against current context.\n\n"
        "Use your fast (current) model with this tool to gather data, collect context, "
        "ask quick questions, or investigate issues without polluting history."
    )
    if _advisor() is None:
        return base
    return base + (
        "\n\nAdvisor escalation: set 'escalate' to true to route this side query "
        "to a stronger advisor model. See the session-start [SYS_EXT] note for "
        "when escalation is warranted — the principle is judgement-call cost, "
        "not a checklist of categories."
    )


def _aside_tool_parameters() -> dict[str, Any]:
    """Build the aside tool's parameter schema, adding 'escalate' only when configured."""
    schema: dict[str, Any] = {
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
                        "title": {
                            "type": "string",
                            "description": "Short description of what this tool call does (shown in UI).",
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
    }
    if _advisor() is not None:
        schema["properties"]["escalate"] = {
            "type": "boolean",
            "description": (
                "When true, route this side query to the configured advisor "
                "model instead of the executor's current model. Use sparingly "
                "— see the tool description for when escalation is warranted."
            ),
        }
    return schema


@fir_ext.tool(
    name="aside",
    description=_aside_tool_description(),
    parameters=_aside_tool_parameters(),
    display_hint={
        "title_args": [{"name": "title", "style": "accent"}],
    },
)
def aside(params: dict, ctx: fir_ext.Context):
    tools = params.get("tools", [])
    instructions = params.get("instructions", "")
    escalate = bool(params.get("escalate", False))
    return _run_aside(tools, instructions, ctx, escalate=escalate)


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


# ---------------------------------------------------------------------------
# Command: /aside-advisor
# ---------------------------------------------------------------------------


def _save_advisor_config(cfg: dict[str, str] | None) -> str | None:
    """Persist *cfg* to the advisor config file. Returns an error string on failure.

    When *cfg* is ``None``, persists the explicit opt-out marker
    (``"advisor": "off"``) so the absence of a file remains the "use default
    advisor" signal. This keeps the contract simple:

      file missing       → use built-in default advisor
      "advisor": "off"   → escalation disabled
      "advisor": "p/m"   → user-pinned advisor
    """
    cfg_path = _config_path()
    if cfg_path is None:
        return "no config dir advertised by host; cannot persist advisor config"
    try:
        cfg_path.parent.mkdir(parents=True, exist_ok=True)
        existing: dict[str, Any] = {}
        loaded = _read_existing_config()
        if isinstance(loaded, dict):
            existing = loaded
        if cfg is None:
            existing["advisor"] = "off"
        else:
            existing["advisor"] = _format_advisor_spec(cfg)
        cfg_path.write_text(json.dumps(existing, indent=2) + "\n")
        return None
    except OSError as exc:
        return f"failed to write {cfg_path}: {exc}"


@fir_ext.command(
    name="aside-advisor",
    description=(
        "Show, set, or unset the advisor model used by aside's escalate flag. "
        "Usage: /aside-advisor [provider/model[:effort] | off]"
    ),
)
def cmd_aside_advisor(args: list[str], ctx: fir_ext.Context):
    """Handle /aside-advisor — manage the persisted advisor model config."""
    spec = " ".join(args).strip()

    # Show current.
    if not spec:
        advisor = _advisor()
        if advisor is None:
            return {
                "message": (
                    "aside-advisor: disabled (advisor: off in aside.json).\n\n"
                    "Set one with:\n"
                    "  /aside-advisor anthropic/claude-opus-4-x\n"
                    "  /aside-advisor anthropic/claude-opus-4-x:high\n\n"
                    "Changes take effect on the next session start."
                ),
            }
        cfg_path = _config_path()
        is_default = cfg_path is None or not cfg_path.is_file()
        suffix = " (default — no aside.json)" if is_default else f" (from {cfg_path})"
        return {
            "message": (
                f"aside-advisor: {_format_advisor_spec(advisor)}{suffix}\n\n"
                "Override:  /aside-advisor <provider>/<model>[:effort]\n"
                "Disable:   /aside-advisor off"
            ),
        }

    # Unset.
    if spec.lower() in ("off", "none", "unset", "clear"):
        err = _save_advisor_config(None)
        if err:
            return {"message": f"aside-advisor: {err}"}
        return {
            "message": (
                "aside-advisor: disabled. The 'escalate' parameter will be "
                "removed from the aside tool on next session start. "
                "Run `/aside-advisor <provider>/<model>` to re-enable, or "
                "delete aside.json to return to the built-in default."
            ),
        }

    # Set.
    parsed = _parse_advisor_spec(spec)
    if parsed is None:
        return {
            "message": (
                f"aside-advisor: malformed spec {spec!r}.\n"
                "Expected 'provider/model' or 'provider/model:effort' "
                "(e.g. 'anthropic/claude-opus-4-x:high')."
            ),
        }
    err = _save_advisor_config(parsed)
    if err:
        return {"message": f"aside-advisor: {err}"}
    return {
        "message": (
            f"aside-advisor: set to {_format_advisor_spec(parsed)}.\n"
            "Changes take effect on the next session start."
        ),
    }


fir_ext.run(name="aside")
