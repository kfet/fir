#!/usr/bin/env python3
# ---
# name: handoff
# description: Reliable self-handoff with bookmark-based continuity — validate handoff doc, pin significant turns, restart with a clean LLM context
# builtin: true
# modes: tui, acp
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

* ``pin()`` — argless reflex. The model calls 📌 the instant it senses
  something worth keeping (no args, no deliberation). A side-query
  branch with full session context then decides which turn(s) actually
  merit a bookmark and anchors them via the same write path as
  ``bookmark``. Keeps the cost of *signalling* near zero while moving
  the *judgement* off the main thread.

* ``/handoff`` — slash command. Asks the agent to write a curated
  briefing and call ``self_handoff``.

Validation runs to completion BEFORE any restart fires. Bad input
yields a normal tool error and the session continues.

The bookmarks file is the source of truth. An observable card
(``handoff/bookmarks``) is a derived projection written on every
``bookmark()`` call. We rely on the cards file being read on session
construct (see ``docs/design/observable-cards.md``) so the card
survives ``/reexec`` for free — no reconciler needed.

A ``session_start`` handler additionally runs a one-time, idempotent
migration that repairs pre-existing bookmarks files (and their cards)
written before the self-match guard existed — see the migration block
below.

See ``docs/design/handoff-bookmarks.md``.
"""

from __future__ import annotations

import contextlib
import json
import os
import threading
import time
from typing import Any

try:
    import fcntl
except ImportError:  # pragma: no cover - non-POSIX (fir targets unix)
    fcntl = None  # ty: ignore[invalid-assignment]

import fir_ext

# ---------------------------------------------------------------------------
# Validation thresholds (self_handoff)
# ---------------------------------------------------------------------------

MIN_CONTENT_LEN = 200  # chars after strip — below this is junk
MAX_CONTENT_LEN = 64 * 1024  # chars — above this is an accidental dump
MIN_NON_BLANK_LINES = 3  # a briefing has structure

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
# section *within this process*. Multiple concurrent bookmark() calls on
# the same session would otherwise race and lose entries. Cross-process
# safety (bookmark() vs. the repair migration in another session's
# extension) is handled separately by an flock sidecar — see
# ``_bookmarks_filelock``.
_bookmarks_lock = threading.Lock()


@contextlib.contextmanager
def _bookmarks_filelock(bm_path: str, *, blocking: bool):
    """Advisory cross-process lock on a sidecar beside the bookmarks file.

    Yields ``True`` when the lock is held, ``False`` when ``blocking`` is
    False and another process holds it. Shared by ``bookmark()``
    (blocking — the write must complete) and the repair migration
    (non-blocking — a held lock means a live session owns the file and
    will reconcile it itself, so the sweep simply skips it).

    The lock lives on a stable sidecar inode (``<bm_path>.lock``) rather
    than the bookmarks file, because the file is replaced via atomic
    temp+rename — an flock on the renamed-away inode would be invisible
    to the next opener. flock is released automatically on close /
    process death, so a crash never deadlocks. Degrades to a no-op
    (always "held") when ``fcntl`` is unavailable.
    """
    if fcntl is None:
        yield True
        return
    lock_path = bm_path + ".lock"
    fd = None
    try:
        os.makedirs(os.path.dirname(lock_path) or ".", exist_ok=True)
        fd = os.open(lock_path, os.O_CREAT | os.O_RDWR, 0o644)
        flags = fcntl.LOCK_EX if blocking else (fcntl.LOCK_EX | fcntl.LOCK_NB)
        try:
            fcntl.flock(fd, flags)
        except OSError:
            yield False
            return
        try:
            yield True
        finally:
            with contextlib.suppress(OSError):
                fcntl.flock(fd, fcntl.LOCK_UN)
    finally:
        if fd is not None:
            with contextlib.suppress(OSError):
                os.close(fd)


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
turn, latest-first. If multiple turns match, the most recent wins. \
(Your own bookmark calls are never matched, so quoting text you just \
wrote into a bookmark resolves to the original turn, not the call.)

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
# pin: argless reflex tool
# ---------------------------------------------------------------------------

_PIN_DESCRIPTION = """📌 pin — always call this the moment the \
conversation produces something worth keeping.

