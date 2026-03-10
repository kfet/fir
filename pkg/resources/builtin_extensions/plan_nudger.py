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

turns_since_update = 0
last_update_time = time.monotonic()
plan_total = 0
plan_completed = 0
turn_threshold = DEFAULT_TURN_THRESHOLD


@fir_ext.on("session_update")
def on_session_update(params, ctx):
    global turns_since_update, last_update_time, plan_total, plan_completed, turn_threshold
    plan = params.get("plan")
    if plan is None:
        return
    plan_total = plan.get("total", 0)
    plan_completed = plan.get("completed", 0)
    turns_since_update = 0
    last_update_time = time.monotonic()
    # The LLM can set metadata key "next_update_in" to hint how many turns
    # until the next expected plan update.
    metadata = plan.get("metadata") or {}
    hint = metadata.get("next_update_in", "")
    try:
        hint_val = int(hint)
    except (ValueError, TypeError):
        hint_val = 0
    turn_threshold = hint_val if hint_val > 0 else DEFAULT_TURN_THRESHOLD


@fir_ext.on("turn_end")
def on_turn_end(params, ctx):
    global turns_since_update, last_update_time
    turns_since_update += 1
    if plan_total <= plan_completed:
        return
    elapsed = time.monotonic() - last_update_time
    if turns_since_update >= turn_threshold or elapsed >= NUDGE_TIME_THRESHOLD:
        turns_since_update = 0
        last_update_time = time.monotonic()
        ctx.send_message(
            "nudge",
            "Reminder: update your plan to reflect current progress.",
            deliver_as="steer",
        )


@fir_ext.on("agent_end")
def on_agent_end(params, ctx):
    if plan_total > plan_completed:
        ctx.send_message(
            "nudge",
            "Your plan has incomplete steps. "
            "Continue working until all steps are completed or cancelled.",
            deliver_as="steer",
        )


fir_ext.run(name="plan-nudger")
