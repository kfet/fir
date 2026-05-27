#!/usr/bin/env python3
# ---
# name: plan-nudger
# description: When the plan stagnates, ask the model what (if anything) to inject
# builtin: true
# ---
"""plan-nudger — single-trigger plan-stagnation handler.

When ``metadata.progress_metric`` has been unchanged across several
consecutive plan updates, ask the model (via ``side_query`` carrying
the live session context) what to say. The reply is injected verbatim
as a follow-up user message; an empty reply is a noop.

All judgement (done-detection, drift vs replanning, blocked-on-user,
message wording, whether to nudge at all) is delegated to the model.
This extension is a trigger and a transport, not a judge. Always
runs on the current session model — no advisor escalation.
"""

from __future__ import annotations

import contextlib
import json

import fir_ext

# Stagnations before we consult the model
STAGNATION_THRESHOLD = 2

# Last plan snapshot. The model sees entries via session transcript.
plan_total: int = 0
plan_completed: int = 0
plan_metadata: dict = {}

# Plan-updates since ``progress_metric`` last changed. ``_last_metric``
# is ``None`` until the first plan_update so an unset/empty metric on
# turn one is a baseline, not stagnation against the empty default.
stagnation: int = 0
_last_metric: str | None = None

# Stagnation value at which we last consulted.
# Used to prevent re-firing. Cleared on any stagnation reset.
_nudged_at: int = -1


PROMPT = """\
The plan seems to have stagnated — `progress_metric` has been unchanged across
multiple plan updates.

Plan updates are very important for user visibility. Completing the plan even more so.

PLAN STATE (from the host extension):
{plan_state}

Decide if we need to inject a short message to promote plan update or
commpletion.

Your response will be direclty injected, return "" for noop.
"""


# --- Event handlers ---------------------------------------------------------


@fir_ext.on("session_update")
def on_session_update(params, ctx):
    """Absorb plan_update events; tick stagnation when the metric holds."""
    global plan_total, plan_completed, plan_metadata
    global stagnation, _last_metric, _nudged_at

    if not params or params.get("type") != "plan_update":
        return
    plan = params.get("plan") or {}

    plan_total = int(plan.get("total", 0) or 0)
    plan_completed = int(plan.get("completed", 0) or 0)
    plan_metadata = plan.get("metadata") or {}

    metric = (plan_metadata.get("progress_metric") or "").strip()
    if metric != _last_metric:
        # Baseline or genuine progress: reset counters so an
        # oscillating metric can't silently silence the nudge.
        stagnation = 0
        _last_metric = metric
        _nudged_at = -1
    else:
        stagnation += 1


def _nudge(ctx) -> None:
    """Body of the turn_end handler. Extracted so the outer wrapper
    suppresses unexpected exceptions without nesting the gating logic."""
    global _nudged_at

    if plan_total == 0 or plan_completed >= plan_total:
        return
    if stagnation < STAGNATION_THRESHOLD:
        return
    if _nudged_at == stagnation:
        return  # already consulted at this stagnation level

    # Mark before delivery: noop / error outcomes also count, so we
    # don't retry every turn on persistent failure or quiet replies.
    _nudged_at = stagnation

    prompt = PROMPT.format(plan_state=json.dumps(
        {
            "total": plan_total,
            "completed": plan_completed,
            "metadata": plan_metadata,
            "stagnation_count": stagnation,
        },
        indent=2,
        sort_keys=True,
    ))

    text = ctx.side_query(prompt)
    message = (text or "").strip()
    if message:
        ctx.send_user_message(message, deliver_as="followUp")


@fir_ext.on("turn_end")
def on_turn_end(params, ctx):
    """Consult when stagnation crosses the threshold. All failures
    are swallowed — the host session never crashes because of us."""
    with contextlib.suppress(Exception):
        _nudge(ctx)


fir_ext.run(name="plan-nudger")
