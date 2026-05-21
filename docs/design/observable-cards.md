# Observable Cards

## Motivation

Extensions (and core) accumulate per-session state worth surfacing to humans and sibling agents — mood tags, plan progress, model identity, activity counters. Today this surfaces ad-hoc: ctx.set_status shoves a string at the TUI footer, observe.py maintains its own sidecar of activity, mood logs live in session_data and have no presence in observe_session output. Live vs at-rest readers take different paths. Extensions can't see each other's state without coupling.

Observable cards unify these into a single primitive owned by the session store.

## Invariants

1. Producer renders, consumer concatenates. No render callback, no liveness coupling, no cross-extension imports. Once written, cards are dumb data.
2. Sidecar IS canonical. Live read and at-rest read are the same syscall on the same file.
3. Host stamps trust. Source is stamped by the host from the calling extension's name (or set by core directly). Extensions cannot spoof source.
4. Every observable state change traces to a transcript entry. Cards carry an entry_id. Cards summarise; transcript is ground truth.
5. One write surface. ctx.set_status is reimplemented as a thin wrapper over put_observable. No parallel paths.
6. No cross-extension imports. Checkable: rg "import.*\.fir\.extensions\." pkg/resources/builtin_extensions/ is empty forever.

## Data model

```go
type Card struct {
    Source   string    // stamped by host
    Key      string    // producer-chosen, namespaced within source
    Slug     string    // ≤24 chars, host-rendered headers
    Detail   string    // pre-rendered plain text
    Ts       time.Time // stamped by host
    EntryID  string    // transcript entry id, stamped by host
}
```

Six fields. Each answers a reader question today: Source/Key for addressing, Slug/Detail for the two required representations, Ts for when, EntryID for provenance.

Explicitly NOT in MVP: visibility, ttl_turns, ttl_seconds, write_turn, structured. Speculative fields are schema rot. Reintroduce only when a real use case lands.

## API surface

Three calls. Three only.

In-process (core + tests):

```go
ObservableStore.Put(source, key, slug, detail, entryID string)
ObservableStore.Clear(source, key string)
ObservableStore.List() []Card
```

Extension SDK (Python):

```python
ctx.put_observable(key, slug, detail)
ctx.clear_observable(key)
```

Extensions cannot READ cards in v1 — keeps the abstraction one-directional. Reading is for observers, not producers.

The wire RPC is a four-line wrapper over in-process Put. In bridge.go inbound handler for put_observable: call b.store.Put(b.caps.Name, p.Key, p.Slug, p.Detail, b.currentEntryID()). Source and entryID are stamped, never read from payload.

This is the entire trust seam. RPC writes get stamped; direct in-process callers pick their own source. Both call Store.Put.

## Ownership

Storage lives in pkg/session/store/observables.go. The session store is the only thing that already outlives bridges across /reexec, already owns atomic temp+rename infrastructure (WriteReexecSidecar pattern), and already owns transcript-sibling files.

The bridge holds a pointer; the manager doesn't store anything. The manager's only responsibility is the trust seam: stamp source from b.caps.Name in the RPC handler, reject extensions whose name collides with reserved core sources at startOne.

Reserved source names (extensions cannot claim these): plan, model, session.

## File layout

```text
<sessionFile>.jsonl          (transcript, existing)
<sessionFile>.jsonl.reexec   (reexec sidecar, existing, consumed-and-deleted)
<sessionFile>.jsonl.cards    (NEW)
```

Observe's discovery sidecar gains one field: cards_path. Readers follow the pointer. Live and at-rest readers take the same path.

Write strategy: sync atomic temp+rename on every Put. No debounce in MVP. If real sessions show write-amp pain, add a 500ms debounce later as a single localised change. Caps and TTL stay deferred indefinitely.

## Reexec story

Cards file is read on session construct → footer/headers populated from last-known state before any bridge handshakes complete → bridges spawn → producers re-Put on their first relevant event → stale entries get overwritten naturally. No explicit restore code in the store. Reexec falls out for free.

## Producers in MVP

