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

import atexit
import contextlib
import json
import os
import re
import secrets
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


# Trailing metadata block appended by core tools (Bash/Read) carrying the
# output content hash. Pure protocol noise for pipelines: it must not leak
# into {{prev}}/{{step:N}} substitutions and must not displace the WAIT:
# sentinel as the final line of a wait verdict step. Matched only when it is
# a block's ENTIRE text, so genuine output lines are never dropped.
_HASH_BLOCK_RE = re.compile(r"^\[hash: [0-9a-f]+\]$")


def _result_text(result: Mapping[str, Any]) -> str:
    """Extract text content from a call_tool result dict.

    Drops standalone ``[hash: ...]`` metadata blocks (see _HASH_BLOCK_RE)."""
    content = result.get("content", [])
    if isinstance(content, list):
        parts = []
        for block in content:
            if isinstance(block, dict):
                text = block.get("text") or block.get("Text", "")
            elif isinstance(block, str):
                text = block
            else:
                continue
            if text and not _HASH_BLOCK_RE.match(text.strip()):
                parts.append(text)
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


def _step_call_kwargs(step: Mapping[str, Any]) -> dict:
    """Build the ctx.call_tool kwargs for a step, honouring an optional
    per-step ``timeout_s`` (seconds). Bad values fall back to the default."""
    t = step.get("timeout_s")
    if t is None:
        return {}
    try:
        return {"timeout": float(t)}
    except (TypeError, ValueError):
        return {}


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
            errors.append(f"steps[{i}] ({name}): missing required params: " + ", ".join(missing))

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

        # Front-loaded: clients truncate the spinner label to ~12 runes, so the
        # label and step counter must come first.
        ctx.report_progress(f"{label or 'pipe'} {i + 1}/{len(steps)} {name}")

        try:
            result = ctx.call_tool(name, params, **_step_call_kwargs(step))
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
            parts.append(f"## Step {i}: {r['name']}{tag} (intermediate, {size} bytes — omitted)")
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


def _sleep_sliced(seconds: float) -> None:
    """Sleep *seconds* in small slices so cancellation aborts promptly.

    The ``wait`` tool declares its host-side timeout disabled (``timeout=-1``),
    so a long interval is never mistaken for a hung tool — no keep-alive
    heartbeat is needed. Slicing exists only so a cancelled/timed-out call
    returns near the slice boundary instead of blocking the whole interval."""
    remaining = seconds
    while remaining > 0:
        chunk = _WAIT_SLEEP_SLICE if remaining > _WAIT_SLEEP_SLICE else remaining
        time.sleep(chunk)
        remaining -= chunk


def _coerce_num(params: Mapping[str, Any], key: str, default: float, minimum: float) -> float:
    """Coerce a numeric param, falling back to *default* on bad input and
    clamping up to *minimum*."""
    try:
        v = float(params.get(key, default))
    except (TypeError, ValueError):
        return default
    return v if v >= minimum else minimum


def _opt_num(params: Mapping[str, Any], key: str, minimum: float) -> float | None:
    """Like _coerce_num but returns None when the key is absent (or unusable),
    so a caller can distinguish "not passed" from "passed a default"."""
    if params.get(key) is None:
        return None
    try:
        v = float(params[key])
    except (TypeError, ValueError):
        return None
    return v if v >= minimum else minimum


def _opt_int(params: Mapping[str, Any], key: str, minimum: float) -> int | None:
    """Integer flavour of _opt_num."""
    v = _opt_num(params, key, minimum)
    return None if v is None else int(v)


def _inject_env(params: Any, poll: int, state_path: str) -> Any:
    """Expose WAIT_POLL / WAIT_STATE to a probe step.

    The Bash tool has no ``env`` parameter, so we prepend ``export`` lines to
    any string ``command`` param. Steps without a command are passed through
    unchanged."""
    if isinstance(params, dict) and isinstance(params.get("command"), str):
        prefix = f"export WAIT_POLL={poll}\nexport WAIT_STATE={shlex.quote(state_path)}\n"
        new = dict(params)
        new["command"] = prefix + params["command"]
        return new
    return params


def _wait_prefix(label: str, poll: int, max_polls: int) -> str:
    """Front-loaded progress label, e.g. ``"rl-reset 7/60"``.

    Clients truncate the spinner label to about 12 runes, so the caller's label
    and the poll counter must come first."""
    return f"{label or 'wait'} {poll}/{max_polls}"


