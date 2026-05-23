#!/usr/bin/env python3
# ---
# name: handoff
# description: Reliable self-handoff with bookmark-based continuity — validate handoff doc, pin significant turns, restart with a clean LLM context
# builtin: true
# modes: tui
# ---
"""handoff.py — reliable self-handoff for fir, with bookmarks.

Two tools and one slash command:

* ``self_handoff(content)`` — abort the in-flight turn and restart with
  a clean LLM context, briefing carried in-context. When bookmarks
  exist, a pointer line to the bookmarks file is appended to the
  briefing so the child agent can pull the high-fidelity highlight
  reel without the parent having to re-author it.

* ``bookmark(quote, note)`` — pin a past turn as significant. The
  quote uniquely identifies a turn anywhere in the session JSONL
  (user message, assistant message, tool call, tool result, system
  message). The entire turn entry is copied as-is to
  ``bookmarks-<session-id>.jsonl`` with the note injected as a new
  ``_bookmark_note`` key. The file is kept sorted by the original
  turn's timestamp so it reads chronologically regardless of
  bookmark-call order.

* ``/handoff`` — slash command. Asks the agent to write a curated
  briefing and call ``self_handoff``.

Validation runs to completion BEFORE any restart fires. Bad input
yields a normal tool error and the session continues.

The bookmarks file is the source of truth. An observable card
(``handoff/bookmarks``) is a derived projection written on every
``bookmark()`` call. We rely on the cards file being read on session
construct (see ``docs/design/observable-cards.md``) so the card
survives ``/reexec`` for free — no reconciler needed.

See ``docs/design/handoff-bookmarks.md``.
"""

from __future__ import annotations

import contextlib
import json
import os
import threading
from typing import Any

import fir_ext

# ---------------------------------------------------------------------------
# Validation thresholds (self_handoff)
# ---------------------------------------------------------------------------

MIN_CONTENT_LEN = 200          # chars after strip — below this is junk
MAX_CONTENT_LEN = 64 * 1024    # chars — above this is an accidental dump
MIN_NON_BLANK_LINES = 3        # a briefing has structure

# ---------------------------------------------------------------------------
# Bookmark constants
# ---------------------------------------------------------------------------

# Observable card key under our (host-stamped) source name.
_CARD_KEY = "bookmarks"

# Cap the card detail at this many most-recent bookmarks. Purely a
# human-readability bound on the rendered string — observe_session
# truncates further as needed. Doesn't affect the bookmarks file.
_CARD_MAX_BOOKMARKS_IN_DETAIL = 20

# Serialises the bookmark file's read/append/sort/write critical
# section. Multiple concurrent bookmark() calls on the same session
# would otherwise race and lose entries.
_bookmarks_lock = threading.Lock()


# ---------------------------------------------------------------------------
# self_handoff: tool description + validation
# ---------------------------------------------------------------------------

_TOOL_DESCRIPTION = """Hand off to a fresh fir session with a clean LLM context.

Atomically: validates the handoff doc, aborts the current turn, and \
starts a new session whose conversation begins with your briefing as \
an authoritative [SYS_EXT] message followed by a short prompt telling \
the new agent to continue.

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

The briefing is carried in-context to the new session (no filesystem \
artifact is written). The new session's jsonl log preserves it.

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


# ---------------------------------------------------------------------------
# bookmark: tool description
# ---------------------------------------------------------------------------

_BOOKMARK_DESCRIPTION = """Pin a significant turn from earlier in this session as a bookmark.

Use as a lightweight, continuous form of state preservation — call \
this whenever something happens that the next agent (after a future \
/handoff) would need to know. Examples: a final design decision is \
locked in, the user states a constraint, a tool returns a critical \
result, you discover something non-obvious about the code, a long \
debugging session converges.

You may bookmark ANY past turn type: user message, assistant message, \
tool call arguments, tool result, or system message. The quote is \
matched as a substring against the decoded text of each transcript \
turn, latest-first. If multiple turns match, the most recent wins.

Arguments:
- quote: minimum exact text that uniquely identifies the turn. Short \
  and distinctive is best.
- note: short label explaining why this is significant (e.g. \
  'final DB schema', 'user constraint: no auth', 'fix for bug #42').

The full turn entry (role, content, tool calls/results, timestamps) \
is copied as-is to bookmarks-<session-id>.jsonl next to the session \
transcript, with your note injected as _bookmark_note. The file is \
kept sorted by the original turn's timestamp.

When /handoff runs, the child session is told where to find the \
bookmarks file and reads it directly. No further action needed at \
handoff time.