Mood extension publishes on mood_note: ctx.put_observable("current", tag or "noted", note). Also publishes ("footer", tag, note) to take over from the existing set_status path. Mood log entries persisted in session_data gain a parallel entry_id field for cross-reference.

Plan tool publishes on every plan mutation, INSIDE THE TOOL'S Execute (not from agentsession event dispatch). Slug comes from plan metadata progress_metric or falls back to "done/total status". Detail is the rendered bullet listing. Whoever owns the state owns its card.

set_status becomes a thin wrapper: b.store.Put(b.caps.Name, "footer", p.Status, "", b.currentEntryID()). The race we just fixed in set_status stays fixed — Put is one atomic operation through the store.

## Reader in MVP

observe_session prepends a one-line header derived from card slugs, ordered by (source-priority, recency desc):

```text
mood: #engaged  ·  plan: 3/8 in_progress  ·  …+1 more (--ext)
```

Flags: --ext <name> expands detail for one source; --raw_json includes the full cards array alongside transcript lines. Truncate with …. Producers don't optimise width they don't control.

## Provenance — the entry_id link

Every transcript entry already has a stable id and parentId (visible in the JSONL). Card.EntryID holds the id of the transcript entry whose execution caused the Put: tool_call_id for tool-driven Puts, event-trigger entry id for event-driven Puts with a clear trigger, empty string when there's no clear anchor (consumers fall back to Ts).

The host stamps EntryID from the in-flight dispatch context — extensions cannot spoof it. Same pattern as source.

What this enables: audit (jump from any card to the exact transcript line that caused it), bookmarks (observe_session <id> --since-card plan/active), debug (find the producing tool call without grep-archaeology).

Card→entry is the only viable direction. Entries are append-only, cards are mutable; the link never rots.

The deeper invariant: every observable state change has a traceable cause in the transcript. A card with empty EntryID and no clear non-tool origin is a bug — drift outside the dispatched-event spine is what bites you in /reexec.

## Checkable invariants

After landing:

```sh
rg "import.*\.fir\.extensions\." pkg/resources/builtin_extensions/   # empty
rg '"plan"|"mood"|"footer"' pkg/session/store/observables.go         # empty
```

If a future PR breaks either grep, the abstraction has leaked and the PR fails review.

## What this unlocks

The non-obvious win: plan progress visible across sibling agents via observe_session. Watching another agent tick 3/8 → 4/8 live, without parsing their transcript, is real multi-agent observability falling out for free.

## Out of scope (v1)

TTL (turn-based or wall-clock); per-source caps / LRU; debounced async flush; visibility levels; structured payloads; markup language / theming in detail; activity / usage / model migration from observe's sidecar; extension-side reading of other extensions' cards.

Each can return as a focused follow-up if a real use case lands. The MVP spine is locked.

## Implementation plan

Five core files:

1. pkg/session/store/observables.go — ObservableStore, Card, file IO.
2. pkg/session/store/session.go — Session.Observables() field, constructed in NewSession.
3. pkg/extension/bridge.go — two RPC handlers (put_observable, clear_observable; List stays in-process per the one-directional rule above); stamp source + entry_id; set_status reimplemented.
4. pkg/extension/manager.go — reserved-name rejection in startOne.
5. Plan tool's Execute (location TBD: cmd/fir/ or pkg/builtin_tools/plan/) — call session.Observables().Put("plan", ...).

Plus:
6. pkg/resources/builtin_extensions/mood.py — call ctx.put_observable in mood_note; add entry_id to mood log entries.
7. pkg/resources/builtin_extensions/observe.py — read cards file via cards_path; prepend header in _snapshot_transcript; --ext and --raw_json support.
8. Tests:
   - synthetic ext writes a card → file contains it → observe surfaces slug
   - extension named "plan" fails to load with clear error
   - reexec re-shows last-known state pre-handshake
   - spoofed source / entry_id in Put payload is ignored
   - every Put from a tool-call dispatch context has non-empty EntryID
   - set_status end-to-end (the race we just fixed) still works through the new path
