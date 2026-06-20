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

By default fir's bundled top-tier Anthropic flagship is used as the advisor,
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

Delegate de-escalation
----------------------

The mirror image of escalation: when the executor itself is an expensive
flagship, context-heavy but low-judgement asides (bulk file reads +
synthesis, log summarisation, data extraction) can be routed *down* to a
fast/cheap model via the ``delegate: bool`` parameter.  Configured the
same way, under the ``"delegate"`` key in aside.json:

    /aside-delegate anthropic/claude-haiku-4-5        # pin a model
    /aside-delegate                                   # show current
    /aside-delegate off                               # disable delegation

The default delegate is fir's cheapest current Anthropic tier.  Setting
both ``escalate`` and ``delegate`` on one call is a validation error.

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
import re
import time
from pathlib import Path
from typing import TYPE_CHECKING, Any

import fir_ext

if TYPE_CHECKING:
    from collections.abc import Mapping

# ---------------------------------------------------------------------------
# Advisor configuration — read once at module load
# ---------------------------------------------------------------------------

_CONFIG_FILENAME = "aside.json"

# Default advisor when no config file exists. Always points at the strongest
# Anthropic flagship baked into fir's model registry — the highest Fable,
# falling back to the highest Opus only when no Fable exists. Drift is caught
# by DefaultAdvisorTracksHighestAnthropicFlagship in
# pkg/resources/testdata/aside_test.py — bump this constant when fir adds a
# newer flagship.
_DEFAULT_ADVISOR_SPEC = "anthropic/claude-fable-5"

# Default delegate when no config file exists. Always points at the cheapest
# current Anthropic tier (highest Haiku) baked into fir's model registry.
# Drift is caught by DefaultDelegateTracksHighestAnthropicHaiku in
# pkg/resources/testdata/aside_test.py — bump this constant when fir adds a
# newer Haiku.
_DEFAULT_DELEGATE_SPEC = "anthropic/claude-haiku-4-5"


def _config_path() -> Path | None:
    """Highest-priority path for reading/writing aside.json. None when the
    SDK has no config dirs (e.g. tests that haven't seeded any)."""
    p = fir_ext.config_path(_CONFIG_FILENAME)
    return Path(p) if p else None


def _read_existing_config() -> dict | None:
    """Read the existing aside.json from the highest-priority dir that has
    one. Returns the parsed dict, or None if no file exists / unparsable."""
    return fir_ext.load_config(_CONFIG_FILENAME)


def _load_role_config(key: str, default_spec: str) -> dict[str, str] | None:
    """Read a model-role config (advisor or delegate) from aside.json.

    Returns a dict with ``provider``, ``model`` and optional ``effort`` keys,
    or ``None`` if the role is explicitly disabled.  Malformed files are
    ignored silently — the extension falls back to the default in that case.

    Resolution order:
      1. Explicit ``"<key>": null`` (or ``"<key>": "off"``) → disabled.
      2. Explicit ``"<key>": "<spec>"`` → use it (validated; falls through
         to default on parse failure).
      3. File missing or unparsable → use the bundled default.
    """
    data = _read_existing_config()
    if isinstance(data, dict) and key in data:
        value = data[key]
        # Explicit opt-out.
        if value is None or (
            isinstance(value, str) and value.strip().lower() in ("", "off", "none")
        ):
            return None
        if isinstance(value, str):
            parsed = _parse_advisor_spec(value)
            if parsed is not None:
                return parsed
        # Malformed entry — fall through to default rather than disable.

    return _parse_advisor_spec(default_spec)


def _load_advisor_config() -> dict[str, str] | None:
    """Advisor model config — defaults to the strongest bundled Anthropic flagship."""
    return _load_role_config("advisor", _DEFAULT_ADVISOR_SPEC)


def _load_delegate_config() -> dict[str, str] | None:
    """Delegate model config — defaults to the cheapest bundled Anthropic tier."""
    return _load_role_config("delegate", _DEFAULT_DELEGATE_SPEC)


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


