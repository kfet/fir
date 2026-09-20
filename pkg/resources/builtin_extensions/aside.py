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
``.fir/aside.json`` overrides ``~/.config/fir/aside.json``).  Re-read on every
use, so an edit to the file takes effect on the next escalation and
``/aside-advisor`` always reports the LIVE resolved value — this extension is
builtin, so ``ext_reload`` refuses it and there would otherwise be no way to
change or even observe the routing in a running session.  One thing is still
fixed at session start: whether the ``escalate`` parameter appears in the tool
schema at all, because the host collects tool schemas during the init
handshake.

Advisor / delegate as a fallback CHAIN
--------------------------------------

The advisor (and delegate) is an ORDERED FALLBACK CHAIN, not a single model.
The bundled default advisor chain is
``anthropic/claude-fable-5 -> claude-opus-4-8 -> claude-opus-4-7`` (Fable is
kept first: it looks live in ``/v1/models`` even while it is unavailable, so
we try it and let the failure advance the chain).  ``aside.json`` accepts a
JSON array to express a custom chain, alongside the back-compat single string:

    {"advisor": ["anthropic/claude-fable-5:high", "anthropic/claude-opus-4-8"]}
    {"advisor": "anthropic/claude-opus-4-8:high"}     # single (back-compat)
    {"advisor": "off"}                                # disabled

Resolution walks the chain in order.  Each candidate passes through the
availability/breaker filter (skip models cooling off after a failure,
degrade a pruned model to a live sibling of its tier).  A candidate that
cannot answer opens its breaker and advances; a candidate that fails for a reason
unrelated to its own health stops the walk; either way the query terminates
on the executor / current-session model, so escalation is NEVER a hard
failure just because the advisor is down.  The single exception is a failure
attributable to the REQUEST rather than the route — context overflow or a
user cancellation — which surfaces immediately, because no other model can
do anything with the same oversized prompt or the same cancelled context.
``/aside-advisor`` and ``/aside-delegate`` set a single pinned model; edit
``aside.json`` directly to configure a chain array.

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

Agentic delegate mode
---------------------

The ordered ``tools`` list requires the CALLER to know the whole chain
upfront, which defeats offloading *exploration*.  Passing ``goal`` instead
(with ``delegate=true``; the two are mutually exclusive with ``tools``) runs
an autonomous loop where the DELEGATE model chooses its own read-only tool
calls — ``read``/``glob``/``grep``/``ls``, never anything mutating — until it
can answer, bounded by ``max_iterations`` (default 8, clamped 1-20), a total
tool-output cap and a wall clock.  It is delegate-only by design: escalation
buys judgement on context already in hand, so the pattern stays "delegate
gathers -> escalate judges".  See the block comment above
``_READONLY_TOOL_NAMES`` for the mechanism and its upgrade path.

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

# Default advisor chain — LAST-RESORT FALLBACK ONLY.
#
# At runtime the default chain is resolved dynamically from
# ctx.available_models() (see _dynamic_default_chain), ranked strongest-first,
# so a new flagship is picked up with zero code changes. These constants are
# used only when that live set is empty, errors, or has no rankable Anthropic
# entries — and when an explicit aside.json value is absent.
#
# Fable is kept FIRST by explicit decision: it is NOT flagged unavailable in
# /v1/models — it looks identical to a live model, and only a runtime failure
# reveals it is down. So we try it and let that failure advance the chain to
# the next candidate. Note the runtime failure often carries NO provider error
# text at all — see _is_provider_error_without_message, which is what makes
# this chain actually degrade instead of dead-ending on the head.
# The tail Opus versions are concrete fallbacks the chain walk advances to
# when Fable (and higher Opuses) fail at runtime.
_DEFAULT_ADVISOR_SPEC = "anthropic/claude-fable-5"
_DEFAULT_ADVISOR_CHAIN = [
    _DEFAULT_ADVISOR_SPEC,
    "anthropic/claude-opus-4-8",
    "anthropic/claude-opus-4-7",
]

# Default delegate — LAST-RESORT FALLBACK ONLY, same as above: the live
# highest Haiku from ctx.available_models() wins when it is resolvable.
_DEFAULT_DELEGATE_SPEC = "anthropic/claude-haiku-4-5"

# How many candidates the dynamically-resolved advisor chain carries. Matches
# the static chain length: enough redundancy to survive a couple of dead or
# empty-responding models, bounded worst-case walk before executor fallback.
_DYNAMIC_ADVISOR_CHAIN_LEN = 3


def _config_path() -> Path | None:
    """Highest-priority path for reading/writing aside.json. None when the
    SDK has no config dirs (e.g. tests that haven't seeded any)."""
    p = fir_ext.config_path(_CONFIG_FILENAME)
    return Path(p) if p else None


def _read_existing_config() -> dict | None:
    """Read the existing aside.json from the highest-priority dir that has
    one. Returns the parsed dict, or None if no file exists / unparsable."""
    return fir_ext.load_config(_CONFIG_FILENAME)


def _parse_advisor_chain(specs: list[str]) -> list[dict[str, str]]:
    """Parse a list of spec strings into a list of config dicts.

    Malformed elements are skipped silently. Returns a possibly-empty list.
    """
    out: list[dict[str, str]] = []
    for spec in specs:
        if not isinstance(spec, str):
            continue
        parsed = _parse_advisor_spec(spec)
        if parsed is not None:
            out.append(parsed)
    return out


def _load_role_config(
    key: str, default: str | list[str]
) -> dict[str, str] | list[dict[str, str]] | None:
    """Back-compat wrapper around :func:`_load_role_config_source`."""
    return _load_role_config_source(key, default)[0]


def _load_role_config_source(
    key: str, default: str | list[str]
) -> tuple[dict[str, str] | list[dict[str, str]] | None, bool]:
    """Read a model-role config (advisor or delegate) from aside.json.

    The ``"<key>"`` value may be:
      * ``null`` / ``"off"`` / ``"none"`` / ``""`` → the role is disabled
        (returns ``None``).
      * a string ``"provider/model[:effort]"`` → a single spec, returned as a
        dict (back-compat).
      * an array of such strings → an ordered fallback CHAIN, returned as a
        list of dicts (malformed elements skipped; an all-malformed / empty
        array falls through to the default).
      * missing / unparsable file → the bundled default (which may itself be a
        chain, e.g. the advisor default).

    Returns ``(value, from_default)`` where value is a dict (single spec), a
    non-empty list of dicts (chain), or ``None`` (disabled), and
    ``from_default`` is True when the bundled default was used — the signal
    that runtime dynamic resolution may replace it. An explicit config value
    (including an explicit opt-out) always reports False.
    """
    data = _read_existing_config()
    if isinstance(data, dict) and key in data:
        value = data[key]
        # Explicit opt-out.
        if value is None or (
            isinstance(value, str) and value.strip().lower() in ("", "off", "none")
        ):
            return None, False
        if isinstance(value, str):
            parsed = _parse_advisor_spec(value)
            if parsed is not None:
                return parsed, False
            # Malformed entry — fall through to default rather than disable.
        elif isinstance(value, list):
            chain = _parse_advisor_chain(value)
            if chain:
                return chain, False
            # All elements malformed — fall through to default.

    if isinstance(default, list):
        chain = _parse_advisor_chain(default)
        return (chain, True) if chain else (None, True)
    return _parse_advisor_spec(default), True


def _load_advisor_config() -> dict[str, str] | list[dict[str, str]] | None:
    """Advisor model config — defaults to the bundled flagship-first chain."""
    return _load_role_config("advisor", _DEFAULT_ADVISOR_CHAIN)


def _load_delegate_config() -> dict[str, str] | list[dict[str, str]] | None:
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


def _format_role_config(cfg: dict[str, str] | list[dict[str, str]]) -> str:
    """Render a role config (single spec dict OR a chain list) for display.

    A chain is rendered as ``a -> b -> c`` using the same per-spec form.
    """
    if isinstance(cfg, list):
        return " -> ".join(_format_advisor_spec(c) for c in cfg)
    return _format_advisor_spec(cfg)


# Advisor/delegate config overrides. Normally UNSET, in which case the config
# is re-read from disk on every access — aside.json is two stat calls and a
# small JSON parse, which is free next to the LLM call it is about to route,
# and it removes a genuine trap: `aside` is a builtin extension so `ext_reload`
# refuses it, which meant an edit to aside.json could not take effect in a
# running session AND `/aside-advisor` reported a stale memoized value with no
# way to observe the live one. Reading per call makes `/aside-advisor` the
# live view. Tests (and only tests) inject by assigning _ADVISOR / _DELEGATE
# directly; a non-UNSET value short-circuits the read.
#
# Only a value that came from the bundled static default is eligible for
# runtime dynamic resolution against ctx.available_models() — an explicit
# aside.json value always wins. That is carried by the ``from_default``
# boolean the loader already returns (see _load_role_config_source), reported
# per call by ``_advisor_source`` / ``_delegate_source``. It deliberately is
# NOT recorded as the identity of a memoized object: re-reading per call
# yields a fresh object every time, so identity could never match. An
# injected _ADVISOR / _DELEGATE counts as explicit and is used verbatim.
_ADVISOR_UNSET = object()
_ADVISOR: Any = _ADVISOR_UNSET
_DELEGATE: Any = _ADVISOR_UNSET


def _advisor_source() -> tuple[dict[str, str] | list[dict[str, str]] | None, bool]:
    """Advisor config plus whether it came from the bundled default."""
    if _ADVISOR is not _ADVISOR_UNSET:
        return _ADVISOR, False
    return _load_role_config_source("advisor", _DEFAULT_ADVISOR_CHAIN)


def _delegate_source() -> tuple[dict[str, str] | list[dict[str, str]] | None, bool]:
    """Delegate config plus whether it came from the bundled default."""
    if _DELEGATE is not _ADVISOR_UNSET:
        return _DELEGATE, False
    return _load_role_config_source("delegate", _DEFAULT_DELEGATE_SPEC)


def _advisor() -> dict[str, str] | list[dict[str, str]] | None:
    return _advisor_source()[0]


def _delegate() -> dict[str, str] | list[dict[str, str]] | None:
    return _delegate_source()[0]


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
# The ranking helpers below are the single source of truth: the fallback-
# sanity tests reuse them so test and runtime always agree on "which model is
# strongest/cheapest".

# Model id grammar: ``claude-<tier>-<major>[-<minor>][-<YYYYMMDD>]``. The minor
# is OPTIONAL for every tier — bare ``claude-opus-5`` is a real, live id and an
# earlier version of these patterns required a minor, so opus-5 was unrankable
# and the "strongest available" answer silently stayed on opus-4-8. The minor
# is capped at 2 digits and the date stamp fixed at 8 (YYYYMMDD), so the two
# groups can never be confused: given ``claude-opus-4-20250514`` the engine
# backtracks to minor=None, date=20250514.
#
# Date-stamped ids ARE ranked, but strictly BELOW the bare alias of the same
# version (the trailing element of the rank tuple: 1 for bare, 0 for dated).
# They cannot simply be rejected: on a real host the ONLY live Haiku was
# ``claude-haiku-4-5-20251001``, and dropping it disabled delegation entirely.
# Callers dedupe by the version part of the rank, so the same model is never
# probed twice, and the live id is used VERBATIM — never normalised to a bare
# alias the API may not accept.
_FABLE_RE = re.compile(r"^claude-fable-(\d+)(?:-(\d{1,2}))?(?:-(\d{8}))?$")
_OPUS_RE = re.compile(r"^claude-opus-(\d+)(?:-(\d{1,2}))?(?:-(\d{8}))?$")
_HAIKU_RE = re.compile(r"^claude-haiku-(\d+)(?:-(\d{1,2}))?(?:-(\d{8}))?$")


