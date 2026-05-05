#!/usr/bin/env python3
# ---
# name: handoff
# description: Reliable self-handoff — validate handoff doc, write atomically, restart with clean LLM context
# builtin: true
# modes: tui
# ---
"""handoff.py — reliable self-handoff for fir.

Single tool ``self_handoff(content)``:

  1. Validates ``content`` (length, structure).
  2. Writes it atomically to ``<cwd>/.fir/handoff-<timestamp>.md``.
  3. Verifies the file is readable and non-empty.
  4. Calls the ``restart_session`` bridge RPC, which aborts the in-flight
     stream synchronously and starts a fresh session whose first user
     message points at the doc.

Validation runs to completion BEFORE any restart fires. Bad input yields
a normal tool error and the session continues. Only after every check
passes does the restart trigger.
"""

from __future__ import annotations

import os
import time
from typing import Any

import fir_ext

# ---------------------------------------------------------------------------
# Validation thresholds
# ---------------------------------------------------------------------------

MIN_CONTENT_LEN = 200          # chars after strip — below this is junk
MAX_CONTENT_LEN = 64 * 1024    # chars — above this is an accidental dump
MIN_NON_BLANK_LINES = 3        # a briefing has structure


# ---------------------------------------------------------------------------
# Tool
# ---------------------------------------------------------------------------

_TOOL_DESCRIPTION = """Hand off to a fresh fir session with a clean LLM context.

Atomically: validates the handoff doc, writes it to \
<cwd>/.fir/handoff-<timestamp>.md, then aborts the current turn and \
starts a new session whose first user message instructs the new agent \
to read the doc and continue.

Use when:
  - context window is filling up (>60-70% used)
  - finishing one task and starting another (a major boundary)
  - the user explicitly asks for a handoff or fresh start
  - a long session has accumulated irrelevant context

The doc must be a curated briefing — not a transcript dump. Cover: \
project + branch, what's done, what's in progress with concrete file/\
line anchors, key decisions worth remembering, running services / \
external state, concrete next steps.

Validation rejects content shorter than 200 chars (after strip), longer \
than 64 KB, or with fewer than 3 non-blank lines. On rejection the \
session continues — you can fix and retry.

After validation passes the calling turn is aborted. Do not call any \
other tools after this one and do not emit further explanatory text — \
that turn is being torn down."""


_TOOL_PARAMS: dict[str, Any] = {
    "type": "object",
    "properties": {
        "content": {
            "type": "string",
            "minLength": MIN_CONTENT_LEN,
            "maxLength": MAX_CONTENT_LEN,
            "description": (
                "The handoff document, as markdown. Must be a curated "
                "briefing of at least 200 characters and 3 non-blank "
                "lines. Cover project + branch, completed work, in-"
                "progress task with file/line anchors, key decisions, "
                "running services/state, and concrete next steps."
            ),
        },
    },
    "required": ["content"],
    "additionalProperties": False,
}


def _err(text: str) -> dict:
    return {"content": [{"type": "text", "text": text}], "is_error": True}


def _ok(text: str) -> dict:
    return {"content": [{"type": "text", "text": text}], "is_error": False}


def _project_dir() -> str:
    """Best-effort project directory (cwd reported by fir at init)."""
    cwd = getattr(fir_ext, "cwd", "") or ""
    return cwd or os.getcwd()


def _default_path() -> str:
    base = os.path.join(_project_dir(), ".fir")
    os.makedirs(base, exist_ok=True)
    ts = time.strftime("%Y%m%d-%H%M%S")
    return os.path.abspath(os.path.join(base, f"handoff-{ts}.md"))


def _atomic_write(path: str, content: str) -> None:
    os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
    tmp = f"{path}.tmp.{os.getpid()}"
    with open(tmp, "w", encoding="utf-8") as f:
        f.write(content)
        f.flush()
        os.fsync(f.fileno())
    os.replace(tmp, path)


def _verify_readable(path: str) -> str | None:
    """Return None on success, error string on failure.

    Verifies the file exists, is a regular file, is readable, and is
    non-empty. Run after writing to catch filesystem oddities (full
    disk, permissions, race with external rm).
    """
    if not os.path.exists(path):
        return f"handoff doc not found at {path} after write."
    if not os.path.isfile(path):
        return f"{path} is not a regular file."
    try:
        st = os.stat(path)
    except OSError as exc:
        return f"could not stat {path}: {exc}."
    if st.st_size == 0:
        return f"handoff doc at {path} is empty after write."
    try:
        with open(path, "rb") as f:
            chunk = f.read(64)
    except OSError as exc:
        return f"could not read {path}: {exc}."
    if not chunk:
        return f"handoff doc at {path} read back empty."
    return None


