#!/usr/bin/env python3
# ---
# name: plan-nudger
# description: Remind the model to update an unfinished plan it has stopped updating
# builtin: true
# ---
"""plan-nudger — keep the plan current during long runs.

Counts LLM turns since the last ``plan`` call. When an unfinished plan
has gone ``EVERY`` turns without an update, a short
``[SYS_EXT]`` reminder is injected with ``ctx.prepend``. It lands
right before the next model request, mid-run, so it works inside one
long agent loop. It repeats every overdue period until the plan is
updated or finished.
"""

from __future__ import annotations

import contextlib

import fir_ext

# Turns without a plan update between reminders.
EVERY = 3

NUDGE = "Plan {completed}/{total}, stale {turns} turns. Update it now."

plan_total: int = 0
plan_completed: int = 0
turns: int = 0  # turns since the last plan update
_skip_turn: bool = False  # the turn that made the plan update


@fir_ext.on("session_update")
def on_session_update(params, ctx):
    global plan_total, plan_completed, turns, _skip_turn
    if not params or params.get("type") != "plan_update":
        return
    plan = params.get("plan") or {}
    plan_total = int(plan.get("total", 0) or 0)
    plan_completed = int(plan.get("completed", 0) or 0)
    turns = 0
    _skip_turn = True


def _tick(ctx) -> None:
    global turns, _skip_turn
    if _skip_turn:
        _skip_turn = False
        return
    if plan_total == 0 or plan_completed >= plan_total:
        return
    turns += 1
    if turns % EVERY == 0:
        # prepend drops text already in the conversation; turns grows
        # within a stale window, so at worst one repeat is skipped.
        ctx.prepend(NUDGE.format(completed=plan_completed, total=plan_total, turns=turns))


@fir_ext.on("turn_end")
def on_turn_end(params, ctx):
    with contextlib.suppress(Exception):
        _tick(ctx)


fir_ext.run(name="plan-nudger")