def _rank_flagship(model_id: str) -> tuple[int, int, int, int] | None:
    """Rank an Anthropic flagship model id. Higher tuple == stronger.

    Returns ``(tier, major, minor, bare)`` — the Fable (Mythos-class) tier
    always outranks the Opus tier, then major, then minor, and a bare id
    outranks its own date-stamped snapshot. ``None`` for non-flagship ids.
    """
    mid = model_id.strip()
    m = _FABLE_RE.match(mid)
    tier = 1
    if m is None:
        m = _OPUS_RE.match(mid)
        tier = 0
    if m is None:
        return None
    return (tier, int(m.group(1)), int(m.group(2) or 0), 0 if m.group(3) else 1)


def _rank_haiku(model_id: str) -> tuple[int, int, int] | None:
    """Rank a Haiku id by ``(major, minor, bare)``. None if not Haiku."""
    m = _HAIKU_RE.match(model_id.strip())
    if m is None:
        return None
    return (int(m.group(1)), int(m.group(2) or 0), 0 if m.group(3) else 1)


def _best_anthropic_flagship(model_ids: list[str]) -> str | None:
    """Pick the highest-ranked flagship id from *model_ids* (Fable > Opus)."""
    best_id: str | None = None
    best_rank: tuple[int, int, int, int] | None = None
    for mid in model_ids:
        r = _rank_flagship(mid)
        if r is not None and (best_rank is None or r > best_rank):
            best_rank, best_id = r, mid
    return best_id


def _best_anthropic_haiku(model_ids: list[str]) -> str | None:
    """Pick the highest-ranked Haiku id from *model_ids*."""
    best_id: str | None = None
    best_rank: tuple[int, int, int] | None = None
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


# Circuit breaker for models that failed a call this session, keyed by
# "provider/id". This is a COOLDOWN, not a tombstone — and the distinction is
# load-bearing. The original design assumed unavailability was a steady state
# ("the model is dead, stop probing it"), so one failure banned a model for the
# life of the session. But the head of the default chain is *intermittent*: it
# fails some calls and answers others. Under a permanent memo a single blip
# costs you your best advisor until the process restarts, silently and with no
# way to clear it — and because `aside` is a builtin, `ext_reload` can't even
# reset the module.
#
# So a failure opens the breaker for a backoff window instead. The window
# doubles per consecutive failure (60s → 30min cap) and a successful call
# resets it. That resolves "intermittent vs genuinely dead" empirically rather
# than by assumption: a blip costs one skipped escalation and recovers within a
# minute, while a genuinely dead model backs off to near-zero probe cost. The
# probe is worth minimising because a failing side query is slow — the agent
# layer retries with backoff before the error ever reaches us.
_UNAVAILABLE_BASE_COOLDOWN = 60.0
_UNAVAILABLE_MAX_COOLDOWN = 1800.0

# model key -> (monotonic deadline, consecutive failure count)
_UNAVAILABLE_UNTIL: dict[str, tuple[float, int]] = {}


def _model_key(provider: str | None, model: str | None) -> str:
    return f"{provider}/{model}"


def _mark_model_unavailable(provider: str | None, model: str | None) -> None:
    """Open the breaker for provider/model, backing off on repeat failures."""
    if not provider or not model:
        return
    key = _model_key(provider, model)
    _, failures = _UNAVAILABLE_UNTIL.get(key, (0.0, 0))
    failures += 1
    cooldown = min(
        _UNAVAILABLE_BASE_COOLDOWN * (2 ** (failures - 1)),
        _UNAVAILABLE_MAX_COOLDOWN,
    )
    _UNAVAILABLE_UNTIL[key] = (time.monotonic() + cooldown, failures)


def _mark_model_available(provider: str | None, model: str | None) -> None:
    """Close the breaker for provider/model — it just answered.

    Clearing the failure count (rather than only the deadline) is what stops
    an intermittent model from ratcheting itself into the 30-minute cap over a
    long session: each recovery earns it a fresh, short first backoff.
    """
    if not provider or not model:
        return
    _UNAVAILABLE_UNTIL.pop(_model_key(provider, model), None)


def _model_unavailable(provider: str | None, model: str | None) -> bool:
    """True while provider/model's breaker is open."""
    entry = _UNAVAILABLE_UNTIL.get(_model_key(provider, model))
    return entry is not None and time.monotonic() < entry[0]


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
    in_available = not _model_unavailable(cfg["provider"], cfg["model"]) and any(
        m.get("provider") == cfg["provider"] and m.get("id") == cfg["model"] for m in available
    )
    if in_available:
        return cfg
    if cfg["provider"] != "anthropic":
        return cfg
    ids = [
        m.get("id", "")
        for m in available
        if m.get("provider") == "anthropic" and not _model_unavailable("anthropic", m.get("id", ""))
    ]
    best = _best_anthropic_haiku(ids) if role == "delegate" else _best_anthropic_flagship(ids)
    if best is None:
        return None
    resolved: dict[str, str] = {"provider": "anthropic", "model": best, "_fallback": cfg["model"]}
    if cfg.get("effort"):
        resolved["effort"] = cfg["effort"]
    return resolved


def _normalise_chain(
    cfg: dict[str, str] | list | None,
) -> list[dict[str, str]]:
    """Normalise a role config (dict | list | None) to a list of spec dicts."""
    if cfg is None:
        return []
    if isinstance(cfg, dict):
        return [cfg]
    if isinstance(cfg, list):
        return [c for c in cfg if isinstance(c, dict)]
    return []


def _resolve_role_chain(
    ctx: fir_ext.Context,
    cfg: dict[str, str] | list | None,
    role: str,
) -> list[dict[str, str]]:
    """Resolve an ORDERED candidate chain for a role (Layer A).

    Each configured spec is passed through the existing availability/memo
    filter (:func:`_degrade_role`): models cooling off after a recent failure
    are skipped, and a spec whose own model has gone away degrades to
    the highest available Anthropic model of its tier (recording ``_fallback``
    for the trace). The author's ORDER is preserved — the chain is not
    collapsed to a single "best" — and duplicate models (which degrade can
    introduce) are removed keeping the first occurrence.

    Returns a possibly-empty list; empty means "nothing to try this session"
    and the caller falls back to the executor model.
    """
    specs = _normalise_chain(cfg)
    if not specs:
        return []
    available = _query_available_models(ctx)
    out: list[dict[str, str]] = []
    seen: set[str] = set()
    for spec in specs:
        # When availability is unknown ([]), _degrade_role can't rank a live
        # sibling, so it would return a cooling-off model unchanged and we'd
        # re-probe it every call. Skip it here. (When availability IS known,
        # _degrade_role handles the breaker itself by degrading to a live model
        # of the same tier — e.g. cooling fable -> opus — so we must NOT skip
        # in that case or we'd lose that substitution.)
        if not available and _model_unavailable(spec["provider"], spec["model"]):
            continue
        resolved = _degrade_role(spec, available, role)
        if resolved is None:
            continue
        key = _model_key(resolved["provider"], resolved["model"])
        if key in seen:
            continue
        seen.add(key)
        out.append(resolved)
    return out


def _dynamic_default_chain(ctx: fir_ext.Context, role: str) -> list[dict[str, str]] | None:
    """Resolve a role's DEFAULT chain from the live model registry.

    The hardcoded ``_DEFAULT_ADVISOR_CHAIN`` / ``_DEFAULT_DELEGATE_SPEC``
    constants drift: hosts have run ``claude-opus-5`` as their executor while
    the baked chain tail was opus-4-8/opus-4-7 — i.e. escalation could route
    to a model WEAKER than the one already running. Resolving from
    ``ctx.available_models()`` (the session registry's live-and-authed set, a
    local RPC — no network) picks up new flagships with zero code changes.

    Ranking reuses the existing helpers, so runtime and tests agree:
      * advisor — ``_rank_flagship`` (Fable tier > Opus tier, then major,
        minor), strongest first, top ``_DYNAMIC_ADVISOR_CHAIN_LEN``.
      * delegate — ``_best_anthropic_haiku``, a single-element chain.
    Date-stamped ids rank below the bare alias of the same version and are
    deduped away when both are live, so the same model is never probed twice —
    but they are kept when the snapshot is the ONLY live spelling (observed:
    a host whose sole live Haiku was ``claude-haiku-4-5-20251001``). Sonnet is
    deliberately excluded — escalating to a mid tier defeats the purpose, and
    the executor fallback already covers "no flagship available".

    Returns ``None`` when the live set is empty/unavailable/has no rankable
    Anthropic entries — the caller then keeps the static constants. Never
    raises: any failure degrades to the static chain.
    """
    try:
        available = _query_available_models(ctx)
        # Sorted + de-duplicated for deterministic ordering within a rank.
        ids = sorted({m.get("id", "") for m in available if m.get("provider") == "anthropic"})
        if role == "delegate":
            best = _best_anthropic_haiku(ids)
            return [{"provider": "anthropic", "model": best}] if best else None
        ranked = [(r, mid) for mid in ids if (r := _rank_flagship(mid)) is not None]
        if not ranked:
            return None
        # Rank descending; `ids` is already ascending so ties stay stable.
        ranked.sort(key=lambda p: p[0], reverse=True)
        # Dedupe by VERSION (tier, major, minor) — a date-stamped id is the
        # same model as its bare alias and must not be probed twice. Bare
        # sorts first within a version, so keeping the first occurrence keeps
        # the bare form; when only the snapshot is live it is kept verbatim.
        out: list[dict[str, str]] = []
        seen: set[tuple[int, int, int]] = set()
        for rank, mid in ranked:
            version = rank[:3]
            if version in seen:
                continue
            seen.add(version)
            out.append({"provider": "anthropic", "model": mid})
            if len(out) == _DYNAMIC_ADVISOR_CHAIN_LEN:
                break
        return out
    except Exception:
        return None


def _resolve_role_chain_dynamic(
    ctx: fir_ext.Context,
    cfg: dict[str, str] | list | None,
    role: str,
    from_default: bool,
) -> list[dict[str, str]]:
    """Resolve a role chain, substituting the dynamic default when applicable.

    An explicit ``aside.json`` value (including ``off``/``none``, which yields
    ``cfg is None``) ALWAYS wins — the dynamic chain replaces only the bundled
    static default.
    """
    if cfg is not None and from_default:
        dynamic = _dynamic_default_chain(ctx, role)
        if dynamic:
            cfg = dynamic
    return _resolve_role_chain(ctx, cfg, role)


def _resolve_advisor_chain(ctx: fir_ext.Context) -> list[dict[str, str]]:
    """Availability-aware advisor chain resolution (Layer A)."""
    cfg, from_default = _advisor_source()
    return _resolve_role_chain_dynamic(ctx, cfg, "advisor", from_default)


def _resolve_delegate_chain(ctx: fir_ext.Context) -> list[dict[str, str]]:
    """Availability-aware delegate chain resolution (Layer A)."""
    cfg, from_default = _delegate_source()
    return _resolve_role_chain_dynamic(ctx, cfg, "delegate", from_default)


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