# Lazily-loaded advisor/delegate configs — populated on first access (after
# the init handshake has set fir_ext.config_dirs). Tests that want to inject
# a value can assign to _ADVISOR / _DELEGATE directly.
_ADVISOR_UNSET = object()
_ADVISOR: Any = _ADVISOR_UNSET
_DELEGATE: Any = _ADVISOR_UNSET


def _advisor() -> dict[str, str] | None:
    global _ADVISOR
    if _ADVISOR is _ADVISOR_UNSET:
        _ADVISOR = _load_advisor_config()
    return _ADVISOR


def _delegate() -> dict[str, str] | None:
    global _DELEGATE
    if _DELEGATE is _ADVISOR_UNSET:
        _DELEGATE = _load_delegate_config()
    return _DELEGATE


# ---------------------------------------------------------------------------
# Runtime availability adaptation (Layer A) + ranking helpers
# ---------------------------------------------------------------------------
#
# The configured/default advisor & delegate specs are a *preference seed*, not
# the final answer. Anthropic can make a model (e.g. claude-fable-5)
# unavailable at runtime; fir's model registry already prunes it from
# GetAvailable(). We query that set via ctx.available_models() and degrade to
# the highest-ranked AVAILABLE Anthropic flagship (advisor) / Haiku (delegate)
# rather than routing to a dead model.
#
# The ranking helpers below are the single source of truth: the drift tests
# (DefaultAdvisorTracksHighestAnthropicFlagship /
# DefaultDelegateTracksHighestAnthropicHaiku) reuse them so test and runtime
# always agree on "which model is strongest/cheapest".

# Bare X-Y forms only — minor capped at 2 digits to reject date stamps
# (e.g. claude-opus-4-1-20250805). Fable's minor is optional; Opus requires it.
_FABLE_RE = re.compile(r"^claude-fable-(\d+)(?:-(\d{1,2}))?$")
_OPUS_RE = re.compile(r"^claude-opus-(\d+)-(\d{1,2})$")
_HAIKU_RE = re.compile(r"^claude-haiku-(\d+)-(\d{1,2})$")


def _rank_flagship(model_id: str) -> tuple[int, int, int] | None:
    """Rank an Anthropic flagship model id. Higher tuple == stronger.

    The Fable (Mythos-class) tier always outranks the Opus tier, then by
    (major, minor). Returns None for non-flagship / date-stamped ids.
    """
    mid = model_id.strip()
    m = _FABLE_RE.match(mid)
    if m is not None:
        return (1, int(m.group(1)), int(m.group(2) or 0))
    m = _OPUS_RE.match(mid)
    if m is not None:
        return (0, int(m.group(1)), int(m.group(2)))
    return None


def _rank_haiku(model_id: str) -> tuple[int, int] | None:
    """Rank an Anthropic Haiku model id by (major, minor). None if not Haiku."""
    m = _HAIKU_RE.match(model_id.strip())
    if m is None:
        return None
    return (int(m.group(1)), int(m.group(2)))


def _best_anthropic_flagship(model_ids: list[str]) -> str | None:
    """Pick the highest-ranked flagship id from *model_ids* (Fable > Opus)."""
    best_id: str | None = None
    best_rank: tuple[int, int, int] | None = None
    for mid in model_ids:
        r = _rank_flagship(mid)
        if r is not None and (best_rank is None or r > best_rank):
            best_rank, best_id = r, mid
    return best_id


def _best_anthropic_haiku(model_ids: list[str]) -> str | None:
    """Pick the highest-ranked Haiku id from *model_ids*."""
    best_id: str | None = None
    best_rank: tuple[int, int] | None = None
    for mid in model_ids:
        r = _rank_haiku(mid)
        if r is not None and (best_rank is None or r > best_rank):
            best_rank, best_id = r, mid
    return best_id


def _query_available_models(ctx: fir_ext.Context) -> list[dict]:
    """Query the host's live-available model set, tolerating old hosts.

    Returns a list of ``{provider, id, name}`` dicts, or ``[]`` when the host
    doesn't implement the verb, the call errors, or the result isn't a list.
    An empty result means "availability unknown" — callers then use their
    static config unchanged (no regression on older fir).
    """
    fn = getattr(ctx, "available_models", None)
    if fn is None:
        return []
    try:
        models = fn()
    except Exception:
        return []
    if isinstance(models, list):
        return [m for m in models if isinstance(m, dict)]
    return []


