#!/usr/bin/env python3
# ---
# name: handoff-nudger
# description: Nudge the agent to self_handoff once context grows expensive, instead of waiting for lossy auto-compaction at 70% of the window
# builtin: true
# ---
"""handoff-nudger — cost-driven handoff pressure.

Auto-compaction only fires at 70% of the context window. On a 1M-window
model that is ~700k tokens, so a long session re-reads a huge prefix on
every turn: prompt caching keeps fresh input near zero, but *cache-read*
cost grows linearly with conversation length and nothing stops it. One
measured 198-turn session accumulated 45.7M cache-read tokens and never
compacted once. Compaction is also lossy and in-band; ``self_handoff``
(curated briefing + bookmarks) is the better instrument, and the only
thing missing was something to tell the agent when to reach for it.

Two triggers, because there are two distinct reasons to hand off:

* **Ceiling** — the session is simply large. Attention dilution and
  cache-read cost both scale with tokens, so past ``atTokens`` (or
  ``atPercent`` of the window, whichever is lower) it is time to go
  regardless of anything else.

* **Cold cache** — fir requests Anthropic's 1h extended cache retention.
  While turns keep coming the prefix is cache-warm and a re-read costs
  ~0.1x input, which is noise: nudging there is nagging. Once the
  session has been idle longer than ``idleMinutes`` the cache has
  provably expired, every subsequent turn pays full input on the whole
  prefix plus a fresh cache write, and that is the natural moment to
  cut over — at a much lower token bar than the ceiling.

After every turn this reads context usage via ``agent.info`` and, once a
trigger fires, prepends a ``[SYS_EXT]`` note to the live agent state. The
note is not delivered as a turn of its own — the agent simply sees it at
the top of its next turn and is expected to finish the current request
and hand off.

Config — ``handoff-nudger.json`` in any host config dir (project-local
``.fir/`` wins over ``~/.config/fir/``)::

    {"atTokens": 500000, "atPercent": 65, "idleMinutes": 65,
     "nudgeEvery": 100000, "off": false}

The cold-cache trigger has no threshold of its own: it fires at
``IDLE_TOKEN_FRACTION`` of whichever ceiling is in effect (floored at
``IDLE_FLOOR_TOKENS``, and never above the ceiling), so tuning
``atTokens``/``atPercent`` moves both triggers together and there is one
fewer knob to get wrong. Only the *first* turn of a cold streak skips the
``nudgeEvery`` throttle — a slow-cadence session is cold on every turn and
must be nudged once, not continuously.
"""

from __future__ import annotations

import contextlib
import time

import fir_ext

CONFIG_FILENAME = "handoff-nudger.json"

# Ceiling: absolute token threshold, and percent-of-window threshold. The
# lower of the two fires. 500k is where a session is large enough that
# context rot alone justifies a handoff; 65% keeps small-window models
# nudged before compaction's 70%.
DEFAULT_AT_TOKENS = 500_000
DEFAULT_AT_PERCENT = 65

# Idle gap after which the provider's 1h extended prompt cache is assumed
# dead. Slightly over the hour to avoid firing on a cache that is merely
# about to expire.
DEFAULT_IDLE_MINUTES = 65

# How many further tokens must accumulate before re-nudging an agent that
# keeps going.
DEFAULT_NUDGE_EVERY = 100_000

# The cold-cache trigger fires at this fraction of the ceiling, but never
# below IDLE_FLOOR_TOKENS: under that, a cold re-read is cheap enough that
# handing off costs more (in lost context) than it saves, and the fraction
# alone would nudge a small-window session absurdly early. It never exceeds
# the ceiling either, so on a very small window the ceiling is the only
# trigger.
IDLE_TOKEN_FRACTION = 0.3
IDLE_FLOOR_TOKENS = 100_000

# Context size at the last nudge. 0 = never nudged.
_last_nudge_at = 0

# Whether the current cold streak has already been nudged. A user whose
# cadence is one message every couple of hours sees a cold cache on every
# single turn; without this they would be nudged on every turn, which is
# the nagging this extension exists to stop. Re-armed by any warm turn.
_cold_nudged = False

# Wall-clock of the last turn_end, and the idle gap observed before the
# turn now in flight. Every turn — including a follow-up turn — emits its
# own turn_start/turn_end pair, so the value set at start is the right one
# to consume at that turn's end. Deliberately not persisted: a fir restart
# means the whole session was reloaded and the cache is cold anyway, and
# _idle_seconds=None is treated as cold. An extension-only restart (e.g.
# /reload) is the one case where that guess can cost a spurious nudge.
_last_turn_end = None
_idle_seconds = None


_NOTE_HEAD = {
    "ceiling": """CONTEXT PRESSURE: this session is at {tokens:,} tokens{window}. Every \
further turn re-reads that whole prefix, so cost per turn is now flat-expensive \
and rising, and recall across that much context is degrading. Auto-compaction \
will not save you — it only fires at 70% of the window, and it is lossy.""",
    "cold": """CONTEXT PRESSURE: this session is at {tokens:,} tokens{window} and has \
been idle for {idle}, so the prompt cache has expired. Every turn from here \
re-reads the entire prefix at full input price until the cache is rebuilt — this \
is the cheapest moment to cut over to a fresh session.""",
}

