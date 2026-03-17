#!/usr/bin/env python3
# ---
# name: auto-namer
# description: Automatically name unnamed sessions after the first interaction
# builtin: true
# events: session_start, session_named, agent_end
# ---
"""auto_namer.py — derive a short session name from the first user message.

On the first agent_end, if the session still has no name, this extension uses
a side_query to distil the conversation into a three-word kebab-case slug
(e.g. "fix-login-bug", "add-search-api") and sets it as the session name.
"""

from __future__ import annotations

import re

import fir_ext

_already_named = False
_first_turn_done = False


@fir_ext.on("session_start")
def on_session_start(params, ctx):
    """Reset state at session start."""
    global _already_named, _first_turn_done
    _already_named = False
    _first_turn_done = False


@fir_ext.on("session_named")
def on_session_named(params, ctx):
    """Track when a name is set (by the user, another extension, or resume)."""
    global _already_named
    _already_named = True


@fir_ext.on("agent_end")
def on_agent_end(params, ctx):
    """After the first agent turn, auto-name the session if still unnamed."""
    global _first_turn_done, _already_named
    if _first_turn_done or _already_named:
        return
    _first_turn_done = True

    try:
        raw = ctx.side_query(
            "Based on the conversation so far, generate a very short session "
            "name in kebab-case (lowercase words separated by hyphens). "
            "Use exactly 2 to 4 words that capture the main intent. "
            "Examples: fix-login-bug, add-search-api, refactor-db-layer, "
            "update-readme. Reply with ONLY the kebab-case name, nothing else."
        )
    except Exception:
        return

    # Sanitise: keep only the first line, strip whitespace, enforce kebab-case.
    lines = raw.strip().splitlines()
    if not lines:
        return
    name = lines[0].strip().strip("`\"'").lower()
    # Remove anything that isn't a lowercase letter, digit, or hyphen.
    name = re.sub(r"[^a-z0-9-]", "", name)
    # Collapse multiple hyphens and strip leading/trailing hyphens.
    name = re.sub(r"-{2,}", "-", name).strip("-")

    if name:
        ctx.set_session_name(name)


fir_ext.run(name="auto-namer")
