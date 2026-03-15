#!/usr/bin/env python3
# ---
# name: btw
# description: Ask a side question without adding it to session history
# builtin: true
# modes: tui
# commands: btw: Ask a side question using current context (no history)
# ---
"""Ask a quick side question without touching session history.

Runs an ephemeral LLM call with the current session context.
The response is shown as a notification. Nothing is saved to history.

Usage:
  /btw <question>
  /btw what does that error mean?
  /btw which file defines the Router interface?
"""

from __future__ import annotations

import fir_ext


@fir_ext.command(
    name="btw",
    description="Ask a side question using current context (no history)",
)
def cmd_btw(args: list[str], ctx: fir_ext.Context):
    """Handle /btw <question>."""
    question = " ".join(args).strip()
    if not question:
        return {"message": "Usage: /btw <question>"}

    text = ctx.btw(question)
    ctx.notify(f"btw: {text}")
    return {}


fir_ext.run(name="btw")
