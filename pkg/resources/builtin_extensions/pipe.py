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

import contextlib
import json
import os
import re
import shlex
import tempfile
import time
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


def _collect_refs(value: Any, current_idx: int, refs: set[int]) -> None:
    """Walk a params value and record which earlier step indices it references.

    Only refs strictly *earlier* than ``current_idx`` count — self-references
    and forward references substitute to an empty string at runtime and do
    not actually consume any step's output."""
    if isinstance(value, str):
        for m in _TOKEN_RE.finditer(value):
            kind = m.group(1)
            if kind == "prev":
                if current_idx > 0:
                    refs.add(current_idx - 1)
            else:
                target = int(m.group(2))
                if target < current_idx:
                    refs.add(target)
    elif isinstance(value, dict):
        for v in value.values():
            _collect_refs(v, current_idx, refs)
    elif isinstance(value, list):
        for v in value:
            _collect_refs(v, current_idx, refs)


def _leaf_indices(steps: list[dict]) -> set[int]:
    """Return the set of step indices whose outputs are *not* referenced by any
    later step. The final step is always a leaf by construction."""
    referenced: set[int] = set()
    for j, step in enumerate(steps):
        # _collect_refs is self-guarding — only refs strictly < j count, so
        # step 0 contributes nothing regardless.
        _collect_refs(step.get("params") or {}, j, referenced)
    return {i for i in range(len(steps)) if i not in referenced}


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


def _validate_steps(steps: list[dict], ctx: fir_ext.Context, prefix: str = "pipe") -> str | None:
    """Validate a steps array and normalise tool names in-place.

    Shared by ``pipe`` and ``wait``. Returns an error message string (using
    *prefix* in user-facing text) on failure, or ``None`` if the steps are
    valid. Tool names are normalised case-insensitively against the live
    registry as a side effect."""
    if not isinstance(steps, list) or not steps:
        return f"{prefix}: steps must be a non-empty array"

    # Shape validation up front so a bad spec fails fast.
    for i, step in enumerate(steps):
        if not isinstance(step, dict):
            return f"{prefix}: steps[{i}] must be an object"
        if not step.get("tool"):
            return f"{prefix}: steps[{i}] missing 'tool'"

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
        return f"{prefix} validation failed:\n" + "\n".join(errors)
    return None


def _run_pipe(steps: list[dict], label: str, ctx: fir_ext.Context) -> dict:
    err = _validate_steps(steps, ctx, "pipe")
    if err:
        return _error(err)

    prior_text: list[str] = []
    results: list[dict] = []
    any_error = False
    leaves = _leaf_indices(steps)

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
            results.append({"name": name, "output": text, "is_error": True, "leaf": i in leaves})
            prior_text.append(text)
            any_error = True
            if cont:
                continue
            return _format_error(i, name, results)

        is_error = bool(result.get("is_error", False))
        text = _truncate(_result_text(result))
        results.append({"name": name, "output": text, "is_error": is_error, "leaf": i in leaves})
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
        # Errored steps are always shown, even if non-leaf — the LLM needs
        # to see the failure output regardless of how the pipe ended up here.
        if r.get("leaf", True) or r["is_error"]:
            parts.append(f"## Step {i}: {r['name']}{tag}")
            parts.append(r["output"])
        else:
            size = len(r["output"])
            parts.append(
                f"## Step {i}: {r['name']}{tag} (intermediate, {size} bytes — omitted)"
            )
        parts.append("")
    return "\n".join(parts).rstrip() + "\n"


# ---------------------------------------------------------------------------
# wait — server-side poll loop
# ---------------------------------------------------------------------------

# The verdict step's final non-empty line must match this exactly.
_WAIT_RE = re.compile(r"^WAIT:(done|continue|fail)( .*)?$")

# Sleep is sliced into chunks this small so a cancelled/timed-out tool call
# aborts promptly rather than blocking for the whole interval.
_WAIT_SLEEP_SLICE = 0.25