# User-cancellation markers. An aborted side query must never mark a model
# as dead (the user hit Ctrl-C; the model said nothing about its own health)
# and must never trigger a retry on the executor — firing another LLM call
# after a cancel is actively hostile.
_ABORT_MARKERS = (
    "context canceled",
    "context cancelled",
    "stop_reason=aborted",
    "operation was canceled",
    "operation was cancelled",
)


def _is_request_shaped_error(msg: str) -> bool:
    """True when *msg* is attributable to the REQUEST, not to the ROUTE.

    Two classes qualify: context overflow (a different model receives the
    identical oversized prompt, so retrying is futile *and* expensive — it
    re-sends a whole context window of input tokens) and user cancellation.
    Both must surface immediately with their own handling rather than
    advancing the chain or falling through to the executor.
    """
    low = msg.lower()
    return any(m in low for m in _OVERFLOW_MARKERS) or any(m in low for m in _ABORT_MARKERS)


# Pattern that matches the block summary the host attaches to "no usable
# content" errors, e.g.
#   side-query: response had no usable content (blocks: [thinking(th=0,sig=940)])
# Shared by two consumers: the card slug (so a failure shows its actual kind
# instead of a flat ERR) and the transient empty-content classifier.
_EMPTY_BLOCKS_RE = re.compile(r"no usable content \(blocks: \[([^\]]*)\]\)")
_BLOCK_TYPE_RE = re.compile(r"(\w+)\((th=(\d+),sig=(\d+)|len=(\d+))\)")


