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
    response carried no blocks at all. This drives the card SLUG only; the
    routing verdict is _is_empty_content_error's, which treats every one of
    these as the same transient class.
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


def _run_side_query_chain(
    ctx: fir_ext.Context,
    question: str,
    *,
    chain: list[dict[str, str]],
    role_label: str | None,
) -> tuple[str | None, str | None, dict[str, str] | None, str]:
    """Run a side query, walking a candidate chain with executor fallback.

    Walks *chain* (an ordered list of resolved advisor/delegate specs) in
    order. Returns ``(text, error, used_cfg, note)``:

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
    walk_key = f"aside/chain/{int(time.time() * 1000)}"

    def _attempt(
        *, model: str | None, provider: str | None, effort: str | None, label: str
    ) -> tuple[str | None, str | None]:
        """One candidate probe, with a single retry on empty content.

        At most two attempts: the second happens ONLY when the first failed
        with the transient empty-content class. A request-shaped error breaks
        out even when it wears empty-content wording — a cancelled call must
        never earn another LLM call, and ``(blocks: []) (stop_reason=aborted)``
        matches the empty-content pattern.
        """
        text: str | None = None
        err: str | None = None
        for attempt in range(2):
            if attempt:
                # Keep the retry honest and visible — an observer sees the
                # second probe, not a silent stall.
                ctx.put_observable(
                    walk_key,
                    slug="retry:empty",
                    detail=f"{label} returned no usable content — retrying once\n\n{err}",
                )
                if _EMPTY_CONTENT_RETRY_BACKOFF > 0:
                    time.sleep(_EMPTY_CONTENT_RETRY_BACKOFF)
            text, err = _run_side_query_with_card(
                ctx, question, model=model, provider=provider, effort=effort
            )
            if err is None or _is_request_shaped_error(err) or not _is_empty_content_error(err):
                break
        return text, err

    for cfg in chain:
        label = f"{cfg['provider']}/{cfg['model']}"
        text, err = _attempt(
            model=cfg["model"],
            provider=cfg["provider"],
            effort=cfg.get("effort"),
            label=label,
        )
        if err is None:
            # It answered — close its breaker so an intermittent model gets a
            # fresh short backoff next time rather than ratcheting toward the
            # cap over a long session.
            _mark_model_available(cfg["provider"], cfg["model"])
            # A later candidate answered — surface which higher-priority model
            # it stood in for, reusing the "(fallback: … unavailable)" trace
            # style. Layer A may have already set _fallback (degrade); only
            # annotate when it hasn't.
            if failed_models and "_fallback" not in cfg:
                # Copy before annotating — _degrade_role returns the shared
                # _ADVISOR spec dict on the in-available path, so mutating it
                # in place would corrupt the session config.
                cfg = dict(cfg)
                cfg["_fallback"] = failed_models[0]
            return text, None, cfg, ""
        if _is_request_shaped_error(err):
            # Attributable to the request, not the route — surface as-is.
            # Deliberately ahead of the empty-content check so a cancellation
            # can never earn a retry.
            return None, err, None, ""
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
    text, err = _attempt(model=None, provider=None, effort=None, label="executor model")
    if err is None:
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
            return text, None, None, note
        return text, None, None, ""

    # Executor fallback ALSO failed. When we had advisor candidates, chain both
    # error sets so neither is discarded.
    if advisor_errs and role_label:
        joined = "; ".join(advisor_errs)
        how = "chain failed" if stop_err is not None else "chain exhausted"
        combined = f"{role_label} {how}: {joined}; executor fallback also failed: {err}"
        return None, combined, None, ""
    return None, err, None, ""


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
        synthesis, err, used_cfg, note = _run_side_query_chain(
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
    synthesis, err, used_cfg, note = _run_side_query_chain(
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
                "text": (note + synthesis)
                if note
                else _prefix_for_role(synthesis, used_cfg, role_label),
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

    advisor_cfg = _advisor()
    if advisor_cfg is None:
        return {
            "message": (
                "No advisor configured. Run `/aside-advisor <provider>/<model>` "
                "to enable, or use `/aside` to ask the current model."
            ),
        }

    chain = _resolve_advisor_chain(ctx)
    answer, err, used_cfg, note = _run_side_query_chain(
        ctx, text, chain=chain, role_label="advisor"
    )
    if err is not None:
        return {"message": _side_query_error_text(RuntimeError(err))}
    if not answer or not answer.strip():
        return {"message": _side_query_error_text(RuntimeError("advisor returned no content"))}
    body = note + answer if note else _prefix_advisor(answer, used_cfg)
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