def _now() -> float:
    """Monotonic clock seam — overridable in tests."""
    return time.monotonic()


def _sleep_sliced(seconds: float, heartbeat: Any = None, heartbeat_every: float = 10.0) -> None:
    """Sleep *seconds* in small slices so cancellation aborts promptly.

    If *heartbeat* is callable, it is invoked roughly every *heartbeat_every*
    seconds during the sleep. This keeps the extension's bridge connection
    marked active (the Go side resets its activity-aware tool-call deadline on
    any message), so a large ``interval`` does not look like a hung tool."""
    remaining = seconds
    since_beat = 0.0
    while remaining > 0:
        chunk = _WAIT_SLEEP_SLICE if remaining > _WAIT_SLEEP_SLICE else remaining
        time.sleep(chunk)
        remaining -= chunk
        since_beat += chunk
        if heartbeat is not None and since_beat >= heartbeat_every and remaining > 0:
            since_beat = 0.0
            heartbeat()


def _coerce_num(params: Mapping[str, Any], key: str, default: float, minimum: float) -> float:
    """Coerce a numeric param, falling back to *default* on bad input and
    clamping up to *minimum*."""
    try:
        v = float(params.get(key, default))
    except (TypeError, ValueError):
        return default
    return v if v >= minimum else minimum


def _inject_env(params: Any, poll: int, state_path: str) -> Any:
    """Expose WAIT_POLL / WAIT_STATE to a probe step.

    The Bash tool has no ``env`` parameter, so we prepend ``export`` lines to
    any string ``command`` param. Steps without a command are passed through
    unchanged."""
    if isinstance(params, dict) and isinstance(params.get("command"), str):
        prefix = (
            f"export WAIT_POLL={poll}\n"
            f"export WAIT_STATE={shlex.quote(state_path)}\n"
        )
        new = dict(params)
        new["command"] = prefix + params["command"]
        return new
    return params


def _run_probe(
    steps: list[dict], label: str, poll: int, state_path: str, ctx: fir_ext.Context
) -> tuple[bool, str, bool]:
    """Run the probe chain once (reusing pipe's execution path).

    Returns ``(reached_verdict, verdict_text, verdict_is_error)`` where the
    verdict is the LAST step. ``reached_verdict`` is False if an earlier step
    errored and aborted the chain before the verdict step could run — in that
    case *verdict_text* holds the aborting step's output for diagnosis."""
    prior_text: list[str] = []
    last_index = len(steps) - 1
    last_text = ""

    for i, step in enumerate(steps):
        name = step["tool"]
        raw_params = step.get("params") or {}
        params = _substitute(raw_params, prior_text)
        params = _inject_env(params, poll, state_path)
        cont = bool(step.get("continue_on_error", False))

        progress = f"wait poll {poll}: step {i + 1}/{len(steps)} {name}"
        if label:
            progress = f"{label}: {progress}"
        ctx.report_progress(progress)

        try:
            result = ctx.call_tool(name, params)
        except Exception as exc:
            text = _truncate(f"error calling tool: {exc}")
            prior_text.append(text)
            last_text = text
            if cont:
                continue
            # Aborted before reaching the verdict step.
            if i == last_index:
                return True, text, True
            return False, text, True

        is_error = bool(result.get("is_error", False))
        text = _truncate(_result_text(result))
        prior_text.append(text)
        last_text = text
        if is_error and not cont:
            if i == last_index:
                return True, text, True
            return False, text, True

    # Reached the end — last step is the verdict.
    return True, last_text, False


def _parse_verdict(text: str) -> tuple[str | None, str]:
    """Parse the final non-empty line of *text* for a WAIT: sentinel.

    Returns ``(kind, message)`` where kind is one of done/continue/fail, or
    ``(None, "")`` if no matching sentinel line is present."""
    lines = [ln for ln in text.splitlines() if ln.strip()]
    if not lines:
        return None, ""
    m = _WAIT_RE.match(lines[-1])
    if not m:
        return None, ""
    return m.group(1), (m.group(2) or "").strip()


