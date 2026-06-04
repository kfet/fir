# Handoff Bookmarks

## Motivation

`self_handoff(content)` and `/handoff` restart the session with a curated
briefing prepended. The briefing is lossy: the model has to re-author
what mattered from the entire prior context, exactly when it is most
overloaded. Worse, the briefing is *one shot* — anything the model
forgets to include is gone for the child.

Bookmarks fix this by letting the model pin significant turns *as they
happen*. The child session inherits a chronological highlight reel of
turns the parent flagged as worth keeping — without any cooperation
from the model at the moment of handoff.

## Core principle

The transformer is uniquely equipped to know what matters in its own
context, at any time. Give it one tool, get out of the way.

- No auto-bookmarking. If the model didn't pin it, it doesn't matter.
- No lazy child-prep. fir starts instantly.
- No range / multi-message APIs. One quote, one note, done.

## The tool

```text
bookmark(quote: string, note: string)
```

- **quote** — minimum exact text needed to uniquely identify a past
  turn. Can be from any turn type: user message, assistant message,
  tool call args, tool result, system message. The tool description
  says this explicitly so the model knows the search is unscoped.
- **note** — short label explaining why this is significant
  (`final DB schema`, `user constraint: no auth`).

Returns the bookmarked turn's `id` and `timestamp` on success; a tool
error if the quote matches nothing.

## Runtime behaviour

1. Reverse-scan the session JSONL (the path returned by
   `ctx.get_session_file()`) line by line, latest first.
2. Decode each line as JSON and substring-match `quote` against any
   string leaf in the decoded object. Decoded matching is the right
   substrate because the model writes `quote` from what it sees in
   its context, which is the decoded turn content — JSON escapes like
   `\n`, `\u003c`, `\u0026` don't matter.
3. The first hit wins (most recent match). Keep counting the rest of
   the file to surface a match count in the tool result. **The
   assistant turn that invokes `bookmark()` is itself skipped**
   (`_is_bookmark_call`): it is persisted to the transcript before this
   handler reverse-scans, with `quote` verbatim in its tool-call
   arguments, so without this guard every quote self-matches the newest
   entry — the call — instead of the earlier turn. Bookmarking a
   bookmark is never meaningful.
4. Duplicate the matched entry, inject `_bookmark_note`, append to
   `bookmarks-<session-id>.jsonl` (co-located with the session file).
5. Re-sort the bookmarks file by the entry's original `timestamp`
   field so the file reads as a chronological highlight reel, not
   bookmark-call order. The model pins retroactively; the file
   reflects when the *original* turn happened.
6. Ambiguous quote: most recent wins, count surfaced. No matches:
   tool error. Critical section under a Python `threading.Lock`
   covers read + sort + atomic temp+rename write + card publish.

## Storage

```text
<sessionDir>/<fileTs>_<sessionID>.jsonl              session transcript
<sessionDir>/<fileTs>_<sessionID>.jsonl.cards        observable cards
<sessionDir>/bookmarks-<sessionID>.jsonl             NEW
```

Bookmarks file format — one JSON object per line, sorted by the
entry's `timestamp`:

```jsonl
{"type": "message", "id": "...", "timestamp": "...", "message": {...}, "_bookmark_note": "final DB schema"}
```

Each line is the *entire transcript entry* as-is, plus the injected
`_bookmark_note` key. Schema drift is impossible — if the transcript
format gains a field, bookmarks inherit it for free.

The file is the source of truth. The observable card is a derived
projection (extensions cannot READ cards in v1 — see
[observable-cards.md](observable-cards.md)).

## Observable card

Written on every `bookmark()` call, inside the same critical section
as the file write.

We do **not** subscribe to `session_start` to "reconcile" the card
from the file. The cards file is read on session construct (see
[observable-cards.md](observable-cards.md)) so the previous card
survives `/reexec` for free. Adding a reconciler would only stamp
the same value the cards file already supplied — pointless work.

NOT written on `agent_end` (nothing changed) or `self_handoff` (the
card is always current after each `bookmark()` write).

```text
source:   handoff
key:      bookmarks
slug:     5 pinned                          (≤24 chars)
detail:   5 bookmarks (/abs/path/to/bookmarks-<id>.jsonl):
          - 14:32  final DB schema
          - 14:45  skip auth for MVP
          - …
entry_id: <tool_call_id>
```