def _classify_empty_blocks(blocks_str: str) -> str:
    """Classify an empty-content side_query failure by its block summary.

    Inputs look like "thinking(th=0,sig=940), text(len=0)". We surface the
    first non-empty block descriptor — sig_len > 0 with empty thinking is
    the canonical redacted-thinking outcome. An empty input means the
    response carried no blocks at all.

    This drives the card SLUG **and** one routing decision: ``empty:redacted``
    is split out of the transient class by
    :func:`_is_redacted_thinking_error` (see its docstring for the 8/8
    evidence). Every other slug here shares the transient empty-content
    routing — same-candidate retry, chain advance, executor fallback.
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


# The synthesised error text the agent dependency (>= v0.1.3) produces when a
# provider asserts a failure but supplies no diagnosis:
#
#   provider reported stop_reason=error with no error message
#       (provider=anthropic model=claude-fable-5 blocks: [])
#
# This is the ONE signal that a candidate's call hard-failed, as opposed to a
# live model producing nothing useful. Before v0.1.3 the two were
# indistinguishable: a message with StopReasonError but an empty ErrorMessage
# fell through to the degenerate-content arm of SimplePrompt, was re-rolled
# three times, and surfaced as ``response had no usable content (blocks: [])
# (stop_reason=error)`` — a failed call wearing the costume of an empty
# generation, which is exactly why the advisor chain used to dead-end on it.
# v0.1.3 intercepts StopReasonError first and synthesises the text above, so
# the two classes are finally separable, and this extension classifies them
# differently: hard failure cools the model off, empty content does not.
#
# Matched as the minimal stable SUBSTRING deliberately. The parenthesised
# ``(provider=… model=… blocks: …)`` tail is the part most likely to drift
# upstream, and a hard failure that managed to emit some blocks first
# (partial stream, then a silent error) is the same provider assertion —
# splitting it off would be machinery for a speculative subcase. Note the
# text matches none of _MODEL_UNAVAILABLE_SIGNATURES (upstream deliberately
# keeps it clear of every retryable-error pattern), so this rule is the only
# thing that classifies it.
_PROVIDER_ERROR_NO_MESSAGE = "provider reported stop_reason=error with no error message"


def _is_provider_error_without_message(msg: str) -> bool:
    """True when the provider asserted an error but gave no diagnosis.

    Treated as unavailability — the breaker opens and the walk advances.
    The alternative (advance without cooling off) would make the verdict hinge
    on how chatty the provider happened to be: a 404 with prose cools the
    model off, the identical failure without prose would not. Being wrong
    costs one skipped escalation for ``_UNAVAILABLE_BASE_COOLDOWN``, reset by
    the model's next success.

    It is deliberately NOT retried on the same candidate: upstream classifies
    this text as terminal (it matches none of ``ai.IsRetryableError``'s
    patterns and fails on the first attempt, with no re-roll), and
    re-litigating that here would just spend a round trip to reach the same
    verdict with less information.
    """
    return _PROVIDER_ERROR_NO_MESSAGE in msg.lower()


# Empty-content markers. The host raises this class when the provider returns
# a response carrying no usable text — typically a lone redacted thinking
# block, e.g.
#   side-query: response had no usable content (blocks: [thinking(th=0,sig=940)])
# or the no-blocks variant "(blocks: [])". Telemetry (2026-08-11) puts this at
# ~57% of aside calls on one host and ~27% on another, with no config
# correlation and no burst pattern — it is a TRANSIENT upstream blip: the same
# question re-probed hours later succeeds immediately.
#
# The ``(blocks: [])`` variant is included, and that is a deliberate reversal
# of an earlier structural rule that read an empty block list as proof of a
# failed call. It WAS proof, against agent < v0.1.3, where a provider error
# with no message was laundered into this exact wording. From v0.1.3 that
# case is intercepted upstream and surfaces as _PROVIDER_ERROR_NO_MESSAGE
# instead, so every remaining "no usable content" error comes from a stream
# that did not error — a live model that genuinely rendered nothing. Treating
# the whole class as transient is therefore both what the fleet telemetry
# measured and what the dependency now guarantees.
#
# The class stays the catch-all for ambiguity by design: a provider that
# reports neither an error nor a stop reason still lands here (``(blocks: [])
# (stop_reason=)``) and degrades gracefully — retry, advance, executor
# fallback — rather than dead-ending or cooling off a healthy model.
#
# NOTE (design): the durable fix belongs in fir's Go SideQuery streaming path —
# one transport-level retry there would fix this for every side_query caller
# and make the same-candidate retry below redundant. This extension-level
# handling is the graceful-degradation layer (chain advance + executor
# fallback are policy only the extension knows), not the root fix. If the Go
# client ever retries empty content itself, delete the same-candidate retry
# here and keep the advance.
#
# ONE member of the class is NOT transient and is routed separately:
# ``empty:redacted`` (see _is_redacted_thinking_error). Keep the split.
_EMPTY_CONTENT_MARKERS = (
    "no usable content",
    "returned no content",
)

# Backoff before the single same-candidate retry on empty content. Module
# constant so tests can zero it.
_EMPTY_CONTENT_RETRY_BACKOFF = 2.0


def _is_empty_content_error(msg: str) -> bool:
    """True when *msg* is the transient 'response had no usable content' class.

    Covers both the block-summary form (``... (blocks: [thinking(th=0,sig=N)])``,
    including the empty ``(blocks: [])`` variant) and the bare
    no-blocks/blocking-path wording. This is its OWN failure class: unlike a
    model-unavailability 404 it is transient, so the model is never cooled
    off; unlike overflow/cancellation errors it IS retried.
    """
    if not msg:
        return False
    if _is_provider_error_without_message(msg):
        # DEFENSIVE, not load-bearing: today's synthesised text contains
        # neither marker, so this cannot fire. It declares the precedence
        # anyway — if upstream ever folds the block-summary wording into the
        # synthesised message, a hard failure must not silently become a
        # retryable blip. The pinning test locks that in.
        return False
    if _EMPTY_BLOCKS_RE.search(msg) is not None:
        return True
    low = msg.lower()
    return any(m in low for m in _EMPTY_CONTENT_MARKERS)


# Effort value that turns reasoning off entirely for one side_query. Supported
# end-to-end already: extension param ``effort`` → SideQueryOptions.Effort →
# ai.ThinkingOff (docs/extension-protocol.md, "side_query"). No host change is
# needed to use it, which is exactly why the degraded retry below is a
# Python-only fix.
_REASONING_OFF_EFFORT = "off"


def _resolved_effort_from_error(stream: Any) -> str | None:
    """Read the actually-dispatched reasoning level off a FAILED stream.

    The host attaches ``{"reasoning_effort": "..."}`` to the JSON-RPC error's
    ``data`` object (docs/extension-protocol.md, "side_query"). Older hosts
    and older SDKs have neither the field nor ``error_data`` — both degrade to
    ``None``, i.e. "unknown", which callers must treat as "assume we got what
    we asked for" so behaviour on an old host is unchanged.
    """
    data = getattr(stream, "error_data", None)
    if not isinstance(data, dict):
        return None
    value = data.get("reasoning_effort")
    return str(value) if value else None


def _reasoning_off_honoured(resolved: str | None) -> bool:
    """True unless the host positively reports that ``off`` was downgraded.

    ``resolved`` is what the transport actually dispatched. ``ai.StreamSimple``
    downgrades thinking-off to MINIMAL effort for always-on adaptive models
    (``model.Reasoning && model.AdaptiveThinking``) — thinking stays on and its
    trace is still scrubbed, so the "retry with reasoning off" was in fact a
    byte-identical second call. Unknown (``None``) means an old host that does
    not report the level; assume honoured, which preserves the pre-existing
    behaviour rather than inventing a downgrade nobody observed.
    """
    return resolved is None or resolved == _REASONING_OFF_EFFORT


def _can_disable_thinking(model_info: Mapping[str, Any]) -> bool:
    """True when this model can genuinely run with reasoning turned off.

    A model that does not reason at all trivially qualifies. One that reasons
    adaptively (``adaptive_thinking``) does NOT: its thinking is always on and
    an ``effort="off"`` request is silently downgraded.
    """
    if not model_info.get("reasoning"):
        return True
    return not model_info.get("adaptive_thinking")


def _prefer_thinking_disablable(
    ctx: fir_ext.Context, candidates: list[dict[str, str]]
) -> list[dict[str, str]]:
    """Reorder *candidates* so ones that can really disable thinking come first.

    Called only after a reasoning-off retry turned out to be a no-op: at that
    point the walk still has no evidence about the INPUT, and the cheapest way
    to get some is a candidate where "off" is actually honoured. Stable, and a
    pure reordering — nothing is dropped, so a host that reports no model flags
    (older fir) leaves the chain exactly as it was.
    """
    flags: dict[tuple[str, str], bool] = {}
    for m in _query_available_models(ctx):
        provider, mid = m.get("provider"), m.get("id")
        if provider and mid:
            flags[(str(provider), str(mid))] = _can_disable_thinking(m)
    if not flags:
        return candidates
    return sorted(
        candidates,
        key=lambda c: 0 if flags.get((c.get("provider", ""), c.get("model", "")), False) else 1,
    )


def _is_redacted_thinking_error(msg: str) -> bool:
    """True when a side_query came back as REDACTED REASONING ONLY.

    Shape: ``no usable content (blocks: [thinking(th=0,sig=N>0)])`` with
    ``stop_reason=stop`` — the model reasoned (a signature was produced), the
    provider scrubbed the trace, and because all of the output had gone into
    reasoning, nothing at all came back.

    This is a SUBSET of the transient empty-content class and it is routed
    DIFFERENTLY, which is the one thing a future reader must not "simplify"
    away. Forensics from the 2026-09-08 incident (session
    ``…c8bc9b8829d30…``, per-attempt records in its ``.cards`` sibling):

      * ONE ``aside escalate=true`` call produced EIGHT consecutive attempts
        in 77s — three distinct models, the executor fallback, and both
        same-candidate retries. ALL 8 returned ``empty:redacted``, with sig
        varying 544..1216: every candidate really did reason, and every trace
        was scrubbed.
      * 31 seconds later a differently-phrased question succeeded on the
        FIRST candidate. No model was down.

    ``SideQueryStream`` snapshots the ENTIRE session transcript and appends
    the question, so every candidate in the chain receives byte-identical
    input. The original inference from that — "the trigger is
    ``question x transcript``, deterministic on it, therefore never advance" —
    over-read the evidence. The experiment could not have distinguished input
    from route: a ``effort="off"`` request is DOWNGRADED to minimal effort on
    an always-on adaptive model (``ai.resolveReasoning``), and all eight
    attempts were adaptive Anthropic models, so not one of them ever ran with
    thinking actually disabled. The 8/8 observation stands; "advancing cannot
    help" does not follow from it.

    What the routing does with that: redacted still does NOT advance
    immediately — it retries ONCE with reasoning off, because with nothing
    left to scrub the model must emit text or a legible refusal. But the walk
    now checks whether the host HONOURED that request. If it did and the
    response is still redacted, the input really is the remaining explanation
    and the caller gets a diagnosis telling it to reword (which is what worked
    in the incident). If the request was downgraded, nothing was tested: the
    walk advances, preferring a candidate that can genuinely disable thinking.
    """
    if not msg or not _is_empty_content_error(msg):
        return False
    m = _EMPTY_BLOCKS_RE.search(msg)
    return m is not None and _classify_empty_blocks(m.group(1)) == "empty:redacted"


def _redacted_diagnosis(role_label: str | None, attempts: int, errs: list[str]) -> str:
    """Compose the terminal error for a redacted-reasoning-only failure.

    Emitted ONLY after a retry that genuinely ran with thinking disabled and
    was still redacted — see :func:`_reasoning_off_honoured`. With nothing
    left to scrub, an empty response points at the input rather than at the
    reasoning trace.

    ``advisor chain exhausted: <block summaries>`` told the calling agent
    nothing it could act on — it looked like flaky infrastructure, so the
    agent's only sane move was to give up or retry the identical call. The
    diagnosis has to name the actual cause (the INPUT, not the route) and the
    only available remedy (the caller rewording the question), while keeping
    the per-candidate block summaries as evidence.
    """
    role = role_label or "side query"
    joined = "; ".join(errs)
    return (
        f"{role} stopped after {attempts} attempt(s): every attempt returned "
        "REDACTED REASONING ONLY — a scrubbed thinking block with no text — "
        "including a retry that really did run with reasoning disabled, where "
        "there was nothing to scrub. That points at the INPUT: the question "
        "combined with the current session context is very likely tripping the "
        "provider's content policy, which scrubs the reasoning trace and leaves "
        "nothing to return. Another model would receive the byte-identical "
        "transcript, so advancing is unlikely to help. "
        "FIX (only the calling agent can do this): rephrase the question — make it "
        "narrower and less charged, and avoid asking about sensitive material "
        f"quoted earlier in the session — then call again. Evidence: {joined}"
    )


def _redacted_no_off_diagnosis(role_label: str | None, attempts: int, errs: list[str]) -> str:
    """Terminal error when NO attempt could actually disable thinking.

    Every candidate came back REDACTED REASONING ONLY, but each one has
    always-on adaptive thinking, so the "retry with reasoning off" was
    downgraded to minimal effort and never tested the hypothesis. Saying "we
    tried with reasoning disabled" here would be false, and telling the caller
    to reword would be a guess — so the diagnosis reports exactly what is
    known and names both remedies.
    """
    role = role_label or "side query"
    joined = "; ".join(errs)
    return (
        f"{role} failed after {attempts} attempt(s): every attempt returned "
        "REDACTED REASONING ONLY — a scrubbed thinking block with no text — and "
        "NONE of them could be retried with reasoning genuinely disabled: every "
        "candidate has always-on adaptive thinking, so the request to turn "
        "reasoning off was downgraded to minimal effort and the retry re-ran the "
        "identical call. The cause is therefore not established: it may be the "
        "input (question plus session context tripping a content policy) or the "
        "scrub itself. FIX: rephrase the question — narrower, less charged, "
        "avoiding sensitive material quoted earlier — and/or configure an advisor "
        "candidate whose thinking can be switched off (a non-adaptive model), "
        f"which is the probe that would settle it. Evidence: {joined}"
    )


def _is_model_unavailable_error(msg: str) -> bool:
    """True when *msg* means 'this candidate cannot answer' — cool off and advance.

    Two detectors: the structural provider-asserted-error check (see
    :func:`_is_provider_error_without_message`), which is what works when the
    provider's own error text never reaches us, and a substring match against
    provider 'model unavailable' phrasings.

    Deliberately excludes context-overflow errors (which already have their
    own hint path) so Layer B doesn't swallow them. That guard is load-bearing
    rather than redundant: ``prompt exceeds maximum context length: 400012
    tokens`` contains the substring `` 400`` and would otherwise be read as an
    unavailability status code.
    """
    low = msg.lower()
    if any(m in low for m in _OVERFLOW_MARKERS):
        return False
    if _is_provider_error_without_message(msg):
        return True
    return any(s in low for s in _MODEL_UNAVAILABLE_SIGNATURES)


# Cap for an error quoted inline in a routing note — long enough to identify
# the fault, short enough to keep the note a single readable line.
_SHORT_ERROR_CHARS = 120


def _short_error(msg: str) -> str:
    """Condense *msg* to one short line for inclusion in a routing note.

    The note is what a future reader (or a future misdiagnosis) has to work
    from, so it must say what actually went wrong rather than asserting an
    unavailability we did not establish.
    """
    flat = " ".join(msg.split())
    if len(flat) > _SHORT_ERROR_CHARS:
        return flat[: _SHORT_ERROR_CHARS - 1].rstrip() + "…"
    return flat


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


def _fmt_tokens(n: int) -> str:
    """Compact token count: 812, 4.1k, 132k."""
    if n < 1000:
        return str(n)
    if n < 100_000:
        return f"{n / 1000:.1f}k"
    return f"{round(n / 1000)}k"


_USAGE_KEYS = ("tokens_in", "tokens_out", "cache_read", "cache_write")


def _format_usage(usage: dict[str, int] | None) -> str:
    """One-line prompt-cache accounting, or "" when nothing is known.

    Renders as ``in 1.2k · read 48.3k · write 612 · out 900`` — the numbers
    that say whether the advisor path actually hit the prompt cache. ``in`` is
    the uncached prompt, ``read`` the cache hit, ``write`` the cache write.

    Requires at least one PROMPT-side counter to be non-zero. A completion
    count on its own says nothing about caching, and padding the other three
    with zeros would read as "nothing was cached" when the truth is "the host
    didn't tell us".
    """
    if not usage:
        return ""
    tin = int(usage.get("tokens_in", 0) or 0)
    read = int(usage.get("cache_read", 0) or 0)
    write = int(usage.get("cache_write", 0) or 0)
    out = int(usage.get("tokens_out", 0) or 0)
    if not (tin or read or write):
        return ""
    return (
        f"in {_fmt_tokens(tin)} · read {_fmt_tokens(read)} · "
        f"write {_fmt_tokens(write)} · out {_fmt_tokens(out)}"
    )


def _append_usage(text: str, usage: dict[str, int] | None) -> str:
    """Append the compact usage footer to a tool result body, if known."""
    footer = _format_usage(usage)
    if not footer:
        return text
    return f"{text}\n\n[{footer}]"


def _merge_usage(*parts: Mapping[str, int] | None) -> dict[str, int]:
    """Sum token counters across several LLM calls.

    An escalation is not always one request: the empty-content retry can fire
    a second attempt on the same model, and the chain walk can burn several
    candidates before one answers. Every one of those is real spend. Reporting
    only the last attempt's numbers hides the others — and actively misleads,
    since a retry re-writes the cache under a different key (the retry forces
    thinking off, and Anthropic's cache key includes the thinking config).
    """
    total = dict.fromkeys(_USAGE_KEYS, 0)
    for part in parts:
        if not part:
            continue
        for k in _USAGE_KEYS:
            total[k] += int(part.get(k, 0) or 0)
    return total


def _usage_from_result(result: Mapping[str, Any]) -> dict[str, int]:
    """Extract the token counters from a side_query result mapping."""
    return {k: int(result.get(k, 0) or 0) for k in _USAGE_KEYS}


def _run_side_query_with_card(
    ctx: fir_ext.Context,
    question: str,
    *,
    model: str | None,
    provider: str | None,
    effort: str | None,
) -> tuple[str | None, str | None, dict[str, int], str | None]:
    """Run a streaming side_query and publish a card for the whole lifecycle.

    Returns ``(text, error, usage, resolved_effort)`` — exactly one of
    text/error is non-None. ``usage`` carries the call's token counters (empty
    when unknown). ``resolved_effort`` is the reasoning level the host
    ACTUALLY dispatched with, or ``None`` when the host doesn't report it
    (blocking flavor, older fir): it is NOT always the level we asked for —
    ``off`` is downgraded to minimal effort on always-on adaptive models. The
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
            return None, err, {}, None
        if not text or not text.strip():
            ctx.put_observable(key, slug="empty", detail="advisor returned no content")
            return None, "advisor returned no content", {}, None
        ctx.put_observable(key, slug="stop", detail=text)
        return text, None, {}, None

    stream = ctx.side_query_stream(question, model=model, provider=provider, effort=effort)

    partial = ""
    usage: dict[str, int] = {}
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
            elif delta.type == "usage":
                # Terminal token accounting — keep it for the card footer
                # and the tool result, nothing to accumulate into text.
                usage = {
                    "tokens_in": delta.tokens_in,
                    "tokens_out": delta.tokens_out,
                    "cache_read": delta.cache_read,
                    "cache_write": delta.cache_write,
                }
                continue
            else:
                # Unknown delta kind — ignore.
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
        return None, msg, usage, None

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
        # A scrubbed response arrives as an ERROR, so the level the call
        # actually ran at travels in the error's structured data — this is
        # the one place the reasoning-off retry can learn it was downgraded.
        return None, err, usage, _resolved_effort_from_error(stream)

    result = stream.result or {}
    text = result.get("text", partial)
    finish = result.get("finish_reason") or "stop"
    # The terminating response is authoritative for usage; the streaming
    # delta is a fallback for hosts that only emit it there.
    from_result = _usage_from_result(result)
    if any(from_result.values()):
        usage = from_result
    # Slug is the finish reason; the cards layer truncates to ≤24 chars.
    # The usage footer makes prompt-cache behaviour visible on the card.
    footer = _format_usage(usage)
    ctx.put_observable(
        key,
        slug=str(finish) or "stop",
        detail=f"{text}\n\n— {footer}" if footer else text,
    )
    resolved = result.get("reasoning_effort") or None
    return text, None, usage, (str(resolved) if resolved else None)


