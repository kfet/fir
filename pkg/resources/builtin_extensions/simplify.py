#!/usr/bin/env python3
# ---
# name: simplify
# description: Review recent git changes and apply simplifications
# builtin: false
# modes: tui
# commands: simplify: Review recent changes and apply simplifications (code reuse / quality / efficiency)
# ---
"""Review recent git changes and ask the agent to simplify them.

Captures the most recent git diff (staged → unstaged → last commit) and
injects a structured review prompt into the session so the agent applies
simplifications across three dimensions: code reuse, code quality, and
efficiency.

Usage:
  /simplify                  — review recent changes for all simplifications
  /simplify <focus>          — narrow the review (e.g. /simplify memory allocation)
"""

from __future__ import annotations

import subprocess

import fir_ext


def _git(*args: str) -> str:
    """Run a git command and return stripped stdout, or '' on failure."""
    try:
        result = subprocess.run(
            ["git", *args],
            capture_output=True,
            text=True,
            timeout=10,
        )
        return result.stdout.strip()
    except Exception:
        return ""


def _gather_diff() -> tuple[str, str]:
    """Return (diff_text, label) for the most relevant recent changes.

    Priority:
      1. Staged changes (git diff --cached)
      2. Unstaged changes (git diff)
      3. Last commit (git diff HEAD~1 HEAD)
    """
    if diff := _git("diff", "--cached"):
        return diff, "staged"
    if diff := _git("diff"):
        return diff, "working tree"
    if diff := _git("diff", "HEAD~1", "HEAD"):
        return diff, "last commit"
    return "", ""


@fir_ext.command(
    name="simplify",
    description="Review recent changes and apply simplifications (code reuse / quality / efficiency)",
)
def cmd_simplify(args: list[str], ctx: fir_ext.Context):
    """Handle /simplify [focus]."""
    focus = " ".join(args).strip()

    diff, label = _gather_diff()
    if not diff:
        return {"message": "Nothing to simplify: no git changes detected."}

    lines = [
        "Review the following recent changes and apply simplifications directly to the source files.\n",
        "Evaluate the changes across three dimensions:",
        "1. **Code reuse** — eliminate duplication, extract shared helpers",
        "2. **Code quality** — improve readability, naming, structure, and adherence to project conventions",
        "3. **Efficiency** — remove unnecessary allocations, redundant work, or slow patterns\n",
    ]
    if focus:
        lines.append(f"Focus especially on: {focus}\n")
    lines.append(
        "Make only changes that are clearly improvements. Do not add features or change behaviour.\n"
    )
    lines.append(f"Recent changes ({label}):\n```diff\n{diff}\n```")

    ctx.send_user_message("\n".join(lines))
    return {}


fir_ext.run(name="simplify")
