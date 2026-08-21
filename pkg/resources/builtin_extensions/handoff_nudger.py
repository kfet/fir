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

After every turn this reads context usage via ``agent.info`` and, once
usage crosses the threshold, prepends a ``[SYS_EXT]`` note to the live
agent state. The note is not delivered as a turn of its own — the agent
simply sees it at the top of its next turn and is expected to finish the
current request and hand off.

Config — ``handoff-nudger.json`` in any host config dir (project-local
``.fir/`` wins over ``~/.config/fir/``)::

    {"atTokens": 150000, "atPercent": 60, "nudgeEvery": 40000, "off": false}

``atTokens`` and ``atPercent`` are both thresholds; whichever is lower
wins. ``nudgeEvery`` is how many further tokens must accumulate before
re-nudging an agent that keeps going.
"""

from __future__ import annotations

import contextlib

import fir_ext

CONFIG_FILENAME = "handoff-nudger.json"

# Absolute token threshold, and percent-of-window threshold. The lower of
# the two fires. 150k is roughly where a turn's cache-read cost stops
# being noise; 60% keeps small-window models nudged before compaction.
DEFAULT_AT_TOKENS = 150_000
DEFAULT_AT_PERCENT = 60
DEFAULT_NUDGE_EVERY = 40_000

# Context size at the last nudge. 0 = never nudged.
_last_nudge_at = 0


NOTE = """CONTEXT PRESSURE: this session is at {tokens:,} tokens{window}. Every \
further turn re-reads that whole prefix from cache, so cost per turn is now \
flat-expensive and rising. Auto-compaction will not save you — it only fires at \
70% of the window, and it is lossy.

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
    """Token count at which to nudge: the lower of the absolute and
    percent-of-window thresholds. Percent is ignored when the window is
    unknown."""
    at_tokens = _int(cfg, "atTokens", DEFAULT_AT_TOKENS)
    if window <= 0:
        return at_tokens
    at_percent = window * _int(cfg, "atPercent", DEFAULT_AT_PERCENT) // 100
    return min(at_tokens, at_percent) if at_percent > 0 else at_tokens


def _nudge(ctx) -> None:
    global _last_nudge_at

    cfg = _config()
    if cfg.get("off"):
        return

    usage = (ctx.agent_info() or {}).get("context") or {}
    tokens = int(usage.get("tokens") or 0)
    window = int(usage.get("window") or 0)
    if tokens < _threshold(cfg, window):
        return

    # A shrinking context (compaction, session rewind) must not strand the
    # interval check above a high-water mark we can never beat again.
    if tokens < _last_nudge_at:
        _last_nudge_at = 0
    if _last_nudge_at and tokens - _last_nudge_at < _int(cfg, "nudgeEvery", DEFAULT_NUDGE_EVERY):
        return
    _last_nudge_at = tokens

    ctx.prepend(
        NOTE.format(
            tokens=tokens,
            window=f" ({100.0 * tokens / window:.0f}% of a {window:,} window)"
            if window > 0
            else "",
        )
    )
    ctx.notify(f"context {tokens:,} tokens — nudged for handoff", "warning")


@fir_ext.on("turn_end")
def on_turn_end(params, ctx):
    """Check context usage once the turn has settled. All failures are
    swallowed — the host session never breaks because of us."""
    with contextlib.suppress(Exception):
        _nudge(ctx)


fir_ext.run(name="handoff-nudger")
