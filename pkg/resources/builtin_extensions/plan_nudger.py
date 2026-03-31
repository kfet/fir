#!/usr/bin/env python3
# ---
# name: plan-nudger
# description: Remind the agent to update its plan periodically
# builtin: true
# events: session_update, turn_end, agent_end
# ---
import time

import fir_ext

DEFAULT_TURN_THRESHOLD = 5
NUDGE_TIME_THRESHOLD = 60  # seconds

# After this many nudges without plan_completed advancing, escalate to a
# strong warning steer.
STAGNATION_WARN_THRESHOLD = 2
# After this many nudges without progress, switch to a [SYS_EXT] prepend
# with system-prompt-level authority.
STAGNATION_CRIT_THRESHOLD = 3

turns_since_update = 0
last_update_time = time.monotonic()
plan_total = 0
plan_completed = 0
turn_threshold = DEFAULT_TURN_THRESHOLD
# Counts consecutive nudge firings where plan_completed did not increase.
# Reset only when actual progress is made (completed count goes up).
nudges_without_progress = 0


@fir_ext.on("session_update")
def on_session_update(params, ctx):
    global turns_since_update, last_update_time, plan_total, plan_completed
    global turn_threshold, nudges_without_progress
    plan = params.get("plan")
    if plan is None:
        return
    new_total = plan.get("total", 0)
    new_completed = plan.get("completed", 0)
    # Only reset the stagnation counter when tasks are actually completed,
    # not merely because the plan was updated.
    if new_completed > plan_completed:
        nudges_without_progress = 0
    plan_total = new_total
    plan_completed = new_completed
    turns_since_update = 0
    last_update_time = time.monotonic()
    # The LLM can set metadata key "next_update_in" to hint how many turns
    # until the next expected plan update. Ignore the hint when stagnating so
    # the agent cannot delay its own intervention.
    if nudges_without_progress == 0:
        metadata = plan.get("metadata") or {}
        hint = metadata.get("next_update_in", "")
        try:
            hint_val = int(hint)
        except (ValueError, TypeError):
            hint_val = 0
        turn_threshold = hint_val if hint_val > 0 else DEFAULT_TURN_THRESHOLD
    else:
        turn_threshold = DEFAULT_TURN_THRESHOLD


# The nudge message must be phrased as a directive that the model can act on
# purely via a tool call.  Earlier versions used conversational language like
# "Reminder: update your plan" which caused the model to reply with plain
# text ("yeah, plan is already updated") instead of actually calling the plan
# tool.  The messages below are deliberately terse, imperative, and end with
# an explicit instruction to call the plan tool — nothing else.

NUDGE_MILD = (
    "Your plan has not been updated recently. "
    "Call the plan tool now to mark completed steps and set in_progress on your current step. "
    "Do not reply with text — respond only with a plan tool call."
)

NUDGE_WARN = (
    "You have been reminded multiple times without completing any plan tasks. "
    "You may be stuck in a loop. Stop re-analyzing. "
    "Commit to a change now — even if it means rewriting a file completely rather than patching it.\n\n"
    "Your plan has incomplete steps. Continue working until all steps are completed or cancelled."
)

NUDGE_CRIT = (
    "STUCK LOOP DETECTED — you have been reminded {n} times "
    "without completing a single plan item.\n\n"
    "You are in an analysis-paralysis loop. Take a completely different approach:\n"
    "- STOP re-reading files you have already read.\n"
    "- STOP running the same commands.\n"
    "- Pick the most problematic file and REWRITE IT COMPLETELY FROM SCRATCH.\n"
    "- If tests are failing due to API changes, the test file itself needs updating.\n"
    "- Do not analyze further. Make the change now."
)

AGENT_END_NUDGE = (
    "You still have incomplete plan steps. "
    "Did you intend to stop, or should you continue? "
    "Update your plan to reflect current status."
)


@fir_ext.on("turn_end")
def on_turn_end(params, ctx):
    global turns_since_update, last_update_time, nudges_without_progress
    turns_since_update += 1
    if plan_total <= plan_completed:
        return
    elapsed = time.monotonic() - last_update_time
    if turns_since_update < turn_threshold and elapsed < NUDGE_TIME_THRESHOLD:
        return

    turns_since_update = 0
    last_update_time = time.monotonic()
    nudges_without_progress += 1

    if nudges_without_progress >= STAGNATION_CRIT_THRESHOLD:
        # Inject with system-prompt authority so it cannot be rationalized away.
        ctx.prepend(NUDGE_CRIT.format(n=nudges_without_progress))
    elif nudges_without_progress >= STAGNATION_WARN_THRESHOLD:
        ctx.send_message(
            "nudge",
            NUDGE_WARN,
            display=True,
            deliver_as="steer",
        )
    else:
        ctx.send_message(
            "nudge",
            NUDGE_MILD,
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
