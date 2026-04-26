#!/usr/bin/env python3
# ---
# name: plan-nudger
# description: Surface a calm plan-status steer when the agent pauses or stagnates while a plan is in flight
# builtin: true
# ---
"""
plan-nudger — keeps a long-running work loop legible to both the user
and the agent.

Design notes
------------
The previous version of this extension fired a loud ``[CONTINUE]``
reminder that demanded a tool call and forbade prose-only replies.  In
practice that wording produced two failure modes:

1. The agent *gamed* the literal check by marking incomplete items
   "completed" or splitting them into a "future work" plan.
2. The procedural "MUST" voice triggered either brittle compliance or
   rationalisation-driven evasion.  It addressed the **fact** of stopping,
   not the **reasons** for stopping.

The redesign keeps the firing rules from main (idle-turn threshold,
wall-clock backstop, stagnation tracking) — those proved sound — but
replaces every imperative with calm collaborator-to-collaborator
framing.  The shift in voice is: instead of telling the agent "you
stopped, do the thing", we explain *why* we want frequent plan updates
("so the user can see what you're doing") and surface only the facts
the agent can't compute itself (counters and stagnation count).

The steer body is composed from four optional blocks, each appearing
only when relevant.  Steady-state output is two short lines.
"""

import time

import fir_ext

# ---------------------------------------------------------------------------
# Firing rules (kept from main's plan-nudger — they were the right call)
# ---------------------------------------------------------------------------

# A turn that ran one or more tools is never idle.  Only when this many
# consecutive turns end without a single tool call do we consider the
# agent to have paused — and only then does the steer fire from
# turn_end.
IDLE_TURN_THRESHOLD = 2

# Backstop for extreme cases where the agent sits idle for a long time
# between turns — trips even if IDLE_TURN_THRESHOLD has not been
# reached.
NUDGE_TIME_THRESHOLD = 120  # seconds

# After this many consecutive idle-turn nudges without plan_completed
# advancing, surface a stagnation line in the steer body.
STAGNATION_THRESHOLD = 2

# ---------------------------------------------------------------------------
# Mutable state
# ---------------------------------------------------------------------------

# Plan snapshot from the latest plan_update.
plan_total: int = 0
plan_completed: int = 0
plan_metadata: dict = {}

# Consecutive turns that ended without executing any tool.
idle_turns: int = 0

# Timestamp of the last "active" tick (tool execution or plan update).
last_active_time: float = time.monotonic()

# Did any tool execute during the current turn?
tool_used_this_turn: bool = False

# Consecutive nudges where plan_completed did not advance.  Used to
# detect "I've been pinging the agent and nothing is happening".
nudges_without_progress: int = 0

# Tracks plan-updates that left ``progress_metric`` unchanged.  Reset
# whenever the AI bumps the metric string.
updates_since_metric_change: int = 0
last_metric_value: str = ""


# ---------------------------------------------------------------------------
# Event handlers — bookkeeping
# ---------------------------------------------------------------------------


@fir_ext.on("session_update")
def on_session_update(params, ctx):
    """Snapshot plan state from each ``plan_update`` event.

    A plan update is itself a sign of forward motion: the agent just
    used the plan tool, which is exactly what we want it to be doing.
    Treat it as an active tick so we don't immediately fire a steer on
    the next idle turn.
    """
    global plan_total, plan_completed, plan_metadata
    global idle_turns, last_active_time
    global nudges_without_progress
    global updates_since_metric_change, last_metric_value

    if not params or params.get("type") != "plan_update":
        return

    plan = params.get("plan") or {}
    new_total = int(plan.get("total", 0) or 0)
    new_completed = int(plan.get("completed", 0) or 0)
    new_metadata = plan.get("metadata") or {}
    new_metric = (new_metadata.get("progress_metric") or "").strip()

    # Reset stagnation only when items are actually completed, not just
    # because the plan was touched.  A plan update without a completion
    # is fine — we still tick the active counter — but it doesn't
    # extinguish the stagnation flag.
    if new_completed > plan_completed:
        nudges_without_progress = 0

    # Independent stagnation signal: AI is touching the plan but the
    # progress_metric string isn't moving.
    if new_metric != last_metric_value:
        updates_since_metric_change = 0
        last_metric_value = new_metric
    else:
        updates_since_metric_change += 1

    plan_total = new_total
    plan_completed = new_completed
    plan_metadata = new_metadata

    idle_turns = 0
    last_active_time = time.monotonic()


@fir_ext.on("tool_execution_end")
def on_tool_execution_end(params, ctx):
    """Mark the current turn as active.

    The flag is consumed (and cleared) inside ``turn_end``: a tool-using
    turn never fires a steer, and resets the idle counter.
    """
    global tool_used_this_turn, last_active_time
    tool_used_this_turn = True
    last_active_time = time.monotonic()


# ---------------------------------------------------------------------------
# Steer body composition
# ---------------------------------------------------------------------------