Whenever the latest turn contains ANY of the following, call pin (no \
arguments) BEFORE you write your reply:
- a decision or choice being finalized ("we'll go with…", "decision \
is final", "let's commit to…")
- a constraint, rule, or requirement stated ("must never…", \
"always…", "the rule is…")
- a root cause or key finding ("we found it", "the cause is…", \
"turns out…", a bug's fix)
- a non-obvious fact or gotcha about the code or system
- a plan locked in or a milestone reached

This is not optional. Do not deliberate and do not explain — just \
call pin, then continue answering. Pinning is free and harmless; a \
separate branch later decides what (if anything) to actually keep \
and where to anchor it — including turns from earlier in the \
session. Failing to pin loses the moment permanently, so when in \
doubt, pin."""

_PIN_PARAMS: dict[str, Any] = {
    "type": "object",
    "properties": {},
    "additionalProperties": False,
}

# Prompt for the decision branch (a no-tools side query over full
# session context). It returns a tiny line list, NOT a tool-call
# protocol: one bookmark per line as ``quote ||| note``, or ``SKIP``.
_PIN_BRANCH_PROMPT = """A 📌 pin was just dropped in this session: the \
agent sensed that something recent (or earlier) is worth preserving as \
a bookmark for a future handoff.

Look back over the conversation and decide which specific turn(s), if \
any, genuinely merit a bookmark. For each one, output exactly one line:

    <verbatim quote from that turn> ||| <one-line note on why it matters>

Rules:
- The quote must be a short, distinctive substring copied VERBATIM from \
that turn's text, so it can be matched in the transcript. Prefer the \
shortest uniquely-identifying snippet.
- The note is a terse label (e.g. 'final DB schema', 'user constraint: \
no auth', 'fix for bug #42').
- You may bookmark earlier turns, not just the latest.
- Be selective: only turns a future agent would truly need. Skip \
chit-chat, restated context, and anything already obvious.
- If nothing genuinely merits a bookmark, reply with exactly: SKIP

Output ONLY the lines (or SKIP). No preamble, no commentary, no \
numbering, no code fences."""


def _parse_pin_branch(out: str) -> list[tuple[str, str]]:
    """Parse the decision branch's reply into (quote, note) pairs.

    Tolerant of stray formatting: ignores blank lines, a lone SKIP,
    code fences, and list bullets. Only lines containing the ``|||``
    delimiter with non-empty both sides are kept.
    """
    pairs: list[tuple[str, str]] = []
    for raw in (out or "").splitlines():
        line = raw.strip()
        if not line or line == "```" or line.upper() == "SKIP":
            continue
        # Strip common list bullets the model might prepend.
        for bullet in ("- ", "* "):
            if line.startswith(bullet):
                line = line[len(bullet) :].strip()
        if "|||" not in line:
            continue
        quote, note = line.split("|||", 1)
        quote, note = quote.strip(), note.strip()
        if quote and note:
            pairs.append((quote, note))
    return pairs


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
        os.path.dirname(session_file),
        f"bookmarks-{session_id}.jsonl",
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


def _is_bookmark_call(value: Any) -> bool:
    """Return whether ``value`` is/contains a ``bookmark`` tool call.

    The assistant turn that *invokes* ``bookmark()`` is persisted to the
    transcript before this handler reverse-scans it, and it carries the
    model's ``quote`` verbatim inside the tool-call arguments. Without
    excluding it, every quote substring-matches its own bookmark call —
    which is always the newest entry — so the scan would resolve to the
    bookmark call instead of the earlier turn the model meant to pin.
    Bookmarking a bookmark is never meaningful, so we skip these turns
    entirely.
    """
    if isinstance(value, dict):
        if value.get("type") == "toolCall" and value.get("name") == "bookmark":
            return True
        return any(_is_bookmark_call(v) for v in value.values())
    if isinstance(value, list):
        return any(_is_bookmark_call(v) for v in value)
    return False


def _find_turn_by_quote(
    transcript_path: str,
    quote: str,
) -> tuple[dict | None, int]:
    """Reverse-scan ``transcript_path`` for ``quote``.

    Returns ``(entry, match_count)``: ``entry`` is the most recent
    matching JSONL line parsed as a dict, or ``None`` if no line
    matched; ``match_count`` is the total number of matches (surfaced
    to the model so it knows when its quote was ambiguous).

    The session header line (no ``type`` field) and ``bookmark`` tool-
    call turns (see ``_is_bookmark_call``) are skipped: bookmarking the
    header or a bookmark call has no meaning and the latter would always
    self-match the just-issued call.
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
        if _is_bookmark_call(obj):
            continue  # never self-match the bookmark call (see helper)
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
# One-time migration: repair v1 bookmarks that self-matched the bookmark call
# ---------------------------------------------------------------------------
#
# Before the ``_is_bookmark_call`` guard existed, every bookmark resolved
# to its own ``bookmark()`` call turn (the quote always substring-matched
# the call's own arguments, and that call is the newest transcript entry).
# So existing bookmarks files store the bookmark-call turn instead of the
# substantive turn, and the persisted ``handoff/bookmarks`` cards show the
# call's timestamp. Those entries are self-describing — each carries the
# original ``quote`` (in the tool-call args) and ``note`` — and the sibling
# transcript still exists, so we can re-resolve and repair in place.
#
# Repair happens on ``session_start`` in two parts (see
# ``on_session_start``): each session first self-heals its own file
# (so /reexec'd and late-upgraded sessions fix themselves), then a
# one-time global backlog sweep — guarded by a version marker under the
# config dir — fixes files of sessions that will never reopen. Per-file
# safety against a live ``bookmark()`` comes from a non-blocking flock
# sidecar (``_bookmarks_filelock``); no host-wide sweep lock is taken,
# so a crash can never wedge the migration. It is idempotent: a repaired
# entry is no longer a bookmark call, so a re-run finds nothing to fix.

# Bump when the migration logic changes in a way that should re-run.
_MIGRATION_VERSION = 1
_MIGRATION_MARKER = ".handoff-bookmarks-migration"

# When directly patching a persisted ``.cards`` file, skip it if it was
# written within this window — fir core (Go) owns ``.cards`` writes and
# does not honour our flock, so a recent mtime means a live session may
# be writing it; let that session republish instead of racing it.
_CARDS_LIVE_WINDOW_S = 15


def _bookmark_call_quote(value: Any) -> str | None:
    """Extract the ``quote`` argument from a ``bookmark`` call in ``value``."""
    if isinstance(value, dict):
        if value.get("type") == "toolCall" and value.get("name") == "bookmark":
            args = value.get("arguments")
            if not isinstance(args, dict):
                args = value.get("args")
            if isinstance(args, dict):
                q = args.get("quote")
                if isinstance(q, str) and q.strip():
                    return q.strip()
        for v in value.values():
            r = _bookmark_call_quote(v)
            if r is not None:
                return r
    elif isinstance(value, list):
        for v in value:
            r = _bookmark_call_quote(v)
            if r is not None:
                return r
    return None


def _sibling_transcript(bm_path: str) -> str:
    """Find the transcript file sitting next to a bookmarks file.

    ``bookmarks-<sid>.jsonl`` lives beside ``<ts>_<sid>.jsonl``. Returns
    "" when no sibling transcript can be located.
    """
    d = os.path.dirname(bm_path)
    base = os.path.basename(bm_path)
    if not base.startswith("bookmarks-") or not base.endswith(".jsonl"):
        return ""
    sid = base[len("bookmarks-") : -len(".jsonl")]
    try:
        names = os.listdir(d)
    except OSError:
        return ""
    for name in names:
        if name.startswith("bookmarks-") or not name.endswith(".jsonl"):
            continue
        if name.endswith(f"{sid}.jsonl"):
            return os.path.join(d, name)
    return ""


def _rewrite_card_file(bm_path: str, slug: str, detail: str) -> None:
    """Patch the persisted handoff/bookmarks card for an INACTIVE session.

    Only the global backlog sweep uses this — for sessions with no live
    fir process, so there is no concurrent ``.cards`` writer to race. We
    still skip a recently-modified ``.cards`` (see ``_CARDS_LIVE_WINDOW_S``)
    as cheap insurance against an old-code session writing during the
    upgrade window. The *current* session never takes this path; it
    republishes via ``ctx.put_observable`` so fir core owns the write.
    Best-effort: any error is swallowed (the bookmarks file is the source
    of truth regardless).
    """
    transcript = _sibling_transcript(bm_path)
    if not transcript:
        return
    cards_path = transcript + ".cards"
    try:
        if time.time() - os.path.getmtime(cards_path) < _CARDS_LIVE_WINDOW_S:
            return  # a live observable writer may be active — don't race it
    except OSError:
        return
    try:
        with open(cards_path, "rb") as f:
            cards = json.loads(f.read())
    except (OSError, ValueError, TypeError):
        return
    if not isinstance(cards, list):
        return
    patched = False
    for c in cards:
        if (
            isinstance(c, dict)
            and c.get("key") == _CARD_KEY
            and "handoff" in str(c.get("source", ""))
        ):
            c["slug"] = slug
            c["detail"] = detail
            patched = True
    if not patched:
        return
    tmp = cards_path + f".tmp.{os.getpid()}"
    try:
        with open(tmp, "wb") as f:
            f.write(json.dumps(cards, indent=2).encode("utf-8"))
        os.replace(tmp, cards_path)
    except OSError:
        with contextlib.suppress(OSError):
            os.remove(tmp)


def _repair_bookmarks_file(bm_path: str, *, publish=None) -> bool:
    """Repair v1 self-matched entries in one bookmarks file in place.

    Acquired under the cross-process file lock (non-blocking): if a live
    session currently holds it, we skip — that session reconciles its own
    file at ``session_start`` before it issues any ``bookmark()``, so a
    held lock means there is nothing here for the sweep to fix. Returns
    whether anything changed (``False`` also when skipped or clean).

    ``publish`` is an optional ``(slug, detail) -> None`` callback used to
    update the observable card for the *current* session (it should wrap
    ``ctx.put_observable`` so fir core owns the ``.cards`` write). When
    omitted — the global backlog sweep over inactive sessions — the card
    is patched directly via ``_rewrite_card_file``.
    """
    with _bookmarks_filelock(bm_path, blocking=False) as held:
        if not held:
            return False
        return _repair_bookmarks_locked(bm_path, publish=publish)


def _repair_bookmarks_locked(bm_path: str, *, publish=None) -> bool:
    """Repair body — caller must hold the bookmarks file lock.

    For each entry whose stored turn body is itself a ``bookmark`` call,
    recover the original ``quote``, re-resolve it against the sibling
    transcript with the fixed (self-excluding) scanner, and swap in the
    real turn — preserving ``_bookmark_note``. Entries that need no
    repair, or can't be re-resolved (missing transcript / quote / no
    better match), are left untouched so a note is never lost. Returns
    whether anything changed.
    """
    entries = _read_bookmarks(bm_path)
    if not entries:
        return False
    transcript = _sibling_transcript(bm_path)
    changed = False
    repaired: list[dict] = []
    for e in entries:
        if not _is_bookmark_call(e):
            repaired.append(e)
            continue
        quote = _bookmark_call_quote(e)
        if not transcript or not quote:
            repaired.append(e)
            continue
        found, _count = _find_turn_by_quote(transcript, quote)
        if found is None:
            repaired.append(e)
            continue
        fixed = dict(found)
        note = e.get("_bookmark_note")
        if note is not None:
            fixed["_bookmark_note"] = note
        repaired.append(fixed)
        changed = True
    if not changed:
        return False
    repaired.sort(key=_entry_sort_key)
    _write_bookmarks(bm_path, repaired)
    slug, detail = _render_card(bm_path, repaired)
    if publish is not None:
        with contextlib.suppress(Exception):
            publish(slug, detail)
    else:
        _rewrite_card_file(bm_path, slug, detail)
    return True


def _run_migration_once(session_file: str) -> None:
    """One-time idempotent repair of the v1 bookmarks backlog on this host.

    Takes the current session's transcript path (so it can run off the
    hot path in a background thread without touching the bridge ``ctx``).
    Sweeps every ``bookmarks-*.jsonl`` and repairs the v1 self-matched
    entries, guarded by a version marker under the config dir so the full
    pass runs once per host per migration version. No exclusive sweep
    lock is taken: per-file safety comes from the non-blocking file lock
    inside ``_repair_bookmarks_file`` (which serialises against any live
    ``bookmark()`` and skips live files), and repairs are idempotent, so
    two sessions starting at once can both sweep harmlessly — at worst a
    little duplicated work. Crucially there is no lock to leak, so a
    crash mid-sweep can never wedge the migration permanently. Files
    owned by sessions that never reopen are fixed here; sessions that do
    reopen also self-heal their own file at ``session_start`` (see
    ``on_session_start``), which covers stragglers upgraded after the
    marker is set. Best-effort throughout.
    """
    if not session_file:
        return
    root = os.path.dirname(os.path.dirname(session_file))
    if os.path.basename(root) != "sessions" or not os.path.isdir(root):
        return
    config_dir = os.path.dirname(root)
    marker = os.path.join(config_dir, _MIGRATION_MARKER)
    try:
        with open(marker) as f:
            if int(f.read().strip() or "0") >= _MIGRATION_VERSION:
                return
    except (OSError, ValueError):
        pass
    for slug in os.listdir(root):
        d = os.path.join(root, slug)
        if not os.path.isdir(d):
            continue
        try:
            names = os.listdir(d)
        except OSError:
            continue
        for name in names:
            if name.startswith("bookmarks-") and name.endswith(".jsonl"):
                with contextlib.suppress(Exception):
                    _repair_bookmarks_file(os.path.join(d, name))
    # Mark done (atomic). A concurrent sweep writing the same value is
    # harmless; the marker only short-circuits the next host start.
    tmp = marker + f".tmp.{os.getpid()}"
    try:
        with open(tmp, "w") as f:
            f.write(str(_MIGRATION_VERSION))
        os.replace(tmp, marker)
    except OSError:
        with contextlib.suppress(OSError):
            os.remove(tmp)


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
    return _write_bookmark(ctx, params.get("quote"), params.get("note"))


def _write_bookmark(ctx: fir_ext.Context, quote: str | None, note: str | None) -> dict:
    """Resolve ``quote`` to a past turn and persist it with ``note``.

    Shared by the model-facing ``bookmark`` tool and the argless
    ``pin`` reflex (whose decision branch produces quote/note pairs).
    Returns an ``_ok``/``_err`` payload.
    """
    quote = (quote or "").strip()
    note = (note or "").strip()
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

    # Critical section: append + sort + write + card publish. The
    # in-process lock serialises threads here; the file lock (blocking)
    # serialises against the repair migration running in any other
    # session's extension process. We read fresh inside both locks so
    # there is no lost update.
    with _bookmarks_lock, _bookmarks_filelock(bm_path, blocking=True):
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
    name="pin",
    description=_PIN_DESCRIPTION,
    parameters=_PIN_PARAMS,
    display_hint={"result_max_lines": 2},
)
def pin(params: dict, ctx: fir_ext.Context) -> dict:
    """Argless reflex: delegate the real decision to a side-query branch.

    The branch sees full session context and returns a list of
    ``quote ||| note`` lines (or SKIP). We write each resolved pair via
    the same path as the ``bookmark`` tool. Returns instantly with a
    one-line summary; never errors back to the model (pinning is a free
    reflex — failures are swallowed, not surfaced as friction).
    """
    try:
        # effort="off": the branch is mechanical quote-extraction, not
        # reasoning. It also sidesteps a 400 when the session has extended
        # thinking enabled (side_query would inherit a budget_tokens < 1024).
        out = ctx.side_query(_PIN_BRANCH_PROMPT, timeout=120, effort="off")
    except Exception as exc:  # pragma: no cover - defensive
        return _ok(f"📌 noted (branch unavailable: {exc})")

    pairs = _parse_pin_branch(out)
    if not pairs:
        return _ok("📌 noted (nothing to bookmark)")

    written = 0
    for quote, note in pairs:
        res = _write_bookmark(ctx, quote, note)
        if not res.get("is_error"):
            written += 1

    if written == 0:
        return _ok(f"📌 noted ({len(pairs)} candidate(s), none anchored)")
    return _ok(f"📌 bookmarked {written} turn(s)")


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
            "self_handoff is not available in this session's mode "
            "(it requires session-restart support, e.g. interactive or "
            f"ACP). (restart_session: {exc})"
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


