#!/usr/bin/env python3
# ---
# name: auto-namer
# description: Automatically name unnamed sessions after the first interaction
# builtin: true
# ---
"""auto_namer.py — derive a short session name from the first user message.

On the first tool call or at the end of the first assistant turn (whichever
comes first), if the session still has no name, this extension uses a
side_query to distil the conversation into a 2-4 word kebab-case slug
(e.g. "fix-login-bug", "add-search-api") and sets it as the session name.
"""

from __future__ import annotations

import re

import fir_ext

_already_named = False
_naming_attempted = False


@fir_ext.on("session_start")
def on_session_start(params, ctx):
    """Reset state at session start."""
    global _already_named, _naming_attempted
    _already_named = False
    _naming_attempted = False


@fir_ext.on("session_named")
def on_session_named(params, ctx):
    """Track when a name is set (by the user, another extension, or resume)."""
    global _already_named
    _already_named = True


def _try_name(ctx):
    """Attempt to auto-name the session exactly once."""
    global _naming_attempted, _already_named
    if _naming_attempted or _already_named:
        return
    _naming_attempted = True

    try:
        raw = ctx.side_query(
            "Stop the conversation here. Do not reply to it. Name this session "
            "for a tmux window title: 2-4 lowercase words joined by hyphens, "
            "like fix-login-bug or add-search-api. Output only the name."
        )
    except Exception:
        return

    name = _sanitise(raw)
    if name:
        ctx.set_session_name(name)


def _sanitise(raw):
    """Return a kebab-case name from the reply, or None if it is not a name.

    A conversational reply is rejected rather than squashed into a blob.
    """
    lines = [ln for ln in (raw or "").splitlines() if ln.strip()]
    if not lines:
        return None
    s = lines[0].strip("`\"'* \t").lower()
    if s.endswith(("?", ":")):
        return None
    s = re.sub(r"[\s_/]+", "-", s)
    s = re.sub(r"[^a-z0-9-]", "", s)
    s = re.sub(r"-{2,}", "-", s).strip("-")
    if not s or len(s.split("-")) > 4 or len(s) > 40:
        return None
    return s


@fir_ext.on("tool_execution_start")
def on_tool_execution_start(params, ctx):
    """Early path: auto-name on first tool call."""
    _try_name(ctx)


@fir_ext.on("turn_end")
def on_turn_end(params, ctx):
    """Fallback: auto-name after first turn if no tool triggered naming."""
    _try_name(ctx)


fir_ext.run(name="auto-namer")