# Always-on tag.  The framing ("keeping plan visible to the user")
# is *why* we send these steers — collaborator-to-collaborator, not
# parent-to-child.  The agent reads the tag before any counters and
# knows the message exists for the user's sake.
_TAG = "[plan-status — keeping plan visible to the user]"

_HANDOFF_LINE = (
    "Note: context isn't a hard wall — `self-handoff` writes a doc and "
    "starts fresh; work continues. Stopping early is not the only escape "
    "from context pressure."
)

_METRIC_TIP = (
    "Tip: set metadata.progress_metric (e.g. \"coverage=95.2%\" or "
    "\"endpoints migrated 3/8\") on your next plan update so the user "
    "sees real progress here too."
)


def _build_status_body(*, surface_handoff: bool) -> str:
    """Compose the steer body from optional blocks.

    Steady-state (no stagnation, metric set, plan in flight) is just
    the tag + the counter line.  Each optional block is added only
    when it has something meaningful to say.

    Parameters
    ----------
    surface_handoff
        When True, append the ``self-handoff`` reassurance line.  Caller
        decides this — typically we only surface it once stagnation is
        real, so it doesn't become wallpaper.
    """
    incomplete = max(plan_total - plan_completed, 0)
    metric = (plan_metadata.get("progress_metric") or "").strip()

    # ── counter line ───────────────────────────────────────────────────
    parts = [f"{incomplete} step(s) incomplete"]
    if idle_turns > 0:
        parts.append(f"{idle_turns} idle turn(s)")
    if metric and updates_since_metric_change > 0:
        parts.append(
            f"{updates_since_metric_change} plan-update(s) since "
            "progress_metric changed",
        )
    if nudges_without_progress >= STAGNATION_THRESHOLD:
        parts.append(
            f"{nudges_without_progress} consecutive pause(s) without "
            "plan progress",
        )
    counter_line = " · ".join(parts)

    lines = [_TAG, counter_line]

    # ── tip: progress_metric is unset ──────────────────────────────────
    # Only show when there's a plan in flight and the metric is
    # genuinely missing.  Once set, the counter line carries the
    # information instead.
    if not metric and plan_total > 0:
        lines.append(_METRIC_TIP)

    # ── handoff reassurance ────────────────────────────────────────────
    if surface_handoff:
        lines.append(_HANDOFF_LINE)

    return "\n".join(lines)


def _send_steer(ctx) -> None:
    """Fire a steer with the current state.  Idempotent; caller does
    the rate-limiting via ``idle_turns``/``last_active_time`` checks."""
    body = _build_status_body(
        surface_handoff=nudges_without_progress >= STAGNATION_THRESHOLD,
    )
    ctx.send_message(
        "plan_status",
        body,
        display=True,
        deliver_as="steer",
    )


# ---------------------------------------------------------------------------
# Firing handlers
# ---------------------------------------------------------------------------


@fir_ext.on("turn_end")
def on_turn_end(params, ctx):
    """Fire the calm steer when the agent paused for too long.

    Identical gating to main's plan-nudger:
      * a turn that called any tool is a normal loop tick — never fires;
      * fires only after IDLE_TURN_THRESHOLD consecutive idle turns or
        NUDGE_TIME_THRESHOLD seconds of wall-clock idleness;
      * plan must be in flight (total > completed).

    What's different is the body: no imperatives, no escalation tiers,
    no [SYS_EXT] prepend.  The agent reads the same calm shape every
    time and reasons.
    """
    global idle_turns, tool_used_this_turn, last_active_time
    global nudges_without_progress

    # A tool-using turn is a healthy loop tick — never fires.
    if tool_used_this_turn:
        tool_used_this_turn = False
        idle_turns = 0
        last_active_time = time.monotonic()
        return

    # No tools ran this turn — the agent paused to reply with prose.
    idle_turns += 1

    # Nothing to say if the plan is absent or already complete.
    if plan_total <= plan_completed:
        return

    elapsed = time.monotonic() - last_active_time
    if idle_turns < IDLE_TURN_THRESHOLD and elapsed < NUDGE_TIME_THRESHOLD:
        return

    # Bump stagnation BEFORE composing so the counter line reflects
    # this firing.  Reset the wall-clock so we don't immediately
    # re-fire on the time backstop.
    nudges_without_progress += 1
    last_active_time = time.monotonic()
    _send_steer(ctx)
    # Reset idle so the next steer needs a fresh idle window.
    idle_turns = 0


@fir_ext.on("agent_end")
def on_agent_end(params, ctx):
    """Fire when the agent loop exits while a plan is in flight.

    ``agent_end`` is qualitatively different from ``turn_end``: the
    agent has actively decided "I'm done for now" and the loop is
    unwinding.  We use the same steer body so the message shape stays
    consistent — the only difference is that we always fire here when
    a plan is incomplete (no idle threshold needed; the agent already
    stopped).
    """
    global nudges_without_progress, last_active_time
    if plan_total <= plan_completed:
        return
    nudges_without_progress += 1
    last_active_time = time.monotonic()
    _send_steer(ctx)


fir_ext.run(name="plan-nudger")
