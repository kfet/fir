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
The question and response are shown inline in the UI but are never
saved to the conversation history.

Usage:
  /btw <question>            — ask anything about the current context
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

    ctx.btw(question)
    return {}


fir_ext.run(name="btw")