def _run_side_query_chain(
    ctx: fir_ext.Context,
    question: str,
    *,
    chain: list[dict[str, str]],
    role_label: str | None,
) -> tuple[str | None, str | None, dict[str, str] | None, str, dict[str, int]]:
    """Run a side query, walking a candidate chain with executor fallback.

    Walks *chain* (an ordered list of resolved advisor/delegate specs) in
    order. Returns ``(text, error, used_cfg, note, usage)`` — ``usage`` is the
    SUMMED token accounting of every LLM call the walk made: each candidate
    probe, each empty-content retry, and the executor fallback. That is what
    the escalation cost, not what its last attempt cost:

      * A candidate succeeds → ``(text, None, <that cfg>, "")``. The caller
        renders the routing trace from ``used_cfg`` (including a
        ``(fallback: … unavailable)`` note when Layer A degraded it).
      * A candidate fails with a REQUEST-shaped error (context overflow, user
        cancellation) → surfaced immediately. Another model receives the
        identical oversized prompt, or the identical cancelled context, so
        neither advancing nor retrying can help — and overflow already has a
        dedicated hint path worth reaching fast. Checked FIRST, so neither the
        empty-content retry nor the executor fallback can fire another LLM
        call after a Ctrl-C.
      * A candidate returns EMPTY CONTENT → retried ONCE on the same candidate
        after ``_EMPTY_CONTENT_RETRY_BACKOFF``; still empty → the walk ADVANCES
        to the next candidate WITHOUT cooling the model off (the blip is
        transient, the model is alive — cooling it off would degrade the chain
        over a hiccup).
      * A candidate returns REDACTED REASONING ONLY (``empty:redacted``) → the
        same-candidate retry runs with reasoning OFF. What happens next
        depends on whether the host HONOURED that:
          - honoured and still redacted → the walk STOPS with a diagnosis.
            With nothing left to scrub, the remaining explanation is the
            input, and every other candidate receives the byte-identical
            transcript + question. Only the calling agent can fix it, by
            rewording.
          - DOWNGRADED (always-on adaptive model: ``off`` becomes minimal
            effort) → the retry was the identical call twice and proved
            nothing, so the walk ADVANCES, reordering what is left to prefer a
            candidate that can genuinely disable thinking.
      * A candidate fails with a model-unavailability error → the model is
        cooled off for a backoff window and the walk ADVANCES to the next
        candidate.
      * A candidate fails with a ROUTE-shaped error (transport, auth) → the
        model is NOT cooled off (we have no evidence it is dead) and the walk
        STOPS, terminating on the executor. Walking the rest of an
        all-Anthropic chain after a provider-wide fault (429, bad key) is N
        doomed calls; the executor is the one route guaranteed to be
        configured and warm.
      * The chain ends (exhausted or stopped) after trying ≥1 candidate →
        retry on the executor's own model (``model=None``); on success return
        ``(text, None, None, "[<role> unavailable — answered on executor
        model]\\n\\n")``, or a ``[<role> failed (…) — answered on executor
        model]`` variant naming the fault when the chain was stopped rather
        than exhausted. Escalation NEVER disables itself on a dead chain.
      * The chain is EMPTY — nothing resolvable, e.g. every candidate is
        cooling off after a recent failure — but escalation/delegation WAS
        requested (``role_label`` is not None) → the executor answers and
        still earns the unavailable note. Silence here would be the same
        class of misdirection this walk exists to prevent: the caller asked
        for advisor judgement, got the executor's own model back, and would
        otherwise have no signal that the escalation never happened.
      * No role at all (``role_label`` is None) → a plain executor call with
        no note.
      * Both the chain AND the executor fallback fail → the errors are chained
        into one message so neither is lost.
    """
    advisor_errs: list[str] = []
    failed_models: list[str] = []
    # True while every candidate failure so far was the transient
    # empty-content class — it selects honest wording for the executor note.
    empty_only = True
    # Set when the walk stopped on a route-shaped fault rather than running
    # the chain to exhaustion — it makes the executor note tell the truth
    # about *why* we ended up here instead of claiming unavailability.
    stop_err: str | None = None
    # Running token total for the WHOLE walk — every candidate probe and every
    # retry, not just the call that ended up answering. Anything less
    # under-reports what the escalation actually cost.
    spent: dict[str, int] = {}
    # Every LLM probe the walk fired, retries included — quoted in the
    # redacted diagnosis so the caller can see the cost of re-measuring a
    # constant.
    attempts_made = 0
    # True once ANY attempt in this walk really did run with thinking
    # disabled. Only then is "we tried with reasoning off and it was still
    # redacted" a statement we are entitled to make.
    genuine_off_ran = False
    walk_key = f"aside/chain/{int(time.time() * 1000)}"

    def _attempt(
        *, model: str | None, provider: str | None, effort: str | None, label: str
    ) -> tuple[str | None, str | None, dict[str, int], bool, bool]:
        """One candidate probe, with a single retry on empty content.

        Returns ``(text, err, usage, degraded, off_downgraded)``. ``degraded``
        is True only when the answer came from a retry that GENUINELY ran with
        reasoning off, so the caller can label it (a degraded path must never
        be silent). ``off_downgraded`` is True when a reasoning-off retry was
        requested but the host downgraded it — the retry then ran at the same
        effort as the first call and proves nothing.

        At most two attempts: the second happens ONLY when the first failed
        with the transient empty-content class. A request-shaped error breaks
        out even when it wears empty-content wording — a cancelled call must
        never earn another LLM call, and ``(blocks: []) (stop_reason=aborted)``
        matches the empty-content pattern.

        The second attempt's SHAPE depends on the class:

          * ``empty:redacted`` → same candidate, same question, reasoning
            turned OFF. The failure mode is "all output went into reasoning,
            reasoning got scrubbed"; with reasoning off there is nothing to
            scrub, so the model must produce text or a legible refusal — and
            the refusal is itself the diagnosis that was missing.

            That request is not always granted: an always-on adaptive model
            has ``off`` downgraded to minimal effort by the transport, so the
            "degraded retry" is a byte-identical second call. The host reports
            the level it actually used, and an unhonoured request is recorded
            as such rather than counted as a reasoning-off datapoint.
          * everything else → an identical replay, as before: those classes
            really are transient blips.

        Only the effort changes, never the question: the transcript prefix
        stays byte-identical so the prompt cache still hits on a 25-60k-token
        context. Filtering or truncating the transcript to dodge the trigger
        was considered and rejected — it invalidates that cached prefix and
        bills advisor-rate tokens on every degraded call.
        """
        nonlocal attempts_made, genuine_off_ran
        text: str | None = None
        err: str | None = None
        usage: dict[str, int] = {}
        use_effort = effort
        degraded = False
        off_downgraded = False
        reasoning_off_next = False
        for attempt in range(2):
            asked_off = False
            if attempt:
                if reasoning_off_next:
                    use_effort = _REASONING_OFF_EFFORT
                    asked_off = True
                    slug = "retry:noreason"
                    detail = (
                        f"{label} returned redacted reasoning only — retrying once "
                        f"with reasoning off\n\n{err}"
                    )
                else:
                    slug = "retry:empty"
                    detail = f"{label} returned no usable content — retrying once\n\n{err}"
                # Keep the retry honest and visible — an observer sees the
                # second probe, not a silent stall.
                ctx.put_observable(walk_key, slug=slug, detail=detail)
                if _EMPTY_CONTENT_RETRY_BACKOFF > 0:
                    time.sleep(_EMPTY_CONTENT_RETRY_BACKOFF)
            attempts_made += 1
            text, err, attempt_usage, resolved = _run_side_query_with_card(
                ctx, question, model=model, provider=provider, effort=use_effort
            )
            usage = _merge_usage(usage, attempt_usage)
            if asked_off:
                # Did the transport honour "off", or downgrade it? An
                # always-on adaptive model gets minimal effort instead, so
                # this second call was the first one again.
                if _reasoning_off_honoured(resolved):
                    genuine_off_ran = True
                    degraded = use_effort != effort
                else:
                    off_downgraded = True
                    ctx.put_observable(
                        walk_key,
                        slug="noreason:denied",
                        detail=(
                            f"{label} cannot disable thinking — asked for "
                            f"reasoning off, host dispatched "
                            f"'{resolved}'. The retry was not a reasoning-off "
                            "retry."
                        ),
                    )
            if err is None or _is_request_shaped_error(err) or not _is_empty_content_error(err):
                break
            reasoning_off_next = _is_redacted_thinking_error(err)
        if err is not None:
            # A degraded attempt that still failed produced no answer to label.
            degraded = False
        return text, err, usage, degraded, off_downgraded

    pending = list(chain)
    while pending:
        cfg = pending.pop(0)
        label = f"{cfg['provider']}/{cfg['model']}"
        text, err, usage, degraded, off_downgraded = _attempt(
            model=cfg["model"],
            provider=cfg["provider"],
            effort=cfg.get("effort"),
            label=label,
        )
        spent = _merge_usage(spent, usage)
        if err is None:
            # It answered — close its breaker so an intermittent model gets a
            # fresh short backoff next time rather than ratcheting toward the
            # cap over a long session.
            _mark_model_available(cfg["provider"], cfg["model"])
            # A later candidate answered — surface which higher-priority model
            # it stood in for, reusing the "(fallback: … unavailable)" trace
            # style. Layer A may have already set _fallback (degrade); only
            # annotate when it hasn't.
            add_fallback = bool(failed_models) and "_fallback" not in cfg
            if add_fallback or degraded:
                # Copy before annotating — _degrade_role returns the shared
                # _ADVISOR spec dict on the in-available path, so mutating it
                # in place would corrupt the session config.
                cfg = dict(cfg)
                if add_fallback:
                    cfg["_fallback"] = failed_models[0]
                if degraded:
                    cfg["_reasoning_off"] = "1"
            return text, None, cfg, "", spent
        if _is_request_shaped_error(err):
            # Attributable to the request, not the route — surface as-is.
            # Deliberately ahead of the empty-content check so a cancellation
            # can never earn a retry.
            return None, err, None, "", spent
        if _is_redacted_thinking_error(err):
            advisor_errs.append(f"{label}: {err}")
            if off_downgraded:
                # The "reasoning off" retry never happened: this candidate's
                # thinking is always on, so the transport downgraded `off` to
                # minimal effort and we simply made the same call twice. We
                # have therefore learned NOTHING about the input yet — do not
                # blame it, and do not stop. Advance, preferring a candidate
                # where disabling thinking is actually honoured, because that
                # is the probe that would settle the question.
                failed_models.append(cfg["model"])
                pending = _prefer_thinking_disablable(ctx, pending)
                ctx.put_observable(
                    walk_key,
                    slug="advance:redacted",
                    detail=(
                        f"{label} redacted-reasoning-only, and it cannot disable "
                        "thinking (the reasoning-off retry was downgraded) — "
                        "advancing to a candidate that can\n\n" + err
                    ),
                )
                continue
            # Redacted reasoning survived a retry that REALLY ran with
            # thinking disabled. Nothing was left to scrub and the response
            # was still empty, so the trigger is the input, not the reasoning
            # trace — and every remaining candidate receives the
            # byte-identical transcript and question. Stop and hand the caller
            # a diagnosis it can act on: rewording is the only fix, and only
            # the calling agent can do it.
            ctx.put_observable(
                walk_key,
                slug="stop:redacted",
                detail=(
                    f"{label} redacted-reasoning-only with reasoning genuinely off — "
                    f"stopping the walk (same input would fail on every candidate)\n\n{err}"
                ),
            )
            return (
                None,
                _redacted_diagnosis(role_label, attempts_made, advisor_errs),
                None,
                "",
                spent,
            )
        if _is_empty_content_error(err):
            # Transient — retried once already. Advance, but do NOT cool the
            # model off: it is alive, the response was just empty.
            ctx.put_observable(
                walk_key,
                slug="advance:empty",
                detail=f"{label} empty twice — advancing past it (not cooled off)\n\n{err}",
            )
            advisor_errs.append(f"{label}: {err}")
            failed_models.append(cfg["model"])
            continue
        empty_only = False
        if _is_model_unavailable_error(err):
            # Open the breaker so escalations in the next window skip this
            # model and Layer A degrades past it instead of re-probing.
            _mark_model_unavailable(cfg["provider"], cfg["model"])
            advisor_errs.append(f"{label}: {err}")
            failed_models.append(cfg["model"])
            continue
        # Route-shaped failure — the candidate could not answer, but nothing
        # says it is dead. Stop the walk and let the executor terminal
        # fallback answer rather than hard-failing the caller.
        advisor_errs.append(f"{label}: {err}")
        stop_err = err
        break

    # Chain exhausted, stopped or empty → executor terminal fallback (the same
    # empty-content retry applies: the executor model is the last hope, one
    # blip must not sink the whole call).
    text, err, usage, degraded, _ = _attempt(
        model=None, provider=None, effort=None, label="executor model"
    )
    spent = _merge_usage(spent, usage)
    if err is None:
        # A reasoning-off answer is degraded output and says so, whether or
        # not a role note also applies.
        degraded_note = (
            "[reasoning off — retried after a redacted-reasoning-only response]\n\n"
            if degraded
            else ""
        )
        # Any requested-but-unfulfilled role earns the note — whether the
        # chain died on this call (advisor_errs) or was already cooling off
        # before it started (empty chain). Only a call with no role at all
        # answers silently.
        if role_label and (advisor_errs or not chain):
            if stop_err is not None:
                note = (
                    f"[{role_label} failed ({_short_error(stop_err)}) — "
                    "answered on executor model]\n\n"
                )
            else:
                reason = (
                    "returned no usable content" if advisor_errs and empty_only else "unavailable"
                )
                note = f"[{role_label} {reason} — answered on executor model]\n\n"
            return text, None, None, note + degraded_note, spent
        return text, None, None, degraded_note, spent

    # The executor's own reasoning got scrubbed too (it can be reached with an
    # empty/exhausted-by-other-means chain). Same verdict as in the walk: the
    # input is the problem, so say so instead of reporting a route failure.
    if _is_redacted_thinking_error(err):
        return (
            None,
            (
                _redacted_diagnosis(
                    role_label, attempts_made, [*advisor_errs, f"executor model: {err}"]
                )
                if genuine_off_ran
                else _redacted_no_off_diagnosis(
                    role_label, attempts_made, [*advisor_errs, f"executor model: {err}"]
                )
            ),
            None,
            "",
            spent,
        )

    # Executor fallback ALSO failed. When we had advisor candidates, chain both
    # error sets so neither is discarded.
    if advisor_errs and role_label:
        joined = "; ".join(advisor_errs)
        how = "chain failed" if stop_err is not None else "chain exhausted"
        combined = f"{role_label} {how}: {joined}; executor fallback also failed: {err}"
        return None, combined, None, "", spent
    return None, err, None, "", spent


