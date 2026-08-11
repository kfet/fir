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

Advisor / delegate as a fallback CHAIN
--------------------------------------

The advisor (and delegate) is an ORDERED FALLBACK CHAIN, not a single model.
The bundled default advisor chain is
``anthropic/claude-fable-5 -> claude-opus-4-8 -> claude-opus-4-7`` (Fable is
kept first: it looks live in ``/v1/models`` but 404s at runtime, so we try it
and let the 404 advance the chain).  ``aside.json`` accepts a JSON array to
express a custom chain, alongside the back-compat single string:

    {"advisor": ["anthropic/claude-fable-5:high", "anthropic/claude-opus-4-8"]}
    {"advisor": "anthropic/claude-opus-4-8:high"}     # single (back-compat)
    {"advisor": "off"}                                # disabled

Resolution walks the chain in order.  Each candidate passes through the
availability/memo filter (skip models memoized unavailable this session,
degrade a pruned model to a live sibling of its tier).  A model-unavailability
error memoizes that model and advances to the next candidate; when the whole
chain is exhausted the query terminates on the executor / current-session
model (never a hard failure).  ``/aside-advisor`` and ``/aside-delegate`` set a
single pinned model; edit ``aside.json`` directly to configure a chain array.

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
# /v1/models — it looks identical to a live model, and only a runtime 404
# reveals it is dead. So we try it and let the 404 advance the chain to the
# next candidate. The tail Opus versions are concrete fallbacks the chain walk
# advances to when Fable (and higher Opuses) 404 at runtime.
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


# Lazily-loaded advisor/delegate configs — populated on first access (after
# the init handshake has set fir_ext.config_dirs). Tests that want to inject
# a value can assign to _ADVISOR / _DELEGATE directly.
#
# Only a value that came from the bundled static default is eligible for
# runtime dynamic resolution against ctx.available_models() — an explicit
# aside.json value always wins. Identity, not a boolean flag, records that:
# ``_*_DEFAULT_OBJ`` holds the exact object the loader produced when it fell
# back to the bundled default; a directly-injected _ADVISOR / _DELEGATE
# (tests, callers) is a different object and is therefore treated as
# explicit and used verbatim.
_ADVISOR_UNSET = object()
_ADVISOR: Any = _ADVISOR_UNSET
_DELEGATE: Any = _ADVISOR_UNSET
_ADVISOR_DEFAULT_OBJ: Any = _ADVISOR_UNSET
_DELEGATE_DEFAULT_OBJ: Any = _ADVISOR_UNSET


def _advisor() -> dict[str, str] | list[dict[str, str]] | None:
    global _ADVISOR, _ADVISOR_DEFAULT_OBJ
    if _ADVISOR is _ADVISOR_UNSET:
        _ADVISOR, from_default = _load_role_config_source("advisor", _DEFAULT_ADVISOR_CHAIN)
        _ADVISOR_DEFAULT_OBJ = _ADVISOR if from_default else _ADVISOR_UNSET
    return _ADVISOR


def _delegate() -> dict[str, str] | list[dict[str, str]] | None:
    global _DELEGATE, _DELEGATE_DEFAULT_OBJ
    if _DELEGATE is _ADVISOR_UNSET:
        _DELEGATE, from_default = _load_role_config_source("delegate", _DEFAULT_DELEGATE_SPEC)
        _DELEGATE_DEFAULT_OBJ = _DELEGATE if from_default else _ADVISOR_UNSET
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
        m.get("provider") == cfg["provider"] and m.get("id") == cfg["model"] for m in available
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
    filter (:func:`_degrade_role`): models already memoized unavailable this
    session are skipped, and a spec whose own model has gone away degrades to
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
        # sibling, so it would return a memoized-dead model unchanged and we'd
        # re-probe it every call. Skip it here. (When availability IS known,
        # _degrade_role handles the memo itself by degrading to a live model
        # of the same tier — e.g. memoized fable -> opus — so we must NOT skip
        # in that case or we'd lose that substitution.)
        if not available and _model_key(spec["provider"], spec["model"]) in _SESSION_UNAVAILABLE:
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
    cfg = _advisor()
    return _resolve_role_chain_dynamic(ctx, cfg, "advisor", cfg is _ADVISOR_DEFAULT_OBJ)


def _resolve_delegate_chain(ctx: fir_ext.Context) -> list[dict[str, str]]:
    """Availability-aware delegate chain resolution (Layer A)."""
    cfg = _delegate()
    return _resolve_role_chain_dynamic(ctx, cfg, "delegate", cfg is _DELEGATE_DEFAULT_OBJ)


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