def _run_probe(
    steps: list[dict],
    prefix: str,
    poll: int,
    state_path: str,
    ctx: fir_ext.Context,
) -> tuple[bool, str, bool]:
    """Run the probe chain once (reusing pipe's execution path).

    *prefix* is the already front-loaded progress label (see ``_wait_prefix``);
    *poll* is the cumulative poll number exposed to the probe as ``$WAIT_POLL``.

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

        ctx.report_progress(f"{prefix} {name}")

        try:
            result = ctx.call_tool(name, params, **_step_call_kwargs(step))
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


# ---------------------------------------------------------------------------
# Resumable timeout checkpoints
# ---------------------------------------------------------------------------

# A wait that hits its timeout/max_polls cap is a checkpoint, not a failure:
# the probe chain, the poll counter and — crucially — the $WAIT_STATE scratch
# file the probe accumulated settle/delta data in are all still valid. We stash
# them under a short handle so the model can re-enter the loop with a fresh
# budget instead of re-authoring `steps` and losing that state.
#
# Session-scoped (this extension process only) and bounded by a TTL sweep so a
# checkpoint the model never resumes cannot leak its tempfile forever.
_WAIT_RESUME_TTL = 2 * 60 * 60.0

# handle -> record. See _stash_resume for the shape. Extension tool calls are
# dispatched one at a time, so plain module globals need no locking here.
_wait_resumes: dict[str, dict[str, Any]] = {}

# Scratch files of loops currently running. Tracked so the atexit sweep also
# reclaims them if the process goes down mid-poll.
_wait_active: set[str] = set()


def _drop_resume(handle: str) -> None:
    """Forget a checkpoint and remove its scratch file."""
    rec = _wait_resumes.pop(handle, None)
    if rec:
        with contextlib.suppress(OSError):
            os.unlink(rec["state_path"])


def _expire_resumes() -> None:
    """Best-effort sweep of checkpoints older than the TTL."""
    cutoff = time.time() - _WAIT_RESUME_TTL
    for handle in [h for h, r in _wait_resumes.items() if r["stashed_at"] < cutoff]:
        _drop_resume(handle)


def _cleanup_resumes() -> None:
    """Unlink every scratch file — stashed checkpoints and in-flight loops
    alike. Registered via atexit so a normal process shutdown does not leave
    wait-state-* tempfiles behind."""
    for handle in list(_wait_resumes):
        _drop_resume(handle)
    for path in list(_wait_active):
        _wait_active.discard(path)
        with contextlib.suppress(OSError):
            os.unlink(path)


atexit.register(_cleanup_resumes)


def _stash_resume(
    steps: list[dict],
    state_path: str,
    polls: int,
    elapsed: float,
    strikes: int,
    last_verdict: str,
    label: str,
    interval: float,
    timeout: float,
    max_polls: int,
) -> str:
    """Record a timed-out loop's state and return its resume handle."""
    _expire_resumes()
    handle = "w_" + secrets.token_hex(3)
    while handle in _wait_resumes:
        handle = "w_" + secrets.token_hex(3)
    _wait_resumes[handle] = {
        "steps": steps,
        "state_path": state_path,
        "polls": polls,
        "elapsed": elapsed,
        "strikes": strikes,
        "last_verdict": last_verdict,
        "label": label,
        "interval": interval,
        "timeout": timeout,
        "max_polls": max_polls,
        "stashed_at": time.time(),
    }
    return handle


def _resume_error(handle: str) -> dict:
    """Explain an unknown/expired handle rather than silently starting over."""
    _expire_resumes()
    valid = ", ".join(sorted(_wait_resumes)) or "<none>"
    return _error(
        f"wait: unknown or expired resume handle {handle!r}. Handles are "
        f"session-scoped and expire after {int(_WAIT_RESUME_TTL // 3600)}h, and "
        "are dropped once their loop finishes (success or error). Valid "
        f"handles: {valid}. To start a new wait, call it with `steps` and no "
        "`resume`."
    )