# ---------------------------------------------------------------------------
# Agentic delegate mode — the side model drives its own tool calls
# ---------------------------------------------------------------------------
#
# The ordered-``tools`` path requires the CALLER to know the whole chain
# upfront, which defeats offloading exploration: "grep around until you find
# where X is configured" is adaptive by nature. Agentic mode inverts it — the
# caller states a ``goal`` and the DELEGATE model decides which read-only tool
# to call next, iteration by iteration.
#
# MECHANISM (and its intended upgrade path). ``side_query`` / ``side_query_stream``
# are TOOLLESS by construction on the host side (AgentSession.SideQueryStream →
# Agent.SimplePromptStream, no tools anywhere in the wire protocol). So the loop
# runs a TEXT PROTOCOL: the allowlisted tool schemas are rendered into the
# question, the model replies with a fenced JSON block naming its next calls,
# this extension executes them via ``ctx.call_tool`` and appends the outputs to
# a scratchpad for the next iteration. The proper long-term fix is native tool
# support on SimplePrompt (host + wire + SDK); the public surface here (``goal``,
# ``allow_tools``, ``max_iterations``, the result shape) is deliberately
# mechanism-free so a native path can drop in underneath without changing it.
#
# WHY DELEGATE ONLY. Escalation exists to buy judgement on context ALREADY in
# hand; spending advisor-rate tokens on a grep loop is the opposite of that.
# The pattern stays "delegate gathers → escalate judges", so ``goal`` requires
# ``delegate=true`` and is a validation error otherwise.
#
# SCRATCHPAD GROWTH is append-only on purpose: ``SideQueryStream`` snapshots the
# whole session transcript and appends the question, so the prefix stays
# byte-identical across iterations and the prompt cache keeps hitting; only the
# scratchpad tail is uncached. Rolling or compacting it would drop exactly the
# evidence the model is reasoning over, so it is CAPPED rather than rolled —
# see the three caps below.

# Canonical read-only tools the delegate may drive. NEVER extend this with a
# tool that can write, edit, patch, execute, or otherwise mutate anything: the
# delegate is a cheap model running unattended in a loop with no human in the
# path, and "read-only" is the entire safety story. Matching against
# ctx.list_tools() is case-insensitive so provider-transformed names
# ("Read" from Anthropic OAuth) resolve to the same entry.
#
# The match is BY NAME, which rests on one assumption worth stating because
# list_tools() reports no origin field to check it against: a bare ``read`` /
# ``glob`` / ``grep`` / ``ls`` is fir's builtin. MCP tools cannot collide (they
# are ``mcp__<server>__<tool>``), but a project extension that registered a
# tool literally named ``ls`` would be reachable here. If list_tools ever grows
# an origin/source field, require builtin origin instead of trusting the name.
#
# INDIRECT PROMPT INJECTION is in scope and accepted: file contents steer the
# delegate's next reads and its final answer, which the executor then reads. The
# blast radius is bounded to read-only calls plus one returned answer — treat a
# delegate answer as untrusted data, never as instructions.
_READONLY_TOOL_NAMES = frozenset({"read", "glob", "grep", "ls"})

# Iteration budget. The default is deliberately small — an exploration that
# needs more than 8 read/grep rounds is usually a badly-scoped goal, and every
# iteration re-sends the transcript prefix.
_AGENTIC_DEFAULT_ITERATIONS = 8
_AGENTIC_MIN_ITERATIONS = 1
_AGENTIC_MAX_ITERATIONS = 20

# Total tool output fed back into the loop, across all iterations. Bounds the
# uncached tail so a delegate that greps the world cannot bill flagship-sized
# prompts on a cheap model.
_AGENTIC_MAX_TOTAL_TOOL_CHARS = 200_000

# Per-call truncation, mirroring the ordered-tools path: one giant grep must
# not consume the whole budget in a single iteration.
_AGENTIC_MAX_TOOL_OUTPUT_CHARS = 50 * 1024

# Wall-clock budget, checked BETWEEN iterations only — never interrupting an
# in-flight call, which would waste tokens already paid for.
_AGENTIC_WALL_CLOCK_SECONDS = 300.0

# Fenced ```json blocks in a model reply. Non-greedy, DOTALL — we take the
# LAST match so a model that shows an example before its real answer still
# parses correctly.
_JSON_FENCE_RE = re.compile(r"```(?:json)?\s*(\{.*?\})\s*```", re.DOTALL)


def _clamp_iterations(value: Any) -> int:
    """Clamp a caller-supplied max_iterations into the supported range.

    Non-integers (including bools, which are ints in Python but never a
    sensible iteration count) fall back to the default rather than erroring:
    a malformed budget is not worth failing an otherwise valid goal over.
    """
    if isinstance(value, bool) or not isinstance(value, int):
        return _AGENTIC_DEFAULT_ITERATIONS
    return max(_AGENTIC_MIN_ITERATIONS, min(_AGENTIC_MAX_ITERATIONS, value))


def _readonly_tool_index(ctx: fir_ext.Context) -> dict[str, Any]:
    """Map lowercased canonical name → tool spec, for read-only tools only.

    Built from the host's live tool list so an equivalent that happens to be
    absent this session simply isn't offered, and anything outside
    :data:`_READONLY_TOOL_NAMES` can never appear.
    """
    out: dict[str, Any] = {}
    try:
        available = ctx.list_tools()
    except Exception:
        return out
    for spec in available or []:
        name = (spec.get("name") or "").strip()
        if name and name.lower() in _READONLY_TOOL_NAMES:
            out[name.lower()] = spec
    return out


def _select_agentic_tools(
    ctx: fir_ext.Context, allow_tools: list | None
) -> tuple[list[Any], str | None]:
    """Resolve the tool set the delegate may call.

    Returns ``(specs, error)``. With no ``allow_tools`` the full read-only set
    present this session is offered. With ``allow_tools`` the request is
    validated against that set and ANY name outside it is an error — a typo
    must not silently widen or narrow the delegate's reach, and a rejected
    name is the only signal a caller gets that it asked for something
    forbidden.
    """
    index = _readonly_tool_index(ctx)
    if not index:
        return [], (
            "agentic mode needs at least one read-only tool "
            f"({', '.join(sorted(_READONLY_TOOL_NAMES))}) but none are registered"
        )
    if allow_tools is None:
        return [index[k] for k in sorted(index)], None
    if not isinstance(allow_tools, list) or not allow_tools:
        return [], "allow_tools must be a non-empty list of tool names"
    chosen: list[Any] = []
    seen: set[str] = set()
    bad: list[str] = []
    for name in allow_tools:
        key = str(name).strip().lower()
        if key not in index:
            bad.append(str(name))
            continue
        if key in seen:
            continue
        seen.add(key)
        chosen.append(index[key])
    if bad:
        return [], (
            f"allow_tools rejected: {', '.join(bad)} — agentic mode is read-only. "
            f"Allowed: {', '.join(sorted(index))}"
        )
    return chosen, None


def _render_tool_schemas(specs: list[Any]) -> str:
    """Render tool schemas as compact text for the delegate's prompt."""
    lines = []
    for spec in specs:
        schema = spec.get("parameters") or {}
        props = schema.get("properties") or {}
        required = schema.get("required") or []
        desc = " ".join((spec.get("description") or "").split())[:300]
        lines.append(f"- {spec['name']}: {desc}")
        for pname, pschema in props.items():
            ptype = (pschema or {}).get("type", "any")
            pdesc = " ".join(((pschema or {}).get("description") or "").split())[:120]
            req = " (required)" if pname in required else ""
            lines.append(f"    {pname}: {ptype}{req} — {pdesc}")
    return "\n".join(lines)


_AGENTIC_PROTOCOL = (
    "Reply with EXACTLY ONE fenced json block and nothing else that matters:\n"
    '  ```json\n  {"tool_calls": [{"name": "grep", "params": {"pattern": "foo"}}]}\n  ```\n'
    "to call tools (they run and you see the output on the next turn), or\n"
    '  ```json\n  {"answer": "your final answer"}\n  ```\n'
    "when you can answer the goal. Prefer several tool calls per turn when they "
    "are independent. Do not ask the user anything — you are running unattended."
)