def _strip_sentinel(text: str) -> str:
    """Remove the bare WAIT: sentinel line (protocol noise) from probe output
    while keeping the pre-sentinel debug lines. Only the final non-empty line
    is removed, and only if it matches the sentinel regex."""
    lines = text.splitlines()
    # Find the last non-empty line index.
    for idx in range(len(lines) - 1, -1, -1):
        if lines[idx].strip():
            if _WAIT_RE.match(lines[idx]):
                del lines[idx]
            break
    return "\n".join(lines)


def _wait_terminal(
    outcome: str, polls: int, elapsed: float, message: str, diag: str
) -> dict:
    """Build the single terminal payload returned to the model."""
    is_error = outcome in ("error", "timeout")
    body = [
        f"wait: {outcome}",
        f"polls: {polls}",
        f"elapsed: {elapsed:.1f}s",
        f"message: {message}",
    ]
    cleaned = _strip_sentinel(diag).rstrip()
    text = "\n".join(body)
    if cleaned:
        text += "\n\n## Last probe output\n" + cleaned
    return {"content": [{"type": "text", "text": text}], "is_error": is_error}


def _run_wait(
    steps: list[dict],
    interval: float,
    timeout: float,
    max_polls: int,
    label: str,
    ctx: fir_ext.Context,
) -> dict:
    err = _validate_steps(steps, ctx, "wait")
    if err:
        return _error(err)

    # A stable per-wait scratch file the probe can read/write across polls
    # (via $WAIT_STATE). Created once, cleaned up at the end.
    fd, state_path = tempfile.mkstemp(prefix="wait-state-")
    os.close(fd)

    start = _now()
    polls = 0
    strikes = 0
    last_verdict = "?"
    try:
        while True:
            polls += 1
            reached, vtext, vis_error = _run_probe(steps, label, polls, state_path, ctx)
            elapsed = _now() - start

            if not reached or vis_error:
                # Verdict step (or the step that aborted before it) errored.
                strikes += 1
                last_verdict = "error"
                if strikes >= 3:
                    return _wait_terminal(
                        "error", polls, elapsed,
                        "wait: verdict step failed 3 polls in a row", vtext,
                    )
                # Treat as continue and fall through to the cap check / sleep.
            else:
                verdict, message = _parse_verdict(vtext)
                if verdict is None:
                    return _wait_terminal(
                        "error", polls, elapsed,
                        "wait: verdict step emitted no WAIT: sentinel", vtext,
                    )
                strikes = 0
                last_verdict = verdict
                if verdict == "done":
                    return _wait_terminal(
                        "success", polls, elapsed, message or "verdict: done", vtext,
                    )
                if verdict == "fail":
                    return _wait_terminal(
                        "error", polls, elapsed,
                        "wait: " + (message or "verdict reported fail"), vtext,
                    )
                # verdict == "continue": fall through.

            if elapsed >= timeout or polls >= max_polls:
                return _wait_terminal(
                    "timeout", polls, elapsed,
                    f"wait: cap reached (polls={polls}, elapsed={elapsed:.1f}s)", vtext,
                )

            ctx.report_progress(
                f"wait: poll {polls}/{max_polls}, {int(elapsed)}s, last={last_verdict}"
            )
            _sleep_sliced(
                interval,
                heartbeat=lambda p=polls, lv=last_verdict: ctx.report_progress(
                    f"wait: waiting (poll {p}/{max_polls}, last={lv})"
                ),
            )
    finally:
        with contextlib.suppress(OSError):
            os.unlink(state_path)


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
    "step. For multi-step pipes, only **leaf** step outputs are returned to "
    "the LLM — a leaf is a step whose output is not referenced by any later "
    "step via {{prev}}, {{step:N}}, or {{step:N.field}}. Non-leaf step "
    "outputs are still fed (in full, post-truncation) into later steps' "
    "substitutions, but are omitted from the final result and replaced by a "
    "one-line size marker. The last step is always a leaf; errored steps "
    "are always shown regardless of leaf status. This lets you build large "
    "data pipelines without polluting LLM context with intermediate blobs.\n\n"
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