# ---------------------------------------------------------------------------
# Lifecycle: one-time bookmarks repair migration
# ---------------------------------------------------------------------------


def _sweep_worker(session_file: str) -> None:
    """Background-thread entry point for the global backlog sweep."""
    with contextlib.suppress(Exception):
        _run_migration_once(session_file)


@fir_ext.on("session_start")
def on_session_start(params: Any, ctx: fir_ext.Context) -> None:
    """Repair bookmarks on session start (incl. /reexec — same session id).

    Two best-effort steps, neither of which may raise into session start:

    1. Self-heal THIS session's own bookmarks file (inline — one file).
       ``/reexec`` continues the same session (same id + transcript) and
       re-emits ``session_start``, so a session upgraded to the fixed
       code repairs its own pre-fix entries here — even after the
       one-time global marker is set — and does so *before* it issues any
       ``bookmark()``, keeping its file clean for the rest of its life.
       The card is republished via ``ctx.put_observable`` so fir core
       owns the ``.cards`` write (no race against the observable store).
    2. Run the one-time global backlog sweep (marker-gated) for files
       owned by sessions that will never reopen — off the hot path in a
       daemon thread so a large backlog never delays session start. The
       thread takes only the captured session-file path, not ``ctx``, so
       it never touches the bridge from a background thread.

    See ``_run_migration_once`` and the migration block above.
    """
    del params
    session_file = ""
    with contextlib.suppress(Exception):
        session_file = ctx.get_session_file()
    with contextlib.suppress(Exception):
        bm = _bookmarks_path(ctx)
        if bm and os.path.exists(bm):
            _repair_bookmarks_file(
                bm,
                publish=lambda slug, detail: ctx.put_observable(_CARD_KEY, slug, detail),
            )
    if session_file:
        threading.Thread(
            target=_sweep_worker,
            args=(session_file,),
            name="handoff-bookmarks-migration",
            daemon=True,
        ).start()


fir_ext.run(name="handoff")
