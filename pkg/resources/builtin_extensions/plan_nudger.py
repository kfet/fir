#!/usr/bin/env python3
# ---
# name: plan-nudger
# description: Remind the agent to update its plan periodically
# builtin: true
# ---
import time

import fir_ext

NUDGE_TURN_THRESHOLD = 20
NUDGE_TIME_THRESHOLD = 120  # seconds (2 minutes)

turns_since_update = 0
last_update_time = time.monotonic()
plan_total = 0
plan_completed = 0


@fir_ext.on("session_update")
def on_session_update(params, ctx):
    global turns_since_update, last_update_time, plan_total, plan_completed
    plan = params.get("plan")
    if plan is None:
        return
    plan_total = plan.get("total", 0)
    plan_completed = plan.get("completed", 0)
    turns_since_update = 0
    last_update_time = time.monotonic()


@fir_ext.on("turn_end")
def on_turn_end(params, ctx):
    global turns_since_update, last_update_time
    turns_since_update += 1
    if plan_total <= plan_completed:
        return
    elapsed = time.monotonic() - last_update_time
    if turns_since_update >= NUDGE_TURN_THRESHOLD or elapsed >= NUDGE_TIME_THRESHOLD:
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