# Per-session memo of models that returned a model-unavailability error this
# session (keys are "provider/id"). Once a model 404s, escalation skips it for
# the rest of the session instead of re-probing on every call: _degrade_role
# treats memoized models as unavailable, so subsequent escalations degrade to
# the next live flagship rather than repeating the failed call. The extension
# process lives for the session, so module scope == session scope.
_SESSION_UNAVAILABLE: set[str] = set()


def _model_key(provider: str | None, model: str | None) -> str:
    return f"{provider}/{model}"


def _mark_model_unavailable(provider: str | None, model: str | None) -> None:
    """Record that provider/model is unavailable for the rest of this session."""
    if provider and model:
        _SESSION_UNAVAILABLE.add(_model_key(provider, model))


def _degrade_role(
    cfg: dict[str, str] | None,
    available: list[dict],
    role: str,
) -> dict[str, str] | None:
    """Degrade a static role config to an AVAILABLE model when needed.

    role is "advisor" or "delegate". Resolution:
      1. cfg None / availability unknown ([]) → return cfg unchanged.
      2. cfg's provider/model is in the available set → use it.
      3. Not available, provider != anthropic → keep the static spec (no
         ranking exists for other providers; avoid a regression).
      4. Not available, anthropic → pick the highest available flagship
         (advisor) / Haiku (delegate), preserving effort. The original model
         id is recorded under ``_fallback`` for the trace line.
      5. Not available, anthropic, but no rankable model available → None
         (the role is disabled this session rather than routing to a dead
         model).
    """
    if cfg is None or not available:
        return cfg
    cfg_key = _model_key(cfg["provider"], cfg["model"])
    in_available = cfg_key not in _SESSION_UNAVAILABLE and any(
        m.get("provider") == cfg["provider"] and m.get("id") == cfg["model"]
        for m in available
    )
    if in_available:
        return cfg
    if cfg["provider"] != "anthropic":
        return cfg
    ids = [
        m.get("id", "")
        for m in available
        if m.get("provider") == "anthropic"
        and _model_key("anthropic", m.get("id", "")) not in _SESSION_UNAVAILABLE
    ]
    best = _best_anthropic_haiku(ids) if role == "delegate" else _best_anthropic_flagship(ids)
    if best is None:
        return None
    resolved: dict[str, str] = {"provider": "anthropic", "model": best, "_fallback": cfg["model"]}
    if cfg.get("effort"):
        resolved["effort"] = cfg["effort"]
    return resolved


def _resolve_advisor(ctx: fir_ext.Context) -> dict[str, str] | None:
    """Availability-aware advisor resolution (Layer A)."""
    cfg = _advisor()
    if cfg is None:
        return None
    return _degrade_role(cfg, _query_available_models(ctx), "advisor")


def _resolve_delegate(ctx: fir_ext.Context) -> dict[str, str] | None:
    """Availability-aware delegate resolution (Layer A)."""
    cfg = _delegate()
    if cfg is None:
        return None
    return _degrade_role(cfg, _query_available_models(ctx), "delegate")


# Substrings (case-insensitive) that signal a model-unavailability error from
# a provider. Used by Layer B to auto-fall-back an escalated/delegated side
# query to the executor's own model.
_MODEL_UNAVAILABLE_SIGNATURES = (
    "not_found_error",
    "model not found",
    "does not exist",
    "not available",
    "invalid model",
    "unknown model",
    " 400",
    "400 ",
    "http 400",
    "404",
)

# Context-overflow markers — these have a dedicated hint path in
# _side_query_error and must NOT be treated as model-unavailability.
_OVERFLOW_MARKERS = (
    "context window",
    "context length",
    "maximum context",
    "token limit",
    "too many tokens",
    "exceeds",
)


def _is_model_unavailable_error(msg: str) -> bool:
    """True when *msg* looks like a provider 'model unavailable' error.

    Deliberately excludes context-overflow errors (which already have their
    own hint path) so Layer B doesn't swallow them.
    """
    low = msg.lower()
    if any(m in low for m in _OVERFLOW_MARKERS):
        return False
    return any(s in low for s in _MODEL_UNAVAILABLE_SIGNATURES)



