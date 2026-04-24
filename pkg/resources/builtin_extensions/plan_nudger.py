#!/usr/bin/env python3
# ---
# name: plan-nudger
# description: Remind the agent to keep working when it pauses with incomplete plan steps
# builtin: true
# events: session_update, turn_end, agent_end, tool_execution_end
# ---
"""
plan-nudger — keeps the agent moving through a long-running work loop.

A nudge fired at the wrong moment is worse than no nudge: an agent that
is already making productive progress (tests passing, files changing,
tools succeeding) reads an alarming reminder as a false positive and
spends its next turn arguing with it instead of working. The copy and
firing rules below are all designed around that failure mode — neutral
wording, tool-call-demanding phrasing, and triggering only on genuinely
idle turns so healthy loop ticks are left alone.
"""

import time

import fir_ext

# A turn that ran one or more tools is never idle. Only when this many
# consecutive turns end without a single tool call do we consider the
# agent to have paused — and only then does the nudge fire.
IDLE_TURN_THRESHOLD = 2

# Backstop for extreme cases where the agent sits idle for a long time
# between turns — trips even if IDLE_TURN_THRESHOLD has not been reached.
NUDGE_TIME_THRESHOLD = 120  # seconds

# After this many consecutive idle-turn nudges without plan_completed
# advancing, escalate to a stronger steer.
STAGNATION_WARN_THRESHOLD = 2
# After this many, switch to a [SYS_EXT] prepend with system-prompt-level
# authority.
STAGNATION_CRIT_THRESHOLD = 3

# -----------------------------------------------------------------------------
# Mutable state
# -----------------------------------------------------------------------------

# Consecutive turns that ended without executing any tool.
idle_turns = 0
# Timestamp of the last "active" tick (session update or tool execution).
last_active_time = time.monotonic()

plan_total = 0
plan_completed = 0

# Did any tool execute during the current turn?
tool_used_this_turn = False
# Last tool the agent invoked (any turn) and whether it errored.
last_tool_name = ""
last_tool_is_error = False

# Consecutive idle-turn nudges where plan_completed did not advance.
nudges_without_progress = 0


# -----------------------------------------------------------------------------
# Event handlers
# -----------------------------------------------------------------------------


@fir_ext.on("session_update")
def on_session_update(params, ctx):
    global plan_total, plan_completed, nudges_without_progress
    global idle_turns, last_active_time
    plan = params.get("plan")
    if plan is None:
        return
    new_total = plan.get("total", 0)
    new_completed = plan.get("completed", 0)
    # Only reset the stagnation counter when tasks are actually completed,
    # not merely because the plan was touched.
    if new_completed > plan_completed:
        nudges_without_progress = 0
    plan_total = new_total
    plan_completed = new_completed
    # A plan update is itself a sign of forward motion — count it as an
    # active tick so we don't fire right after one.
    idle_turns = 0
    last_active_time = time.monotonic()


@fir_ext.on("tool_execution_end")
def on_tool_execution_end(params, ctx):
    global tool_used_this_turn, last_tool_name, last_tool_is_error
    global last_active_time
    tool_used_this_turn = True
    last_tool_name = params.get("tool_name") or last_tool_name
    last_tool_is_error = bool(params.get("is_error", False))
    last_active_time = time.monotonic()


# -----------------------------------------------------------------------------
# Nudge copy
# -----------------------------------------------------------------------------
#
# Every variant:
#   * starts with [CONTINUE] — neutral, no "stuck" language
#   * includes the last tool + status so the next action is obvious
#   * explicitly forbids a prose-only reply (must call a tool)
#   * never suggests rewriting anything from scratch
#
# ``{tool_hint}`` is a pre-formatted phrase like
# ``Last tool was Bash (ok).`` or ``No tool has run yet this session.``


NUDGE_MILD = (
    "[CONTINUE] {tool_hint} Your plan still has incomplete steps — "
    "run the next tool now. Your next reply MUST contain a tool call; "
    "do not reply with prose only. If this is a natural pause point, "
    "update the plan via the plan tool to reflect current status and "
    "continue on the next step."
)

NUDGE_WARN = (
    "[CONTINUE] {tool_hint} You have paused {n} times without completing "
    "a plan item. Pick the smallest next action and execute it with a "
    "tool call — do not analyze, do not narrate. Your next reply MUST "
    "contain a tool call."
)

NUDGE_CRIT = (
    "[CONTINUE] {tool_hint} {n} consecutive idle turns with no plan "
    "progress. Treat this as a loop tick, not a conversation turn.\n\n"
    "Rules for your next reply:\n"
    "  - It MUST contain a tool call. Prose-only replies are not allowed.\n"
    "  - Do not re-read files you have already read.\n"
    "  - Do not re-run the same command to confirm a previous result.\n"
    "  - Do not explain what you are about to do — just do it.\n"
    "If you believe the work is complete, call the plan tool to mark all "
    "remaining steps completed or cancelled."
)

AGENT_END_NUDGE = (
    "[CONTINUE] Your plan still has incomplete steps. End-of-turn is a "
    "pause point in the loop, not a stopping point. If more work remains, "
    "your next reply MUST contain a tool call. If the work really is done, "
    "update the plan via the plan tool so the remaining steps are marked "
    "completed or cancelled, and confirm the outcome to the user."
)


def _tool_hint() -> str:
    """Return a short sentence describing the last tool the agent ran."""
    if not last_tool_name:
        return "No tool has run yet this session."
    status = "error" if last_tool_is_error else "ok"
    return f"Last tool was {last_tool_name} ({status})."


# -----------------------------------------------------------------------------
# Turn / agent-end handlers
# -----------------------------------------------------------------------------


@fir_ext.on("turn_end")
def on_turn_end(params, ctx):
    global idle_turns, tool_used_this_turn, last_active_time
    global nudges_without_progress

    # A turn that ran any tool is a normal loop tick — never nudge.
    if tool_used_this_turn:
        tool_used_this_turn = False
        idle_turns = 0
        last_active_time = time.monotonic()
        return

    # No tools ran this turn — the agent paused to reply with prose.
    idle_turns += 1

    # Nothing to nudge about if the plan is already complete (or absent).
    if plan_total <= plan_completed:
        return

    elapsed = time.monotonic() - last_active_time
    if idle_turns < IDLE_TURN_THRESHOLD and elapsed < NUDGE_TIME_THRESHOLD:
        return

    # Fire a nudge. Reset the idle counter so we don't fire every turn.
    idle_turns = 0
    last_active_time = time.monotonic()
    nudges_without_progress += 1

    hint = _tool_hint()
    if nudges_without_progress >= STAGNATION_CRIT_THRESHOLD:
        # Inject with system-prompt authority so it cannot be rationalized
        # away. Demand a tool call on the next reply.
        ctx.prepend(NUDGE_CRIT.format(tool_hint=hint, n=nudges_without_progress))
    elif nudges_without_progress >= STAGNATION_WARN_THRESHOLD:
        ctx.send_message(
            "nudge",
            NUDGE_WARN.format(tool_hint=hint, n=nudges_without_progress),
            display=True,
            deliver_as="steer",
        )
    else:
        ctx.send_message(
            "nudge",
            NUDGE_MILD.format(tool_hint=hint),
            display=True,
            deliver_as="steer",
        )


@fir_ext.on("agent_end")
def on_agent_end(params, ctx):
    if plan_total > plan_completed:
        ctx.send_message(
            "nudge",
            AGENT_END_NUDGE,
            display=True,
            deliver_as="steer",
        )


fir_ext.run(name="plan-nudger")