def _wait_terminal(
    outcome: str,
    polls: int,
    elapsed: float,
    message: str,
    diag: str,
    resume: str = "",
) -> dict:
    """Build the single terminal payload returned to the model."""
    # A timeout is a PARTIAL RESULT, not a failure: the probe loop ran clean
    # and simply has not settled yet. Flagging it is_error poisons tool
    # error-rate metrics and pushes the model to treat "still running" as
    # "broken". Only outcome=error (probe blew up / WAIT:fail) is an error.
    is_error = outcome == "error"
    body = [
        f"wait: {outcome}",
        f"polls: {polls}",
        f"elapsed: {elapsed:.1f}s",
    ]
    if resume:
        body.append(f"resume: {resume}")
    body.append(f"message: {message}")
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
    rec: dict[str, Any] | None = None,
) -> dict:
    """Run the poll loop until it settles or hits a cap.

    *rec* is a checkpoint record from a previous timed-out run (see
    _stash_resume). When present the loop continues that run: same scratch
    file, same cumulative poll/elapsed counters, same strike/verdict state.
    The caps always apply to THIS segment only — a resume gets a fresh
    budget from the point it re-enters."""
    err = _validate_steps(steps, ctx, "wait")
    if err:
        return _error(err)

    if rec is None:
        # A stable per-wait scratch file the probe can read/write across polls
        # (via $WAIT_STATE). Created once; kept alive across a resume and
        # removed on any non-timeout outcome.
        fd, state_path = tempfile.mkstemp(prefix="wait-state-")
        os.close(fd)
        prior_polls = 0
        prior_elapsed = 0.0
        strikes = 0
        last_verdict = "?"
    else:
        state_path = rec["state_path"]
        prior_polls = rec["polls"]
        prior_elapsed = rec["elapsed"]
        strikes = rec["strikes"]
        last_verdict = rec["last_verdict"]

    _wait_active.add(state_path)
    start = _now()
    # `polls` counts this segment (what the caps bound); `total` is what the
    # model sees, continuing across resumes.
    polls = 0
    total = prior_polls
    try:
        while True:
            polls += 1
            total += 1
            prefix = _wait_prefix(label, polls, max_polls)
            reached, vtext, vis_error = _run_probe(steps, prefix, total, state_path, ctx)
            segment = _now() - start
            elapsed = prior_elapsed + segment

            if not reached or vis_error:
                # Verdict step (or the step that aborted before it) errored.
                strikes += 1
                last_verdict = "error"
                if strikes >= 3:
                    return _wait_terminal(
                        "error",
                        total,
                        elapsed,
                        "wait: verdict step failed 3 polls in a row",
                        vtext,
                    )
                # Treat as continue and fall through to the cap check / sleep.
            else:
                verdict, message = _parse_verdict(vtext)
                if verdict is None:
                    tail = next(
                        (ln for ln in reversed(vtext.splitlines()) if ln.strip()),
                        "<empty>",
                    )
                    return _wait_terminal(
                        "error",
                        total,
                        elapsed,
                        "wait: verdict step emitted no WAIT: sentinel — its "
                        f"final non-empty stdout line was {tail.strip()!r}, "
                        "expected exactly WAIT:done, WAIT:continue, or "
                        "WAIT:fail <msg>",
                        vtext,
                    )
                strikes = 0
                last_verdict = verdict
                if verdict == "done":
                    return _wait_terminal(
                        "success",
                        total,
                        elapsed,
                        message or "verdict: done",
                        vtext,
                    )
                if verdict == "fail":
                    return _wait_terminal(
                        "error",
                        total,
                        elapsed,
                        "wait: " + (message or "verdict reported fail"),
                        vtext,
                    )
                # verdict == "continue": fall through.

            if segment >= timeout or polls >= max_polls:
                cap = "max_polls" if polls >= max_polls else "timeout"
                handle = _stash_resume(
                    steps,
                    state_path,
                    total,
                    elapsed,
                    strikes,
                    last_verdict,
                    label,
                    interval,
                    timeout,
                    max_polls,
                )
                return _wait_terminal(
                    "timeout",
                    total,
                    elapsed,
                    f"wait: {cap} cap reached (polls={polls}/{max_polls}, "
                    f"elapsed={segment:.1f}s/{timeout:.0f}s) — NOT a failure, the "
                    f"probe never reported WAIT:fail. If the job is still "
                    f"legitimately running, re-enter this same loop with "
                    f"resume={handle} (optionally with a larger {cap}) — the "
                    f"probe steps, poll counter and $WAIT_STATE contents are "
                    f"all preserved.",
                    vtext,
                    resume=handle,
                )

            ctx.report_progress(f"{prefix} {int(elapsed)}s last={last_verdict}")
            _sleep_sliced(interval)
    finally:
        # A timeout hands ownership of the scratch file to its checkpoint;
        # every other exit path (settled, cancelled, exception) drops it.
        _wait_active.discard(state_path)
        if not _handle_for(state_path):
            with contextlib.suppress(OSError):
                os.unlink(state_path)


def _handle_for(state_path: str) -> str:
    """Return the checkpoint handle owning *state_path*, or "" if none."""
    for handle, rec in _wait_resumes.items():
        if rec["state_path"] == state_path:
            return handle
    return ""