# ---------------------------------------------------------------------------
# Helper: extract text from a tool result
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
# Card-publishing streaming wrapper
# ---------------------------------------------------------------------------

# Coalescing thresholds for card updates while a side_query is streaming.
# Avoid hammering the atomic temp+rename cycle on every delta — at ~250ms
# / 256-byte cadence the card slug is still snappy in observe_session
# while the disk write rate stays sane.
_CARD_THROTTLE_SECONDS = 0.25
_CARD_THROTTLE_BYTES = 256

# Maximum detail payload size we keep on the running card. Truncated from
# the *tail* — the head of a long synthesis prompt is usually less
# interesting for "what is this advisor saying right now?" debugging.
_CARD_DETAIL_TAIL = 8000


def _slug_for_progress(partial: str) -> str:
    """Compact progress slug, e.g. "2.1kc" or "812c". Stays ≤ 24 chars."""
    n = len(partial)
    if n < 1024:
        return f"{n}c"
    return f"{n / 1024:.1f}kc"


# Pattern that matches the block summary the host attaches to "no usable
# content" errors, e.g.
#   side-query: response had no usable content (blocks: [thinking(th=0,sig=940)])
# Extracted so the card slug can show the actual failure kind
# (empty:thinking / empty:redacted / empty:noblocks) instead of a flat ERR.
_EMPTY_BLOCKS_RE = re.compile(r"no usable content \(blocks: \[([^\]]*)\]\)")
_BLOCK_TYPE_RE = re.compile(r"(\w+)\((th=(\d+),sig=(\d+)|len=(\d+))\)")


def _classify_empty_blocks(blocks_str: str) -> str:
    """Build a card slug for an empty-content side_query failure.

    Inputs look like "thinking(th=0,sig=940), text(len=0)". We surface the
    first non-empty block descriptor — sig_len > 0 with empty thinking is
    the canonical redacted-thinking outcome.
    """
    if not blocks_str.strip():
        return "empty:noblocks"
    for m in _BLOCK_TYPE_RE.finditer(blocks_str):
        kind = m.group(1)
        if kind == "thinking":
            th = int(m.group(3) or "0")
            sig = int(m.group(4) or "0")
            if th == 0 and sig > 0:
                return "empty:redacted"
            return "empty:thinking"
        if kind == "text":
            return "empty:text"
        if kind == "toolCall":
            return "empty:toolcall"
    return "empty"


