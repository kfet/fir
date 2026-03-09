#!/usr/bin/env python3
# ---
# name: plan-nudger
# description: Remind the agent to update its plan periodically
# builtin: true
# ---
import fir_ext

NUDGE_INTERVAL = 5
turns_since_update = 0
has_active_plan = False

@fir_ext.on("session_update")
def on_session_update(params, ctx):
    global turns_since_update, has_active_plan
    plan = params.get("plan", {})
    total = plan.get("total", 0)
    completed = plan.get("completed", 0)
    if total > 0:
        turns_since_update = 0
        has_active_plan = total > completed
    # plan cleared (total == 0) → stop nudging
    if total == 0:
        has_active_plan = False

@fir_ext.on("turn_end")
def on_turn_end(params, ctx):
    global turns_since_update
    turns_since_update += 1
    if turns_since_update >= NUDGE_INTERVAL and has_active_plan:
        turns_since_update = 0
        ctx.send_message(
            "nudge",
            "Reminder: update your plan to reflect current progress.",
            deliver_as="steer",
        )

fir_ext.run(name="plan-nudger")