_NOTE_TAIL = """

ACTION: finish the immediate request, then call `self_handoff` with a curated \
briefing: project/branch, what is done, what is in flight with file:line anchors, \
decisions worth keeping, running services, concrete next steps. Bookmark anything \
the next agent must not lose before you go. Do not start new open-ended work here."""


def _config() -> dict:
    return fir_ext.load_config(CONFIG_FILENAME) or {}


def _int(cfg: dict, key: str, default: int) -> int:
    try:
        return int(cfg[key])
    except (KeyError, TypeError, ValueError):
        return default


def _threshold(cfg: dict, window: int) -> int:
    """Ceiling token count: the lower of the absolute and percent-of-window
    thresholds. Percent is ignored when the window is unknown."""
    at_tokens = _int(cfg, "atTokens", DEFAULT_AT_TOKENS)
    if window <= 0:
        return at_tokens
    at_percent = window * _int(cfg, "atPercent", DEFAULT_AT_PERCENT) // 100
    return min(at_tokens, at_percent) if at_percent > 0 else at_tokens


def _idle_threshold(cfg: dict, window: int) -> int:
    """Token count at which a cold cache is worth nudging about."""
    ceiling = _threshold(cfg, window)
    return min(ceiling, max(int(ceiling * IDLE_TOKEN_FRACTION), IDLE_FLOOR_TOKENS))


def _cache_is_cold(cfg: dict) -> bool:
    """True when the gap before the current turn outlived the prompt cache.
    An unknown gap (first turn after a process start) counts as cold."""
    if _idle_seconds is None:
        return True
    return _idle_seconds >= _int(cfg, "idleMinutes", DEFAULT_IDLE_MINUTES) * 60


def _trigger(cfg: dict, tokens: int, window: int, cold: bool) -> str | None:
    if tokens >= _threshold(cfg, window):
        return "ceiling"
    if cold and tokens >= _idle_threshold(cfg, window):
        return "cold"
    return None


def _humanise(seconds: float | None) -> str:
    if seconds is None:
        return "a while"
    hours, minutes = divmod(int(seconds) // 60, 60)
    return f"{hours}h{minutes:02d}m" if hours else f"{minutes}m"


def _nudge(ctx) -> None:
    global _last_nudge_at, _cold_nudged

    cfg = _config()
    if cfg.get("off"):
        return

    usage = (ctx.agent_info() or {}).get("context") or {}
    tokens = int(usage.get("tokens") or 0)
    window = int(usage.get("window") or 0)

    cold = _cache_is_cold(cfg)
    if not cold:
        _cold_nudged = False

    trigger = _trigger(cfg, tokens, window, cold)
    if trigger is None:
        return

    # A shrinking context (compaction, session rewind) must not strand the
    # interval check above a high-water mark we can never beat again.
    if tokens < _last_nudge_at:
        _last_nudge_at = 0
    # The token interval throttles an agent that just keeps going. The
    # first turn of a cold streak is exempt: that turn pays full input on
    # the whole prefix and is the cheap moment to cut over, so suppressing
    # it because we nudged 20k tokens ago would waste it. Only the first —
    # a slow-cadence session is cold on every turn and must not be nagged
    # on every turn.
    exempt = cold and not _cold_nudged
    if (
        not exempt
        and _last_nudge_at
        and tokens - _last_nudge_at < _int(cfg, "nudgeEvery", DEFAULT_NUDGE_EVERY)
    ):
        return
    _last_nudge_at = tokens
    if cold:
        _cold_nudged = True

    ctx.prepend(
        (_NOTE_HEAD[trigger] + _NOTE_TAIL).format(
            tokens=tokens,
            idle=_humanise(_idle_seconds),
            window=f" ({100.0 * tokens / window:.0f}% of a {window:,} window)"
            if window > 0
            else "",
        )
    )
    ctx.notify(f"context {tokens:,} tokens — nudged for handoff ({trigger})", "warning")


@fir_ext.on("turn_start")
def on_turn_start(params, ctx):
    """Record how long the session sat idle before this turn. Measured
    turn_end -> turn_start so a single long-running turn — which keeps the
    cache warm with its own API calls — never reads as idle."""
    global _idle_seconds
    _idle_seconds = None if _last_turn_end is None else time.monotonic() - _last_turn_end


@fir_ext.on("turn_end")
def on_turn_end(params, ctx):
    """Check context usage once the turn has settled. All failures are
    swallowed — the host session never breaks because of us."""
    global _last_turn_end
    with contextlib.suppress(Exception):
        _nudge(ctx)
    _last_turn_end = time.monotonic()


fir_ext.run(name="handoff-nudger")