def _run_side_query_with_card(
    ctx: fir_ext.Context,
    question: str,
    *,
    model: str | None,
    provider: str | None,
    effort: str | None,
) -> tuple[str | None, str | None]:
    """Run a streaming side_query and publish a card for the whole lifecycle.

    Returns ``(text, error)`` — exactly one is non-None. On success the
    full text is returned; on failure the error string is returned. The
    card identified by ``query/<unix-ms>`` is updated in place: starts at
    slug ``"running"``, ticks through size slugs as text accumulates, and
    settles on the LLM's finish reason (``"stop"``), the block-summary
    classification for empty responses (``"empty:redacted"`` etc.), or
    ``"ERR"`` for everything else. The card is **not** cleared on
    completion — its presence is the whole point.
    """
    call_id = int(time.time() * 1000)
    key = f"query/{call_id}"

    # Initial running card. Detail snapshots a head of the question so the
    # card is self-describing even before any deltas arrive.
    ctx.put_observable(key, slug="running", detail=question[:2000])

    # Streaming side_query — fall back to the blocking flavor when the
    # host doesn't have streaming (older fir releases). The card still
    # gets a terminal state in both branches.
    if not hasattr(ctx, "side_query_stream"):
        try:
            text = ctx.side_query(question, model=model, provider=provider, effort=effort)
        except Exception as exc:
            err = str(exc)
            ctx.put_observable(key, slug="ERR", detail=err)
            return None, err
        if not text or not text.strip():
            ctx.put_observable(key, slug="empty", detail="advisor returned no content")
            return None, "advisor returned no content"
        ctx.put_observable(key, slug="stop", detail=text)
        return text, None

    stream = ctx.side_query_stream(question, model=model, provider=provider, effort=effort)

    partial = ""
    last_flush = time.monotonic()
    last_size = 0
    try:
        for delta in stream:
            if delta.type == "text":
                partial += delta.text
            elif delta.type == "thinking":
                # We don't fold thinking text into the final assistant
                # text — but its arrival is liveness. Bump the card so
                # observers see thinking-only periods as activity.
                now = time.monotonic()
                if (now - last_flush) >= _CARD_THROTTLE_SECONDS:
                    ctx.put_observable(
                        key,
                        slug=f"think+{_slug_for_progress(partial)}" if partial else "thinking",
                        detail=partial[-_CARD_DETAIL_TAIL:],
                    )
                    last_flush = now
                continue
            else:
                # Unknown / usage delta — nothing to accumulate.
                continue
            now = time.monotonic()
            if (now - last_flush) >= _CARD_THROTTLE_SECONDS or (
                len(partial) - last_size
            ) >= _CARD_THROTTLE_BYTES:
                ctx.put_observable(
                    key,
                    slug=_slug_for_progress(partial),
                    detail=partial[-_CARD_DETAIL_TAIL:],
                )
                last_flush = now
                last_size = len(partial)
    except Exception as exc:
        msg = str(exc)
        ctx.put_observable(key, slug="ERR", detail=f"{msg}\n\n{partial}")
        return None, msg

    if stream.error is not None:
        err = stream.error
        m = _EMPTY_BLOCKS_RE.search(err)
        if m is not None:
            slug = _classify_empty_blocks(m.group(1))
            # Use the block summary as the detail — that's the whole
            # point: post-mortem inspection without the raw response.
            ctx.put_observable(key, slug=slug, detail=err)
        else:
            ctx.put_observable(key, slug="ERR", detail=f"{err}\n\n{partial}")
        return None, err

    result = stream.result or {}
    text = result.get("text", partial)
    finish = result.get("finish_reason") or "stop"
    # Slug is the finish reason; the cards layer truncates to ≤24 chars.
    ctx.put_observable(key, slug=str(finish) or "stop", detail=text)
    return text, None


def _run_side_query_reactive(
    ctx: fir_ext.Context,
    question: str,
    *,
    model: str | None,
    provider: str | None,
    effort: str | None,
    role_label: str | None,
) -> tuple[str | None, str | None, str]:
    """Run a side query with Layer B reactive fallback.

    Runs the (possibly escalated/delegated) side query. When the call routed
    to an advisor/delegate model (``model`` is set and ``role_label`` given)
    and fails with a model-unavailability error, retry ONCE on the executor's
    own model (no overrides). Returns ``(text, error, note)``:

      * success on the routed model      → (text, None, "")
      * unavailability → executor retry  → (text, None, "<note>\\n\\n")
      * any other failure                → (None, error, "")

    The note tells the caller the advisor/delegate was unavailable so the
    answer came from the executor model; the caller drops the routing trace.
    """
    text, err = _run_side_query_with_card(
        ctx, question, model=model, provider=provider, effort=effort
    )
    if err is None:
        return text, None, ""
    if model is not None and role_label and _is_model_unavailable_error(err):
        # Memoize so subsequent escalations skip this dead model (Layer A
        # then degrades to the next live flagship instead of re-probing).
        _mark_model_unavailable(provider, model)
        text2, err2 = _run_side_query_with_card(
            ctx, question, model=None, provider=None, effort=None
        )
        if err2 is None:
            note = f"[{role_label} unavailable — answered on executor model]\n\n"
            return text2, None, note
    return None, err, ""


# ---------------------------------------------------------------------------
# Core: run an aside — side query with optional tool calls
# ---------------------------------------------------------------------------