_WAIT_DESCRIPTION = (
    "Wait server-side until a probe condition is met — poll a tool chain on a "
    "fixed interval and return to the model exactly ONCE when it settles. "
    "Moves a watch / while-sleep poll loop off the model's context so growing "
    "probe output never replays each iteration.\n\n"
    "`steps` is the same shape as pipe (tool + params + optional "
    "continue_on_error) with the SAME {{prev}} / {{step:N}} / {{step:N.field}} "
    "substitution. The chain is re-run each poll cycle; the LAST step is the "
    "verdict step. Print debug lines as you like, then make the FINAL "
    "non-empty line of the verdict step's stdout one of:\n"
    "  WAIT:done          — condition met, returns outcome=success.\n"
    "  WAIT:fail <msg>     — give up now, returns outcome=error with <msg>.\n"
    "  WAIT:continue       — not ready, keep polling.\n"
    "Any other final line is a hard error (no defaulting to continue). If the "
    "verdict step itself errors (nonzero exit / timeout) it counts as continue "
    "but 3 consecutive errors abort with outcome=error.\n\n"
    "Every probe step sees two env vars (exported into Bash `command` params): "
    "WAIT_POLL (current poll index) and WAIT_STATE (a stable per-wait scratch "
    "file path, reused across polls) so you can self-implement settle/delta "
    "logic. `interval` (default 5s, no backoff), `timeout` (overall cap, "
    "default 300s) and `max_polls` (mandatory circuit-breaker, default 60) "
    "bound the loop; hitting either cap returns outcome=timeout. The terminal "
    "payload reports outcome/polls/elapsed/message plus the last probe output "
    "(the bare WAIT: line is stripped). Returns once — progress is UI-only.\n\n"
    "[SYS_EXT] Reach for wait (not pipe) when you must BLOCK until something "
    "becomes true — a build finishes, a file appears, a service comes up, a "
    "log line is emitted. Use pipe for a one-shot chain you run immediately. "
    "Never busy-poll in-context with watch or while-sleep loops; use wait so "
    "the loop runs server-side and you pay for the result only once."
)

_WAIT_PARAMETERS: dict[str, Any] = {
    "type": "object",
    "properties": {
        "label": {
            "type": "string",
            "description": "Optional short label shown in UI progress.",
        },
        "steps": {
            "type": "array",
            "minItems": 1,
            "description": (
                "Probe chain re-run each poll. Same shape and substitution as "
                "pipe. The last step is the verdict step: its final non-empty "
                "stdout line must be WAIT:done, WAIT:continue, or WAIT:fail."
            ),
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
                            "If true, continue the probe chain when this step "
                            "errors instead of aborting the poll."
                        ),
                    },
                },
                "required": ["tool"],
            },
        },
        "interval": {
            "type": "number",
            "description": "Fixed seconds between polls (no backoff). Default 5.",
        },
        "timeout": {
            "type": "number",
            "description": "Overall wall-clock cap in seconds. Default 300.",
        },
        "max_polls": {
            "type": "integer",
            "description": "Mandatory circuit-breaker: max poll cycles. Default 60.",
        },
    },
    "required": ["steps"],
}


@fir_ext.tool(
    name="wait",
    description=_WAIT_DESCRIPTION,
    parameters=_WAIT_PARAMETERS,
    display_hint={
        "title_args": [{"name": "label", "style": "accent"}],
    },
)
def wait(params: dict, ctx: fir_ext.Context):
    steps = params.get("steps") or []
    label = params.get("label", "") or ""
    interval = _coerce_num(params, "interval", 5.0, 0.0)
    timeout = _coerce_num(params, "timeout", 300.0, 0.0)
    max_polls = int(_coerce_num(params, "max_polls", 60.0, 1.0))
    return _run_wait(steps, interval, timeout, max_polls, label, ctx)


fir_ext.run(name="pipe")