def _run_wait_resume(
    handle: str,
    steps: list[dict],
    interval: float | None,
    timeout: float | None,
    max_polls: int | None,
    label: str,
    ctx: fir_ext.Context,
) -> dict:
    """Re-enter a timed-out loop by handle.

    Caps passed explicitly override the stored ones (a fresh budget from the
    resume point); omitted ones fall back to what the original call used.
    ``steps`` is optional — the stored chain is reused unless a new one is
    given."""
    _expire_resumes()
    rec = _wait_resumes.get(handle)
    if rec is None:
        return _resume_error(handle)
    steps = steps or rec["steps"]
    # Validate BEFORE consuming the record: a bad probe chain passed alongside
    # a good handle must not destroy the checkpoint (or orphan its scratch
    # file) — the model should be able to retry the same handle.
    err = _validate_steps(steps, ctx, "wait")
    if err:
        return _error(err)
    # Take ownership: the record is consumed by this run and re-stashed only
    # if we time out again.
    del _wait_resumes[handle]
    return _run_wait(
        steps,
        rec["interval"] if interval is None else interval,
        rec["timeout"] if timeout is None else timeout,
        rec["max_polls"] if max_polls is None else max_polls,
        label or rec["label"],
        ctx,
        rec=rec,
    )


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
    "step. A step may set `timeout_s` (seconds) to run a slow tool longer "
    "than the 60s default — a single-step pipe is thus a run-with-timeout "
    "wrapper. For multi-step pipes, only **leaf** step outputs are returned to "
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
                    "timeout_s": {
                        "type": "number",
                        "description": (
                            "Optional per-step timeout in seconds (default "
                            "60). Use a larger value for a step that runs "
                            "long (slow MCP/bash). Soft: on expiry the step "
                            "errors and the pipe stops waiting, but the "
                            "underlying call may keep running."
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
    # pipe is a legitimate long-runner: it chains tool calls and a single step
    # (slow MCP/bash) can silently exceed the 30s default. Disable the
    # host-side timeout so a slow step is never clipped mid-work; each step is
    # still bounded by its own call_tool timeout (per-step `timeout_s`, default
    # 60s) and the whole pipe by the turn context (ESC / abort).
    timeout=-1,
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
    "logic. `interval` (default 5s, no backoff), `timeout` (overall wall-clock "
    "cap, default 900s) and `max_polls` (mandatory circuit-breaker, default "
    "60) bound the loop; hitting either cap returns outcome=timeout. Budget "
    "the caps for the REAL job: a CI/release workflow or a disk check wants "
    "`timeout` 1800-3600 with `interval` 30-60, not the default. "
    "outcome=timeout is NOT an error — it means the probe never said fail and "
    "the job may still be running; it is a RESUMABLE CHECKPOINT. A timeout "
    "payload carries `resume: <handle>`; call wait again with "
    "`resume: <handle>` (steps optional, omit them) to re-enter the SAME loop "
    "— same $WAIT_STATE scratch file and contents, poll counter continuing "
    "where it stopped — with a fresh budget. `interval`/`timeout`/`max_polls` "
    "passed alongside `resume` override the originals; omitted ones are "
    "reused. Handles are session-scoped, expire after 2h, and are dropped "
    "once the loop finishes. The terminal "
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
                "stdout line must be WAIT:done, WAIT:continue, or WAIT:fail. "
                "Required unless `resume` is given."
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
                    "timeout_s": {
                        "type": "number",
                        "description": (
                            "Optional per-step timeout in seconds (default "
                            "60) for a slow probe step."
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
            "description": (
                "Overall wall-clock cap in seconds. Default 900. Raise it for "
                "genuinely long jobs (CI/release runs, fsck, image builds): "
                "1800-3600 is normal there."
            ),
        },
        "max_polls": {
            "type": "integer",
            "description": "Mandatory circuit-breaker: max poll cycles. Default 60.",
        },
        "resume": {
            "type": "string",
            "description": (
                "Handle from a previous outcome=timeout payload (e.g. "
                "w_8f3a21). Re-enters that loop: same probe steps, same "
                "$WAIT_STATE file and contents, poll counter continuing from "
                "where it stopped, with a fresh cap budget. Omit `steps` when "
                "resuming (pass them only to change the probe chain)."
            ),
        },
    },
}


@fir_ext.tool(
    name="wait",
    description=_WAIT_DESCRIPTION,
    parameters=_WAIT_PARAMETERS,
    display_hint={
        "title_args": [{"name": "label", "style": "accent"}],
    },
    # wait blocks server-side across many polls and sleeps, far longer than the
    # 30s default. Disable the host-side timeout; the loop is bounded by its own
    # `timeout` / `max_polls` caps and by the turn context (ESC / abort).
    timeout=-1,
)
def wait(params: dict, ctx: fir_ext.Context):
    steps = params.get("steps") or []
    label = params.get("label", "") or ""
    resume = (params.get("resume") or "").strip()
    if resume:
        # On resume, only caps the model passed explicitly override the
        # stored ones — absent keys fall back to the original call's values.
        return _run_wait_resume(
            resume,
            steps,
            _opt_num(params, "interval", 0.0),
            _opt_num(params, "timeout", 0.0),
            _opt_int(params, "max_polls", 1.0),
            label,
            ctx,
        )
    interval = _coerce_num(params, "interval", 5.0, 0.0)
    timeout = _coerce_num(params, "timeout", 900.0, 0.0)
    max_polls = int(_coerce_num(params, "max_polls", 60.0, 1.0))
    return _run_wait(steps, interval, timeout, max_polls, label, ctx)


fir_ext.run(name="pipe")