def _build_agentic_prompt(
    goal: str,
    tool_specs: list[Any],
    log: list[str],
    *,
    final: bool,
    cap_note: str | None,
) -> str:
    """Compose the question for one iteration of the agentic loop.

    ``final=True`` builds the closing, TOOLLESS synthesis turn used when a cap
    is hit: the model is told the tools are gone and must answer from what it
    already gathered. Everything before the scratchpad is byte-stable across
    iterations so the prompt cache keeps hitting.
    """
    parts = [
        "You are running an autonomous READ-ONLY investigation off to the side of "
        "the main conversation. Work the goal below using the tools listed, then "
        "answer.\n",
        "--- Goal ---",
        goal,
        "",
    ]
    if not final:
        parts += [
            "--- Tools available to you ---",
            _render_tool_schemas(tool_specs),
            "",
            "--- Protocol ---",
            _AGENTIC_PROTOCOL,
            "",
        ]
    if log:
        parts += ["--- Work so far ---", *log, ""]
    if final:
        parts += [
            "--- Stop ---",
            f"You have hit a budget limit ({cap_note}). No further tool calls are "
            "possible. Answer the goal now, in plain text, from what you gathered "
            "above, and state plainly what remains unknown.",
        ]
    return "\n".join(parts)


def _extract_json_object(text: str) -> dict | None:
    """Pull the model's protocol object out of a reply, or None.

    Tolerates prose around the block: the LAST fenced ```json object wins, and
    failing that we scan for a bare top-level ``{…}`` with ``raw_decode``.
    Only an object carrying ``tool_calls`` or ``answer`` counts — a stray dict
    quoted in prose must not be mistaken for the protocol.
    """
    candidates: list[str] = _JSON_FENCE_RE.findall(text or "")
    decoder = json.JSONDecoder()
    if not candidates:
        for i, ch in enumerate(text or ""):
            if ch != "{":
                continue
            try:
                obj, _ = decoder.raw_decode(text[i:])
            except ValueError:
                continue
            if isinstance(obj, dict) and ("tool_calls" in obj or "answer" in obj):
                return obj
        return None
    for raw in reversed(candidates):
        try:
            obj = json.loads(raw)
        except ValueError:
            continue
        if isinstance(obj, dict) and ("tool_calls" in obj or "answer" in obj):
            return obj
    return None


def _strip_json_blocks(text: str) -> str:
    """Drop fenced json blocks from a reply, leaving only the model's prose."""
    return _JSON_FENCE_RE.sub("", text or "").strip()


def _parse_agentic_reply(text: str) -> tuple[str, Any]:
    """Classify a delegate reply.

    Returns ``("calls", [ {name, params}, … ])``, ``("answer", str)`` or
    ``("malformed", reason)``. A reply with no protocol object at all is an
    ANSWER, not an error: plain prose is the natural way a model signals it is
    done, and punishing it would burn an iteration on a correct result.
    """
    obj = _extract_json_object(text)
    if obj is None:
        return "answer", (text or "").strip()
    if "tool_calls" in obj:
        calls = obj.get("tool_calls")
        if not isinstance(calls, list) or not calls:
            return "malformed", "'tool_calls' must be a non-empty list"
        out = []
        for call in calls:
            if not isinstance(call, dict) or not str(call.get("name", "")).strip():
                return "malformed", "each tool_call needs a 'name' and optional 'params' object"
            params = call.get("params") or {}
            if not isinstance(params, dict):
                return "malformed", f"'params' for {call.get('name')!r} must be an object"
            out.append({"name": str(call["name"]).strip(), "params": params})
        return "calls", out
    answer = obj.get("answer")
    if isinstance(answer, str) and answer.strip():
        return "answer", answer.strip()
    return "malformed", "'answer' must be a non-empty string"


def _agentic_exhausted_error(errs: list[str]) -> str:
    """Terminal error when no delegate candidate could serve the loop.

    Says explicitly what the caller should do instead, because the one thing
    it must NOT do is assume the work happened.
    """
    detail = f" ({'; '.join(errs)})" if errs else ""
    return (
        "delegate chain exhausted — agentic mode deliberately does NOT fall back to "
        "the executor model, because running an autonomous tool loop on it is exactly "
        f"the cost you delegated away{detail}. Do this investigation inline instead: "
        "call the read/glob/grep/ls tools yourself."
    )


def _agentic_probe(
    ctx: fir_ext.Context,
    question: str,
    chain: list[dict[str, str]],
) -> tuple[str | None, str | None, dict[str, str] | None, dict[str, int], list[dict[str, str]]]:
    """One LLM call for the loop, walking *chain* with NO executor fallback.

    The executor fallback that :func:`_run_side_query_chain` provides is wrong
    here: running an autonomous tool loop on the executor's own model is the
    very cost the caller delegated away, and it would do it silently. So an
    exhausted chain is an ERROR the caller must handle (by doing the work
    inline), not a quiet downgrade.

    Classification mirrors the main walk — request-shaped errors surface
    immediately (a cancellation must never earn another LLM call), empty
    content retries once on the same candidate then advances without cooling
    it off, unavailability cools off and advances, anything else stops.

    Returns ``(text, err, used_cfg, usage, chain)``. The returned chain is
    rotated so the candidate that answered is at the head — the loop sticks
    with one model across iterations instead of re-walking every time.
    """
    spent: dict[str, int] = {}
    errs: list[str] = []
    for idx, cfg in enumerate(chain):
        label = f"{cfg['provider']}/{cfg['model']}"
        text: str | None = None
        err: str | None = None
        for attempt in range(2):
            if attempt and _EMPTY_CONTENT_RETRY_BACKOFF > 0:
                time.sleep(_EMPTY_CONTENT_RETRY_BACKOFF)
            text, err, usage, _ = _run_side_query_with_card(
                ctx,
                question,
                model=cfg["model"],
                provider=cfg["provider"],
                effort=cfg.get("effort"),
            )
            spent = _merge_usage(spent, usage)
            if err is None or _is_request_shaped_error(err) or not _is_empty_content_error(err):
                break
        if err is None:
            _mark_model_available(cfg["provider"], cfg["model"])
            return text, None, cfg, spent, [cfg, *chain[:idx], *chain[idx + 1 :]]
        if _is_request_shaped_error(err):
            return None, err, None, spent, chain
        errs.append(f"{label}: {err}")
        if _is_empty_content_error(err):
            continue
        if _is_model_unavailable_error(err):
            _mark_model_unavailable(cfg["provider"], cfg["model"])
            continue
        break
    return None, _agentic_exhausted_error(errs), None, spent, chain


def _summarise_params(params: dict) -> str:
    """One-line, bounded rendering of tool params for the log and the card."""
    flat = " ".join(json.dumps(params, sort_keys=True, default=str).split())
    return flat if len(flat) <= 120 else flat[:119] + "…"


def _run_agentic_delegate(
    goal: str,
    ctx: fir_ext.Context,
    *,
    chain: list[dict[str, str]],
    allow_tools: list | None,
    max_iterations: int,
) -> dict:
    """Run the agentic delegate loop and return a structured tool result.

    Iterates: ask the delegate → parse → execute allowlisted read-only tools →
    append outputs → repeat, stopping on a text answer, a cap, or an abort. On
    any cap it makes ONE final toolless synthesis call so the caller still gets
    an answer built from real evidence, and the note says which cap was hit —
    a truncated investigation presented as a complete one is worse than no
    answer at all.
    """
    tool_specs, terr = _select_agentic_tools(ctx, allow_tools)
    if terr is not None:
        return _error(terr)
    if not chain:
        return _error(_agentic_exhausted_error([]))

    index = {s["name"].lower(): s for s in tool_specs}
    log: list[str] = []
    tool_log: list[dict] = []
    spent: dict[str, int] = {}
    used_cfg: dict[str, str] | None = None
    total_chars = 0
    calls_made = 0
    iterations = 0
    cap_note: str | None = None
    malformed_streak = 0
    started = time.monotonic()
    card = f"aside/agentic/{int(time.time() * 1000)}"
    answer: str | None = None

    def _publish(slug: str) -> None:
        detail = "\n".join(
            [f"goal: {goal}", "", *(f"{e['name']} {e['args']} → {e['size']}c" for e in tool_log)]
        )
        ctx.put_observable(card, slug=slug, detail=detail)

    _publish(f"iter 0/{max_iterations}")

    while iterations < max_iterations:
        # Caps are checked BETWEEN iterations only — never mid-call, which
        # would throw away tokens already paid for.
        if time.monotonic() - started > _AGENTIC_WALL_CLOCK_SECONDS:
            cap_note = "hit time cap"
            break
        if total_chars >= _AGENTIC_MAX_TOTAL_TOOL_CHARS:
            cap_note = "hit tool-output cap"
            break
        iterations += 1
        ctx.report_progress(f"delegate iter {iterations}/{max_iterations}")
        question = _build_agentic_prompt(goal, tool_specs, log, final=False, cap_note=None)
        text, err, cfg, usage, chain = _agentic_probe(ctx, question, chain)
        spent = _merge_usage(spent, usage)
        if cfg is not None:
            used_cfg = cfg
        if err is not None:
            if _is_request_shaped_error(err):
                # Cancellation or overflow: break the loop right here. Never
                # fire another LLM call — not a retry, not the final synthesis.
                _publish("aborted")
                return _side_query_error(RuntimeError(err))
            _publish("ERR")
            return _error(err)

        kind, payload = _parse_agentic_reply(text or "")
        if kind == "answer":
            answer = payload
            break
        if kind == "malformed":
            malformed_streak += 1
            if malformed_streak >= 2:
                # Two corrective nudges in a row means the model cannot hold
                # the protocol; stop rather than spend the whole budget
                # teaching it JSON. Keep only its PROSE — echoing the broken
                # JSON block back as the "answer" would hand the caller
                # machine noise dressed up as a finding. With no prose at all
                # we leave answer unset and let the closing toolless
                # synthesis produce one from the evidence gathered so far.
                answer = _strip_json_blocks(text or "") or None
                cap_note = "malformed tool call — stopped"
                break
            log.append(
                f"[protocol error] {payload}. Your previous reply was not usable. "
                "Reply with a single fenced json block containing either "
                '"tool_calls" or "answer".'
            )
            _publish(f"iter {iterations}/{max_iterations} · protocol error")
            continue
        malformed_streak = 0

        for call in payload:
            name = call["name"].lower()
            spec = index.get(name)
            if spec is None:
                log.append(
                    f"[tool {call['name']}] refused: not in the read-only allowlist "
                    f"({', '.join(sorted(index))})"
                )
                continue
            required = (spec.get("parameters") or {}).get("required") or []
            missing = [r for r in required if r not in call["params"]]
            if missing:
                log.append(
                    f"[tool {spec['name']}] refused: missing required params: " + ", ".join(missing)
                )
                continue
            args = _summarise_params(call["params"])
            ctx.report_progress(f"{spec['name']} {args}"[:80])
            try:
                result = ctx.call_tool(spec["name"], call["params"])
                output = _result_text(result)
                if result.get("is_error"):
                    output = f"[ERROR] {output}"
            except Exception as exc:
                output = f"[ERROR] error calling tool: {exc}"
            if len(output) > _AGENTIC_MAX_TOOL_OUTPUT_CHARS:
                output = output[:_AGENTIC_MAX_TOOL_OUTPUT_CHARS] + "\n... (truncated)"
            calls_made += 1
            total_chars += len(output)
            tool_log.append({"name": spec["name"], "args": args, "size": len(output)})
            log.append(f"[tool {spec['name']} {args}]\n{output}")
            _publish(
                f"iter {iterations}/{max_iterations} · {spec['name']} · {_fmt_tokens(total_chars)}c"
            )
    else:
        # Natural exhaustion. The output cap can land on the SAME iteration
        # that exhausts the budget; report it in preference, since it is the
        # cap that would still bite if the caller simply raised max_iterations.
        cap_note = (
            "hit tool-output cap"
            if total_chars >= _AGENTIC_MAX_TOTAL_TOOL_CHARS
            else "hit iteration cap"
        )

    if answer is None:
        # A cap ended the loop with no answer in hand: one final TOOLLESS
        # synthesis on the same delegate, so the caller gets the partial
        # findings rather than a bare failure.
        if cap_note is None:
            cap_note = "hit iteration cap"
        ctx.report_progress("delegate synthesising…")
        question = _build_agentic_prompt(goal, tool_specs, log, final=True, cap_note=cap_note)
        text, err, cfg, usage, chain = _agentic_probe(ctx, question, chain)
        spent = _merge_usage(spent, usage)
        if cfg is not None:
            used_cfg = cfg
        if err is not None:
            _publish("ERR")
            return (
                _side_query_error(RuntimeError(err))
                if _is_request_shaped_error(err)
                else _error(err)
            )
        answer = (text or "").strip()

    if not answer:
        _publish("empty")
        return _error("delegate returned no content")

    _publish(cap_note or "done")
    model = f"{_format_advisor_spec(used_cfg)}{_trace_notes(used_cfg)}" if used_cfg else "unknown"
    header = f"[delegate: {model} · {calls_made} tool calls · {iterations} iterations]"
    if cap_note:
        header += f" ({cap_note})"
    return {
        "content": [{"type": "text", "text": _append_usage(f"{header}\n\n{answer}", spent)}],
        "is_error": False,
        "details": {
            "tool_outputs": [
                {
                    "name": e["name"],
                    "title": e["args"],
                    "output": f"{e['size']} chars",
                    "is_error": False,
                }
                for e in tool_log
            ]
        },
    }