Including the absolute path in `detail` means
`observe_session <id> --ext handoff` tells the reader exactly where to
read the heavy payload.

## Child session integration

When `self_handoff` calls `restart_session(..., prepend_context=...)`,
the extension appends a pointer line to the briefing if the bookmarks
file exists and has non-zero size (a single `os.stat` — no parse):

```text
Bookmarks from parent session: /abs/path/to/bookmarks-<parent-id>.jsonl
— chronological highlight reel of turns the previous agent pinned as
significant. Read before proceeding.
```

The child uses standard `read` / `grep` tools on that file. No new
child-side tools needed. The bookmarks file is just a sibling on disk.
The child also sees the parent's `handoff/bookmarks` card via the
cards-file-read-on-session-construct mechanism (see
[observable-cards.md](observable-cards.md)).

A handoff with no bookmarks looks exactly as it does today.

## What this does not do

Out of scope for this iteration:

- Auto-triggering handoff at 98% context / on `ContextLengthExceeded`.
  A separate follow-up. Manual `/handoff` and `self_handoff` continue
  to be the only triggers.
- Range bookmarks (pin a span of turns). One quote, one note. If the
  model wants a range, it makes multiple bookmark calls.
- Bookmark editing / deletion. The transcript is append-only; bookmarks
  are append-only. If the model regrets a bookmark, it makes another.
- Cross-session bookmark queries. Bookmarks belong to the session that
  wrote them. The child inherits them by path, not by a query API.

## Invariants

- **The .jsonl file is the source of truth.** The card is a derived
  projection; if they disagree, the file wins.
- **One write surface.** `bookmark()` is the only tool that mutates
  the file. No `session_data`, no reconcile-on-event.
- **Schema-resilient.** Bookmark entries are full transcript entries
  with `_bookmark_note` injected. No remapping, no flattening.
- **Most recent wins on ambiguity.** Predictable for the model;
  matches how the model usually wants to pin "the latest version".
- **Sorted by original timestamp.** The file reads chronologically
  regardless of bookmark-call order.

## Migration (v1 → v2)

Bookmarks written before the `_is_bookmark_call` guard stored the
bookmark-call turn instead of the substantive turn (every quote
self-matched the call). A one-time, idempotent migration repairs them:

- Triggered from a `session_start` handler — after a binary upgrade /
  reexec the fixed code is present, and the next session start runs the
  repair. `/reexec` continues the *same* session (same id + transcript)
  and re-emits `session_start`.
- Two parts per start: (1) the session **self-heals its own file**
  before it issues any `bookmark()`, so any session that reaches the
  fixed code — including ones upgraded *after* the global marker is set
  — repairs its own pre-fix entries; (2) a **one-time global backlog
  sweep**, gated by a version marker (`.handoff-bookmarks-migration`
  under the config dir), fixes files of sessions that will never reopen.
- **Concurrency.** `bookmark()` and the repair share a per-file advisory
  lock on an `flock` sidecar (`<bm>.lock`, a stable inode — the
  bookmarks file itself is replaced via atomic temp+rename). The live
  writer takes it blocking; the sweep takes it non-blocking and *skips*
  a file a live session holds (that session already self-healed its file
  at start, so there is nothing to fix). Both read fresh inside the
  lock, so there are no cross-process lost updates. No host-wide sweep
  lock is taken — concurrent session starts may both sweep harmlessly
  (idempotent), and a crash mid-sweep can never wedge the migration.
- For each `bookmarks-<sid>.jsonl`, every entry whose body is itself a
  bookmark call is repaired: the original `quote` is recovered from the
  call's arguments and re-resolved against the sibling transcript with
  the fixed scanner; the real turn replaces the entry, `_bookmark_note`
  preserved. Entries that can't be re-resolved (missing transcript /
  quote / no better match) are left untouched — a note is never lost.
- The derived `handoff/bookmarks` card in the persisted `.cards` file is
  re-rendered in place, since old (inactive) sessions never republish.
- Idempotent: a repaired entry is no longer a bookmark call, so a
  re-run finds nothing to fix.