def _validate_content(raw: Any) -> tuple[str | None, str]:
    """Return (error_or_None, normalised_content).

    On success the second element is the content with a single trailing
    newline appended (for tidy on-disk form).
    """
    if not isinstance(raw, str):
        return ("self_handoff: `content` must be a string.", "")
    stripped = raw.strip()
    if not stripped:
        return ("self_handoff: `content` is empty.", "")
    n = len(stripped)
    if n < MIN_CONTENT_LEN:
        return (
            f"self_handoff: `content` is too short ({n} chars, need "
            f"≥{MIN_CONTENT_LEN}). A handoff doc must be a real briefing, "
            "not a placeholder.",
            "",
        )
    if n > MAX_CONTENT_LEN:
        return (
            f"self_handoff: `content` is too long ({n} chars, max "
            f"{MAX_CONTENT_LEN}). Don't paste the transcript — write a "
            "curated briefing.",
            "",
        )
    non_blank = sum(1 for line in stripped.splitlines() if line.strip())
    if non_blank < MIN_NON_BLANK_LINES:
        return (
            f"self_handoff: `content` has only {non_blank} non-blank "
            f"line(s); a structured briefing needs at least "
            f"{MIN_NON_BLANK_LINES}.",
            "",
        )
    normalised = raw if raw.endswith("\n") else raw + "\n"
    return (None, normalised)


@fir_ext.tool(
    name="self_handoff",
    description=_TOOL_DESCRIPTION,
    parameters=_TOOL_PARAMS,
)
def self_handoff(params: dict, ctx: fir_ext.Context) -> dict:
    err, normalised = _validate_content(params.get("content"))
    if err is not None:
        return _err(err)

    path = _default_path()
    try:
        _atomic_write(path, normalised)
    except OSError as exc:
        return _err(f"self_handoff: failed to write {path}: {exc}.")

    verr = _verify_readable(path)
    if verr is not None:
        return _err(f"self_handoff: post-write check failed: {verr}")

    prompt = (
        f"Read and follow the self-handoff document at {path} — "
        "continue where the previous session left off."
    )

    try:
        ctx.restart_session(prompt)
    except Exception as exc:
        return _err(
            f"self_handoff: restart_session failed: {exc}. The current "
            "mode may not support session restart (interactive only). "
            f"The handoff doc was written to {path}; you can recover it "
            "manually."
        )

    # Calling turn is being aborted; this result is informational only.
    return _ok(f"Handing off via {path}…")


# ---------------------------------------------------------------------------
# Slash command: /handoff
# ---------------------------------------------------------------------------


@fir_ext.command(
    name="handoff",
    description=(
        "Hand off to a fresh fir session with a clean LLM context. "
        "Usage: /handoff [optional focus or notes for the next session]"
    ),
)
def cmd_handoff(args: list[str], ctx: fir_ext.Context) -> dict:
    """Handle /handoff — instruct the agent to call ``self_handoff``.

    A slash command cannot itself author a curated briefing, so we
    inject a user-role message asking the agent to write one and call
    the ``self_handoff`` tool. Any extra args become focus hints for
    the briefing.
    """
    extra = " ".join(args).strip()
    prompt_lines = [
        "The user has requested a self-handoff via the /handoff slash command.",
        "",
        "Write a curated briefing that covers:",
        "  - project + branch",
        "  - what is done",
        "  - what is in progress (with concrete file/line anchors)",
        "  - key decisions worth remembering",
        "  - running services / external state",
        "  - concrete next steps",
        "",
        "Then call the `self_handoff` tool with that briefing as `content`.",
        "Do not emit any further explanatory text after the tool call — the "
        "current turn will be aborted and a fresh session will take over.",
    ]
    if extra:
        prompt_lines.extend(["", f"Additional focus / notes from the user: {extra}"])
    ctx.send_user_message("\n".join(prompt_lines))
    return {"message": "Preparing self-handoff briefing…"}


fir_ext.run(name="handoff")
