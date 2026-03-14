#!/usr/bin/env python3
# ---
# name: simplify
# description: Review recent git changes and apply simplifications
# builtin: false
# modes: tui
# commands: simplify: Review recent changes and apply simplifications (code reuse / quality / efficiency)
# ---
"""Ask the agent to review and simplify recent git changes.

Usage:
  /simplify                  — review recent changes for all simplifications
  /simplify <focus>          — narrow the review (e.g. /simplify memory allocation)
"""

from __future__ import annotations

import fir_ext


@fir_ext.command(
    name="simplify",
    description="Review recent changes and apply simplifications (code reuse / quality / efficiency)",
)
def cmd_simplify(args: list[str], ctx: fir_ext.Context):
    """Handle /simplify [focus]."""
    focus = " ".join(args).strip()

    prompt = (
        "Review the recent git changes (staged, unstaged, or last commit — "
        "whichever are most relevant) and apply simplifications directly to "
        "the source files across three dimensions:\n"
        "1. **Code reuse** — eliminate duplication, extract shared helpers\n"
        "2. **Code quality** — improve readability, naming, structure, and "
        "adherence to project conventions\n"
        "3. **Efficiency** — remove unnecessary allocations, redundant work, "
        "or slow patterns\n"
    )
    if focus:
        prompt += f"\nFocus especially on: {focus}\n"
    prompt += "\nMake only changes that are clearly improvements. Do not add features or change behaviour."

    ctx.send_user_message(prompt)
    return {}


fir_ext.run(name="simplify")