def _run_aside(
    tools: list[dict],
    instructions: str,
    ctx: fir_ext.Context,
    escalate: bool = False,
    delegate: bool = False,
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
    delegate : bool
        When True (and a delegate model is configured), route the side query
        to the cheaper delegate model instead.  Ignored when no delegate is
        configured.  Mutually exclusive with *escalate*.

    Returns
    -------
    dict
        Structured tool result with ``content`` and ``is_error``.
    """
    if not instructions:
        return _error("instructions are required")
    if escalate and delegate:
        return _error("escalate and delegate are mutually exclusive — pick one")

    # Resolve advisor/delegate override if requested and configured. Layer A:
    # resolution is availability-aware — it degrades to the highest available
    # Anthropic flagship/Haiku when the configured default has gone away.
    advisor_used: dict[str, str] | None = None
    delegate_used: dict[str, str] | None = None
    sq_model: str | None = None
    sq_provider: str | None = None
    sq_effort: str | None = None
    if escalate:
        advisor = _resolve_advisor(ctx)
        if advisor is not None:
            sq_model = advisor["model"]
            sq_provider = advisor["provider"]
            sq_effort = advisor.get("effort")
            advisor_used = advisor
    if delegate:
        delegate_cfg = _resolve_delegate(ctx)
        if delegate_cfg is not None:
            sq_model = delegate_cfg["model"]
            sq_provider = delegate_cfg["provider"]
            sq_effort = delegate_cfg.get("effort")
            delegate_used = delegate_cfg

    # Role label used by Layer B for the executor-fallback note.
    role_label = "advisor" if advisor_used else ("delegate" if delegate_used else None)

    # No tools — pure ephemeral side query.
    if not tools:
        synthesis, err, note = _run_side_query_reactive(
            ctx,
            instructions,
            model=sq_model,
            provider=sq_provider,
            effort=sq_effort,
            role_label=role_label,
        )
        if err is not None:
            return _side_query_error(RuntimeError(err))
        # Belt-and-suspenders: SideQuery should now return an error on truly
        # empty responses, but if something slips through (e.g. whitespace-
        # only output from a provider we don't handle as carefully), surface
        # it as an explicit error so the caller doesn't see a bare trace line.
        if not synthesis or not synthesis.strip():
            return _error("advisor returned no content")
        # When note is set the answer came from the executor model (Layer B
        # fallback) — drop the advisor/delegate trace prefix.
        text = note + synthesis if note else _prefix_trace(synthesis, advisor_used, delegate_used)
        return {
            "content": [{"type": "text", "text": text}],
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
    synthesis, err, note = _run_side_query_reactive(
        ctx,
        prompt,
        model=sq_model,
        provider=sq_provider,
        effort=sq_effort,
        role_label=role_label,
    )
    if err is not None:
        return _side_query_error(RuntimeError(err))
    if not synthesis or not synthesis.strip():
        return _error("advisor returned no content")

    # Include raw tool outputs in details for TUI display (not sent to LLM).
    # Truncate individual outputs to avoid bloating the JSON-RPC response.
    max_output_len = 50 * 1024  # 50KB per tool output
    tool_outputs = []
    for r in results:
        output = r["output"]
        if len(output) > max_output_len:
            output = output[:max_output_len] + "\n... (truncated)"
        tool_outputs.append(
            {
                "name": r["name"],
                "title": r.get("title", ""),
                "output": output,
                "is_error": r.get("is_error", False),
            }
        )

    return {
        "content": [
            {
                "type": "text",
                "text": (note + synthesis)
                if note
                else _prefix_trace(synthesis, advisor_used, delegate_used),
            }
        ],
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
    fallback = advisor.get("_fallback")
    if fallback:
        return f"[advisor: {spec} (fallback: {fallback} unavailable)]\n\n{text}"
    return f"[advisor: {spec}]\n\n{text}"


def _prefix_delegate(text: str, delegate: dict[str, str] | None) -> str:
    """Prefix the synthesis with a single trace line when delegation was used.

    Mirror of _prefix_advisor — makes the cheap-model routing visible so
    both user and agent know the response came from the delegate.
    """
    if delegate is None:
        return text
    spec = _format_advisor_spec(delegate)
    fallback = delegate.get("_fallback")
    if fallback:
        return f"[delegate: {spec} (fallback: {fallback} unavailable)]\n\n{text}"
    return f"[delegate: {spec}]\n\n{text}"


def _prefix_trace(
    text: str,
    advisor: dict[str, str] | None,
    delegate: dict[str, str] | None,
) -> str:
    """Apply whichever trace prefix applies (at most one can be non-None)."""
    return _prefix_delegate(_prefix_advisor(text, advisor), delegate)


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
    """Build the aside tool description, growing escalation/delegation guidance only when configured."""
    base = (
        "Ephemeral side query with optional multi-tool orchestration. "
        "Everything happens off to the side — nothing enters conversation "
        "history, only the synthesis is returned.\n\n"
        "With tools: executes them, collects outputs, synthesises via LLM.\n"
        "Without tools: runs a pure ephemeral side question against current context.\n\n"
        "Use your fast (current) model with this tool to gather data, collect context, "
        "ask quick questions, or investigate issues without polluting history."
    )
    if _advisor() is not None:
        base += (
            "\n\nAdvisor escalation: set 'escalate' to true to route this side query "
            "to a stronger advisor model. See the session-start [SYS_EXT] note for "
            "when escalation is warranted — the principle is judgement-call cost, "
            "not a checklist of categories."
        )
    if _delegate() is not None:
        base += (
            "\n\nDelegation: set 'delegate' to true to route this side query to a "
            "fast, cheap delegate model. Use it for context-heavy, low-judgement "
            "asides — bulk file reads + synthesis, log summarisation, data "
            "extraction — where volume is high but the reasoning is mechanical. "
            "Route by judgement density, not just size."
        )
    return base


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
    if _delegate() is not None:
        schema["properties"]["delegate"] = {
            "type": "boolean",
            "description": (
                "When true, route this side query to the configured cheap "
                "delegate model instead of the executor's current model. Use "
                "for context-heavy, low-judgement work — see the tool "
                "description. Mutually exclusive with 'escalate'."
            ),
        }
    return schema


@fir_ext.tool(
    name="aside",
    description=_aside_tool_description(),
    parameters=_aside_tool_parameters(),
    display_hint={
        "title_args": [
            {"name": "title", "style": "accent"},
            {"name": "escalate", "style": "warning", "label": "↑ escalated"},
            {"name": "delegate", "style": "muted", "label": "↓ delegated"},
        ],
    },
)
def aside(params: dict, ctx: fir_ext.Context):
    tools = params.get("tools", [])
    instructions = params.get("instructions", "")
    escalate = bool(params.get("escalate", False))
    delegate = bool(params.get("delegate", False))
    return _run_aside(tools, instructions, ctx, escalate=escalate, delegate=delegate)


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
# Command: /advise
# ---------------------------------------------------------------------------


@fir_ext.command(
    name="advise",
    description=("Ask the configured advisor model a side question. Usage: /advise <question>"),
)
def cmd_advise(args: list[str], ctx: fir_ext.Context):
    """Handle /advise — route a side question to the configured advisor model.

    Like ``/aside`` but always escalates. If no advisor is configured, point
    the user at ``/aside-advisor`` rather than silently falling back to the
    executor model — the whole point of this command is to ask a stronger
    model.
    """
    text = " ".join(args).strip()
    if not text:
        return {
            "message": (
                "Usage: /advise <question>\n\n"
                "Routes a side question to the configured advisor model.\n"
                "Configure with: /aside-advisor <provider>/<model>[:effort]"
            ),
        }

    advisor = _resolve_advisor(ctx)
    if advisor is None:
        return {
            "message": (
                "No advisor configured. Run `/aside-advisor <provider>/<model>` "
                "to enable, or use `/aside` to ask the current model."
            ),
        }

    try:
        answer = ctx.side_query(
            text,
            model=advisor["model"],
            provider=advisor["provider"],
            effort=advisor.get("effort"),
        )
    except Exception as exc:
        # Layer B: advisor unavailable → answer on the executor model.
        if _is_model_unavailable_error(str(exc)):
            _mark_model_unavailable(advisor["provider"], advisor["model"])
            try:
                answer = ctx.side_query(text)
            except Exception as exc2:
                return {"message": _side_query_error_text(exc2)}
            return {
                "message": (
                    f"**advise:** {text}\n\n"
                    "[advisor unavailable — answered on executor model]\n\n"
                    f"{answer}"
                ),
                "print_response": True,
                "markdown": True,
            }
        return {"message": _side_query_error_text(exc)}
    return {
        "message": f"**advise:** {text}\n\n{_prefix_advisor(answer, advisor)}",
        "print_response": True,
        "markdown": True,
    }


# ---------------------------------------------------------------------------
# Command: /aside-advisor
# ---------------------------------------------------------------------------


def _save_role_config(key: str, cfg: dict[str, str] | None) -> str | None:
    """Persist *cfg* under *key* in aside.json. Returns an error string on failure.

    When *cfg* is ``None``, persists the explicit opt-out marker
    (``"<key>": "off"``) so the absence of a file remains the "use default"
    signal. This keeps the contract simple:

      file missing       → use built-in default
      "<key>": "off"     → disabled
      "<key>": "p/m"     → user-pinned model

    Other keys in the file are preserved.
    """
    cfg_path = _config_path()
    if cfg_path is None:
        return f"no config dir advertised by host; cannot persist {key} config"
    try:
        cfg_path.parent.mkdir(parents=True, exist_ok=True)
        existing: dict[str, Any] = {}
        loaded = _read_existing_config()
        if isinstance(loaded, dict):
            existing = loaded
        if cfg is None:
            existing[key] = "off"
        else:
            existing[key] = _format_advisor_spec(cfg)
        cfg_path.write_text(json.dumps(existing, indent=2) + "\n")
        return None
    except OSError as exc:
        return f"failed to write {cfg_path}: {exc}"


def _save_advisor_config(cfg: dict[str, str] | None) -> str | None:
    """Persist *cfg* as the advisor model in aside.json."""
    return _save_role_config("advisor", cfg)


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


# ---------------------------------------------------------------------------
# Command: /aside-delegate
# ---------------------------------------------------------------------------


@fir_ext.command(
    name="aside-delegate",
    description=(
        "Show, set, or unset the cheap delegate model used by aside's delegate flag. "
        "Usage: /aside-delegate [provider/model[:effort] | off]"
    ),
)
def cmd_aside_delegate(args: list[str], ctx: fir_ext.Context):
    """Handle /aside-delegate — manage the persisted delegate model config."""
    spec = " ".join(args).strip()

    # Show current.
    if not spec:
        delegate = _delegate()
        if delegate is None:
            return {
                "message": (
                    "aside-delegate: disabled (delegate: off in aside.json).\n\n"
                    "Set one with:\n"
                    "  /aside-delegate anthropic/claude-haiku-4-5\n\n"
                    "Changes take effect on the next session start."
                ),
            }
        cfg_path = _config_path()
        is_default = cfg_path is None or not cfg_path.is_file()
        suffix = " (default — no aside.json)" if is_default else f" (from {cfg_path})"
        return {
            "message": (
                f"aside-delegate: {_format_advisor_spec(delegate)}{suffix}\n\n"
                "Override:  /aside-delegate <provider>/<model>[:effort]\n"
                "Disable:   /aside-delegate off"
            ),
        }

    # Unset.
    if spec.lower() in ("off", "none", "unset", "clear"):
        err = _save_role_config("delegate", None)
        if err:
            return {"message": f"aside-delegate: {err}"}
        return {
            "message": (
                "aside-delegate: disabled. The 'delegate' parameter will be "
                "removed from the aside tool on next session start. "
                "Run `/aside-delegate <provider>/<model>` to re-enable, or "
                "delete aside.json to return to the built-in default."
            ),
        }

    # Set.
    parsed = _parse_advisor_spec(spec)
    if parsed is None:
        return {
            "message": (
                f"aside-delegate: malformed spec {spec!r}.\n"
                "Expected 'provider/model' or 'provider/model:effort' "
                "(e.g. 'anthropic/claude-haiku-4-5:low')."
            ),
        }
    err = _save_role_config("delegate", parsed)
    if err:
        return {"message": f"aside-delegate: {err}"}
    return {
        "message": (
            f"aside-delegate: set to {_format_advisor_spec(parsed)}.\n"
            "Changes take effect on the next session start."
        ),
    }


fir_ext.run(name="aside")