# ---------------------------------------------------------------------------
# Core: run an aside — side query with optional tool calls
# ---------------------------------------------------------------------------


def _run_aside(
    tools: list[dict],
    instructions: str,
    ctx: fir_ext.Context,
    escalate: bool = False,
    delegate: bool = False,
    goal: str = "",
    allow_tools: list | None = None,
    max_iterations: Any = None,
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
    goal : str
        Agentic mode. Mutually exclusive with *tools*, and REQUIRES
        *delegate*: the delegate model drives its own read-only tool calls
        until it can answer the goal.  See :func:`_run_agentic_delegate`.
    allow_tools : list, optional
        Narrow agentic mode's read-only tool set. Validated against it.
    max_iterations : int, optional
        Agentic iteration budget, clamped to 1..20 (default 8).

    Returns
    -------
    dict
        Structured tool result with ``content`` and ``is_error``.
    """
    if escalate and delegate:
        return _error("escalate and delegate are mutually exclusive — pick one")

    goal = (goal or "").strip()
    if goal:
        # Agentic mode. The three constraints below are validation errors, not
        # silent coercions: each one means the caller has a different mental
        # model of what this call will do than what it would actually do.
        if tools:
            return _error(
                "goal and tools are mutually exclusive — use 'goal' to let the "
                "delegate choose its own tool calls, or 'tools' to run a chain "
                "you specify upfront"
            )
        if escalate:
            return _error(
                "goal requires delegate=true — agentic mode is delegate-only. "
                "Escalation buys judgement on context already in hand; the pattern "
                "is 'delegate gathers -> escalate judges'"
            )
        if not delegate:
            return _error("goal requires delegate=true — agentic mode is delegate-only")
        if _delegate() is None:
            return _error(
                "goal requires a configured delegate model, but delegation is off "
                "(see /aside-delegate)"
            )
        return _run_agentic_delegate(
            goal,
            ctx,
            chain=_resolve_delegate_chain(ctx),
            allow_tools=allow_tools,
            max_iterations=_clamp_iterations(
                _AGENTIC_DEFAULT_ITERATIONS if max_iterations is None else max_iterations
            ),
        )

    if not instructions:
        return _error("instructions are required")

    # Resolve advisor/delegate override if requested and configured. Layer A:
    # resolution produces an ORDERED candidate chain, each element passed
    # through the availability/memo filter (degrading to a live model of its
    # tier when needed, skipping models cooling off after a recent failure).
    chain: list[dict[str, str]] = []
    role_label: str | None = None
    if escalate and _advisor() is not None:
        chain = _resolve_advisor_chain(ctx)
        role_label = "advisor"
    elif delegate and _delegate() is not None:
        chain = _resolve_delegate_chain(ctx)
        role_label = "delegate"

    # No tools — pure ephemeral side query.
    if not tools:
        synthesis, err, used_cfg, note, usage = _run_side_query_chain(
            ctx, instructions, chain=chain, role_label=role_label
        )
        if err is not None:
            return _side_query_error(RuntimeError(err))
        # Belt-and-suspenders: SideQuery should now return an error on truly
        # empty responses, but if something slips through (e.g. whitespace-
        # only output from a provider we don't handle as carefully), surface
        # it as an explicit error so the caller doesn't see a bare trace line.
        if not synthesis or not synthesis.strip():
            return _error("advisor returned no content")
        # When note is set the answer came from the executor model (chain
        # exhausted) — drop the advisor/delegate trace prefix.
        text = note + synthesis if note else _prefix_for_role(synthesis, used_cfg, role_label)
        return {
            "content": [{"type": "text", "text": _append_usage(text, usage)}],
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
        # Front-loaded: clients truncate the spinner label to ~12 runes.
        label = name + (f" — {title}" if title else "")
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
    synthesis, err, used_cfg, note, usage = _run_side_query_chain(
        ctx, prompt, chain=chain, role_label=role_label
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
                "text": _append_usage(
                    (note + synthesis)
                    if note
                    else _prefix_for_role(synthesis, used_cfg, role_label),
                    usage,
                ),
            }
        ],
        "is_error": False,
        "details": {"tool_outputs": tool_outputs},
    }


def _trace_notes(cfg: dict[str, str]) -> str:
    """Parenthesised routing notes for a trace line, or "" when there are none.

    Two independent degradations can apply to one answer: Layer A/the walk
    substituted a different model (``_fallback``), and/or the answer came from
    the reasoning-off retry (``_reasoning_off``). Both must be visible — the
    caller has to know it is reading a degraded answer — so they compose into
    one note rather than one shadowing the other.
    """
    notes = []
    fallback = cfg.get("_fallback")
    if fallback:
        notes.append(f"fallback: {fallback} unavailable")
    if cfg.get("_reasoning_off"):
        notes.append("reasoning off")
    return f" ({', '.join(notes)})" if notes else ""


def _prefix_advisor(text: str, advisor: dict[str, str] | None) -> str:
    """Prefix the synthesis with a single trace line when escalation was used.

    The trace makes advisor invocations visible to both user and agent —
    the agent sees that the response came from a stronger model, and the
    user sees what was billed.
    """
    if advisor is None:
        return text
    spec = _format_advisor_spec(advisor)
    return f"[advisor: {spec}{_trace_notes(advisor)}]\n\n{text}"


def _prefix_delegate(text: str, delegate: dict[str, str] | None) -> str:
    """Prefix the synthesis with a single trace line when delegation was used.

    Mirror of _prefix_advisor — makes the cheap-model routing visible so
    both user and agent know the response came from the delegate.
    """
    if delegate is None:
        return text
    spec = _format_advisor_spec(delegate)
    return f"[delegate: {spec}{_trace_notes(delegate)}]\n\n{text}"


def _prefix_for_role(
    text: str,
    used_cfg: dict[str, str] | None,
    role_label: str | None,
) -> str:
    """Apply the trace prefix for whichever role actually answered.

    ``used_cfg`` is the resolved candidate that produced the answer (or None
    when the chain was empty / disabled this session, in which case no prefix
    is added — the executor answered plainly).
    """
    if role_label == "advisor":
        return _prefix_advisor(text, used_cfg)
    if role_label == "delegate":
        return _prefix_delegate(text, used_cfg)
    return text


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
    if any(m in msg.lower() for m in _OVERFLOW_MARKERS):
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
            "\n\nAgentic delegation: instead of 'tools' + 'instructions', set 'goal' "
            "(with delegate=true) to let the delegate model drive its OWN read-only "
            "tool calls (read/glob/grep/ls) in a loop until it can answer. Use 'goal' "
            "when the chain of calls is adaptive and not knowable upfront ('find where "
            "X is configured and how it's used'); use 'tools' when you already know "
            "the exact calls. Agentic mode is delegate-only — escalation is for "
            "judgement on context already in hand, so the pattern stays "
            "'delegate gathers -> escalate judges'."
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
                "description": "Instructions for the LLM that synthesises collected outputs, or the side question to ask. Required unless 'goal' is set.",
            },
        },
        "required": ["title"],
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
        schema["properties"]["goal"] = {
            "type": "string",
            "description": (
                "AGENTIC mode: state what you want found out and let the delegate "
                "drive its own read-only tool calls (read/glob/grep/ls) until it can "
                "answer. Requires delegate=true. Mutually exclusive with 'tools' — "
                "use 'goal' when the chain of calls is not knowable upfront, 'tools' "
                "when it is."
            ),
        }
        schema["properties"]["allow_tools"] = {
            "type": "array",
            "items": {"type": "string"},
            "description": (
                "Agentic mode only: narrow the delegate's tool set. Must be a subset "
                "of the read-only set (read, glob, grep, ls); anything else is "
                "rejected."
            ),
        }
        schema["properties"]["max_iterations"] = {
            "type": "integer",
            "description": ("Agentic mode only: max tool-calling rounds. Default 8, clamped 1-20."),
        }
    return schema


@fir_ext.tool(
    name="aside",
    description=_aside_tool_description(),
    parameters=_aside_tool_parameters(),
    # Host-side deadline disabled: this body legitimately runs multi-minute LLM
    # work — an advisor call on a large transcript, and in agentic mode up to
    # 20 of them plus tool calls. The 30s default only survives today by
    # accident (CallHook's deadline is activity-aware and this extension is
    # chatty with cards and progress), which is not something to keep resting
    # on, and it also violated the SDK's documented invariant that a body
    # waiting on ctx.call_tool(timeout=60) must declare at least that much. The
    # call stays bounded: turn cancel / ESC on the host side, and side_query's
    # own 600s per-delta idle timeout on this side.
    timeout=-1,
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
    return _run_aside(
        tools,
        instructions,
        ctx,
        escalate=escalate,
        delegate=delegate,
        goal=params.get("goal", "") or "",
        allow_tools=params.get("allow_tools"),
        max_iterations=params.get("max_iterations"),
    )


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

    advisor_cfg = _advisor()
    if advisor_cfg is None:
        return {
            "message": (
                "No advisor configured. Run `/aside-advisor <provider>/<model>` "
                "to enable, or use `/aside` to ask the current model."
            ),
        }

    chain = _resolve_advisor_chain(ctx)
    answer, err, used_cfg, note, usage = _run_side_query_chain(
        ctx, text, chain=chain, role_label="advisor"
    )
    if err is not None:
        return {"message": _side_query_error_text(RuntimeError(err))}
    if not answer or not answer.strip():
        return {"message": _side_query_error_text(RuntimeError("advisor returned no content"))}
    body = _append_usage(note + answer if note else _prefix_advisor(answer, used_cfg), usage)
    return {
        "message": f"**advise:** {text}\n\n{body}",
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
                f"aside-advisor: {_format_role_config(advisor)}{suffix}\n\n"
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
                f"aside-delegate: {_format_role_config(delegate)}{suffix}\n\n"
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