Returns an error if the quote matches no turn — refine and retry."""


_BOOKMARK_PARAMS: dict[str, Any] = {
    "type": "object",
    "properties": {
        "quote": {
            "type": "string",
            "minLength": 1,
            "description": (
                "Minimum exact text that uniquely identifies the turn "
                "to bookmark. Substring match against decoded transcript "
                "text, latest-first; most recent wins on ties."
            ),
        },
        "note": {
            "type": "string",
            "minLength": 1,
            "maxLength": 240,
            "description": (
                "Short label for why this turn is significant. Will "
                "appear in the bookmarks file and the handoff/bookmarks "
                "observable card."
            ),
        },
    },
    "required": ["quote", "note"],
    "additionalProperties": False,
}


# ---------------------------------------------------------------------------
# Small helpers
# ---------------------------------------------------------------------------


def _err(text: str) -> dict:
    return {"content": [{"type": "text", "text": text}], "is_error": True}


def _ok(text: str) -> dict:
    return {"content": [{"type": "text", "text": text}], "is_error": False}


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


def _bookmarks_path(ctx: fir_ext.Context) -> str:
    """Resolve ``bookmarks-<session-id>.jsonl`` next to the transcript.

    Returns "" when the session has no persisted transcript (in-memory
    only). Both ``get_session_file`` and ``get_session_id`` are
    populated whenever the session has a file — when either is empty
    we have no on-disk place to write.
    """
    session_file = ctx.get_session_file()
    session_id = ctx.get_session_id()
    if not session_file or not session_id:
        return ""
    return os.path.join(
        os.path.dirname(session_file), f"bookmarks-{session_id}.jsonl",
    )


# ---------------------------------------------------------------------------
# Transcript scanning
# ---------------------------------------------------------------------------


def _json_contains(value: Any, quote: str) -> bool:
    """Return whether any string leaf inside ``value`` contains ``quote``.

    The model writes ``quote`` from text it sees in its context, which
    is the decoded turn content — not the raw JSON bytes. Walking the
    decoded structure means JSON escapes (``\\n``, ``\\u003c``, etc.)
    don't trip us up.
    """
    if isinstance(value, str):
        return quote in value
    if isinstance(value, dict):
        return any(_json_contains(v, quote) for v in value.values())
    if isinstance(value, list):
        return any(_json_contains(v, quote) for v in value)
    return False


def _find_turn_by_quote(
    transcript_path: str, quote: str,
) -> tuple[dict | None, int]:
    """Reverse-scan ``transcript_path`` for ``quote``.

    Returns ``(entry, match_count)``: ``entry`` is the most recent
    matching JSONL line parsed as a dict, or ``None`` if no line
    matched; ``match_count`` is the total number of matches (surfaced
    to the model so it knows when its quote was ambiguous).

    The session header line (no ``type`` field) is skipped:
    bookmarking the header has no meaning and confuses the timestamp
    sort.
    """
    try:
        with open(transcript_path, "rb") as f:
            data = f.read()
    except OSError:
        return (None, 0)

    most_recent: dict | None = None
    count = 0
    for line in reversed(data.splitlines()):
        try:
            obj = json.loads(line)
        except (ValueError, TypeError):
            continue
        if not isinstance(obj, dict) or "type" not in obj:
            continue  # header or unparseable
        if not _json_contains(obj, quote):
            continue
        count += 1
        if most_recent is None:
            most_recent = obj
    return (most_recent, count)


# ---------------------------------------------------------------------------
# Bookmarks file IO
# ---------------------------------------------------------------------------


def _entry_sort_key(obj: dict) -> tuple[str, str]:
    """Sort by transcript ``timestamp``, tiebroken by entry ``id``."""
    return (str(obj.get("timestamp") or ""), str(obj.get("id") or ""))


def _read_bookmarks(path: str) -> list[dict]:
    """Read all bookmark entries from ``path``. Returns [] on missing."""
    try:
        with open(path, "rb") as f:
            data = f.read()
    except OSError:
        return []
    out: list[dict] = []
    for line in data.splitlines():
        if not line.strip():
            continue
        try:
            obj = json.loads(line)
        except (ValueError, TypeError):
            continue
        if isinstance(obj, dict):
            out.append(obj)
    return out


def _write_bookmarks(path: str, entries: list[dict]) -> None:
    """Atomically rewrite ``path`` with ``entries`` (one JSON line each).

    Temp file + rename. Same pattern as
    ``pkg/session/store/observables.go``.
    """
    os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
    tmp = path + ".tmp"
    with open(tmp, "wb") as f:
        for obj in entries:
            f.write(json.dumps(obj, separators=(",", ":")).encode("utf-8"))
            f.write(b"\n")
    os.replace(tmp, path)


# ---------------------------------------------------------------------------
# Card rendering
# ---------------------------------------------------------------------------


def _hhmm(ts: str) -> str:
    """Extract ``HH:MM`` from an ISO timestamp; returns "" on bad input."""
    if "T" not in ts:
        return ""
    after_t = ts.split("T", 1)[1]
    for sep in ("+", "-", "Z", "."):
        if sep in after_t:
            after_t = after_t.split(sep, 1)[0]
            break
    parts = after_t.split(":")
    return f"{parts[0]}:{parts[1]}" if len(parts) >= 2 else ""


def _render_card(path: str, entries: list[dict]) -> tuple[str, str]:
    """Return ``(slug, detail)`` for the handoff/bookmarks observable.

    Slug is ``"<N> pinned"``; detail starts with a count + absolute
    path line (so a reader can paste the path straight into ``read``),
    then up to ``_CARD_MAX_BOOKMARKS_IN_DETAIL`` most-recent bookmarks
    as ``- HH:MM  note`` lines.
    """
    n = len(entries)
    slug = f"{n} pinned"
    head = f"{n} bookmark{'s' if n != 1 else ''} ({path}):"
    # Entries are sorted ascending by timestamp; tail is most recent.
    tail = entries[-_CARD_MAX_BOOKMARKS_IN_DETAIL:]
    lines = [head]
    for e in tail:
        hhmm = _hhmm(str(e.get("timestamp") or ""))
        note = str(e.get("_bookmark_note") or "").strip()
        lines.append(f"- {hhmm}  {note}" if hhmm else f"- {note}")
    return (slug, "\n".join(lines))


# ---------------------------------------------------------------------------
# Tools
# ---------------------------------------------------------------------------


@fir_ext.tool(
    name="bookmark",
    description=_BOOKMARK_DESCRIPTION,
    parameters=_BOOKMARK_PARAMS,
    display_hint={
        "title_args": [{"name": "note", "style": "accent"}],
        "result_max_lines": 3,
    },
)
def bookmark(params: dict, ctx: fir_ext.Context) -> dict:
    quote = (params.get("quote") or "").strip()
    note = (params.get("note") or "").strip()
    if not quote:
        return _err("bookmark: `quote` must be a non-empty string.")
    if not note:
        return _err("bookmark: `note` must be a non-empty string.")

    bm_path = _bookmarks_path(ctx)
    if not bm_path:
        return _err(
            "bookmark: no persistent session transcript available "
            "(in-memory session?). Bookmarks require a persisted "
            "session file."
        )
    transcript_path = ctx.get_session_file()

    entry, match_count = _find_turn_by_quote(transcript_path, quote)
    if entry is None:
        return _err(
            f"bookmark: quote not found in session transcript "
            f"({transcript_path}). Refine the quote and retry — try a "
            "shorter, more distinctive substring."
        )

    # Duplicate the entry so we never mutate the matched object.
    bookmarked = dict(entry)
    bookmarked["_bookmark_note"] = note

    # Critical section: append + sort + write + card publish.
    with _bookmarks_lock:
        existing = _read_bookmarks(bm_path)
        existing.append(bookmarked)
        existing.sort(key=_entry_sort_key)
        _write_bookmarks(bm_path, existing)
        slug, detail = _render_card(bm_path, existing)
        with contextlib.suppress(Exception):
            ctx.put_observable(_CARD_KEY, slug, detail)

    return _ok(
        f"bookmarked turn id={bookmarked.get('id') or '?'} "
        f"ts={bookmarked.get('timestamp') or '?'} "
        f"(matches={match_count}, total bookmarks={len(existing)})"
    )


@fir_ext.tool(
    name="self_handoff",
    description=_TOOL_DESCRIPTION,
    parameters=_TOOL_PARAMS,
)
def self_handoff(params: dict, ctx: fir_ext.Context) -> dict:
    err, normalised = _validate_content(params.get("content"))
    if err is not None:
        return _err(err)

    # Append a single pointer line to the briefing when the bookmarks
    # file is non-empty. Stat is sufficient — bookmark() never leaves
    # a half-written or corrupt file (atomic temp+rename), and an
    # externally truncated/touched file is not our problem to defend
    # against. The pointer is a footnote: the human-authored briefing
    # stays the first thing the child sees. ``normalised`` always ends
    # in ``\n`` (guaranteed by ``_validate_content``) so the leading
    # ``\n`` on the pointer string yields a blank-line separator.
    bm_path = _bookmarks_path(ctx)
    if bm_path and os.path.exists(bm_path) and os.path.getsize(bm_path) > 0:
        normalised += (
            f"\nBookmarks from parent session: {bm_path} — chronological "
            "highlight reel of turns the previous agent pinned as "
            "significant. Read before proceeding.\n"
        )

    try:
        ctx.restart_session(
            "Continue from the handoff briefing above.",
            prepend_context=normalised,
        )
    except Exception as exc:
        return _err(
            f"self_handoff: restart_session failed: {exc}. The current "
            "mode may not support session restart (interactive only)."
        )
    # Calling turn is being aborted; this result is informational only.
    return _ok("Handing off…")


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
    """Inject a user-role message asking the agent to author + call self_handoff.

    A slash command cannot itself author a curated briefing, so we
    nudge the agent. Any extra args become focus hints for the briefing.
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