# Empty-content markers. The host raises this class when the provider returns
# a SUCCESSFUL response (stop_reason=stop) carrying no usable text — typically
# a lone redacted thinking block, e.g.
#   side-query: response had no usable content (blocks: [thinking(th=0,sig=940)])
# or the no-blocks variant "(blocks: [])". Telemetry (2026-08-11) puts this at
# ~57% of aside calls on one host and ~27% on another, with no config
# correlation and no burst pattern — it is a TRANSIENT upstream blip: the same
# question re-probed hours later succeeds immediately.
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
    model-unavailability 404 it is transient, so the model is never memoized
    as dead; unlike overflow/connection errors it IS retried.
    """
    if not msg:
        return False
    if _EMPTY_BLOCKS_RE.search(msg) is not None:
        return True
    low = msg.lower()
    return any(m in low for m in _EMPTY_CONTENT_MARKERS)


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
      * A candidate fails with a model-unavailability error → the model is
        memoized and the walk ADVANCES to the next candidate.
      * A candidate returns EMPTY CONTENT → retried ONCE on the same candidate
        after ``_EMPTY_CONTENT_RETRY_BACKOFF``; still empty → the walk ADVANCES
        to the next candidate WITHOUT memoizing the model (the blip is
        transient, the model is not dead — memoizing would degrade the chain
        for the whole session over a hiccup).
      * A candidate fails with any OTHER error (overflow, connection) → that
        error is surfaced immediately (no silent advance).
      * The chain is exhausted after trying ≥1 candidate → retry on the
        executor's own model (``model=None``); on success return
        ``(text, None, None, "[<role> unavailable — answered on executor
        model]\\n\\n")``. Escalation NEVER disables itself on a dead chain.
      * The chain is empty (nothing resolvable, or ``role_label`` is None) →
        a plain executor call with no note.
      * Both the chain AND the executor fallback fail → the errors are chained
        into one message so neither is lost.
    """
    advisor_errs: list[str] = []
    dead_models: list[str] = []
    empty_only = True
    walk_key = f"aside/chain/{int(time.time() * 1000)}"

    def _attempt(
        *, model: str | None, provider: str | None, effort: str | None, label: str
    ) -> tuple[str | None, str | None]:
        """One candidate probe, with a single retry on empty content.

        At most two attempts: the second happens ONLY when the first failed
        with the transient empty-content class.
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
            if err is None or not _is_empty_content_error(err):
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
            # A later candidate answered — surface which higher-priority model
            # it stood in for, reusing the "(fallback: … unavailable)" trace
            # style. Layer A may have already set _fallback (degrade); only
            # annotate when it hasn't.
            if dead_models and "_fallback" not in cfg:
                # Copy before annotating — _degrade_role returns the shared
                # _ADVISOR spec dict on the in-available path, so mutating it
                # in place would corrupt the session config.
                cfg = dict(cfg)
                cfg["_fallback"] = dead_models[0]
            return text, None, cfg, ""
        if _is_empty_content_error(err):
            # Transient — retried once already. Advance, but do NOT memoize:
            # the model is alive, the response was just empty.
            ctx.put_observable(
                walk_key,
                slug="advance:empty",
                detail=f"{label} empty twice — advancing past it (not memoized)\n\n{err}",
            )
            advisor_errs.append(f"{label}: {err}")
            dead_models.append(cfg["model"])
            continue
        empty_only = False
        if _is_model_unavailable_error(err):
            # Memoize so subsequent escalations skip this dead model and
            # Layer A degrades past it instead of re-probing.
            _mark_model_unavailable(cfg["provider"], cfg["model"])
            advisor_errs.append(f"{label}: {err}")
            dead_models.append(cfg["model"])
            continue
        # Non-unavailability, non-empty error (overflow, connection) — do not
        # swallow it by advancing; surface it as-is.
        return None, err, None, ""

    # Chain exhausted or empty → executor terminal fallback (same empty-content
    # retry applies: the executor model is the last hope, one blip must not
    # sink the whole call).
    text, err = _attempt(model=None, provider=None, effort=None, label="executor model")
    if err is None:
        # A non-empty chain that we walked to exhaustion earns the note; an
        # empty chain (no advisor, or nothing resolvable) answers silently.
        if advisor_errs and role_label:
            reason = "returned no usable content" if empty_only else "unavailable"
            note = f"[{role_label} {reason} — answered on executor model]\n\n"
            return text, None, None, note
        return text, None, None, ""

    # Executor fallback ALSO failed. When we had advisor candidates, chain both
    # error sets so neither is discarded.
    if advisor_errs and role_label:
        joined = "; ".join(advisor_errs)
        combined = f"{role_label} chain exhausted: {joined}; executor fallback also failed: {err}"
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
    # tier when needed, skipping models already memoized unavailable).
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
