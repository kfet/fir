# Session restore

Every fir session, in every mode (TUI, print, json/rpc, ACP), saves its
settings next to its transcript and gets them back through one core path.

```
 <agentDir>/sessions/<cwd>/<ts>_<id>.jsonl              transcript (messages, model/thinking/name entries)
 <agentDir>/sessions/<cwd>/<ts>_<id>.jsonl.state.jsonl  session state history, 0600, append-only (this doc)
 <agentDir>/sessions/<cwd>/<ts>_<id>.jsonl.meta.json    listing cache + flock target (0644, rebuilt freely)
 <agentDir>/session-handles/<sha256(handle)[:16]>       handle → transcript path (e.g. ACP sessionId)
```

The state lives in its own sibling file, not in `.meta.json`: that file is a
world-readable, mtime-invalidated listing cache that is rewritten freely and is
the flock target, while state is authoritative and holds secrets (MCP `env`),
so it is created mode 0600. Logs name MCP servers only.

## History file format

`<transcript>.state.jsonl` is append-only JSONL (`pkg/session/store/state.go`).
Each line is a full snapshot:

```json
{"ts":"2026-10-01T12:00:00.123456789Z","state":{"runtime":{...},"conversation":{...}}}
```

- A line is appended (`O_APPEND`, then `fsync`) only when the snapshot differs
  from the last line; a no-op save writes nothing. `reason` is optional
  (`"migrate"` marks a migrated snapshot).
- Restore uses the last line that decodes. A torn/truncated final line is
  skipped, and the next append starts on a fresh line.
- Growth is capped: once the file exceeds 1000 lines (`MaxStateHistory`) it is
  atomically rewritten (temp file + fsync + rename) keeping the newest 1000.
  Snapshots only change on settings changes, so this is years of history.
- Migration: a legacy `<transcript>.state.json` (fir ≤ 1.24.1) with no history
  file is written as the first line (ts = its mtime) and deleted on first read.
- A fork starts a new history whose first line is the source's latest state.

## What is saved

`session.SessionState` (`pkg/session/state.go`) is marshalled whole, so a
field added to it is saved and restored with no other change
(`TestSessionState_AnyFieldSurvivesReopen` proves it by reflection).

| Half | Fields | On an in-process switch (`/resume`, `/new`, fork) |
|---|---|---|
| `runtime` | cwd, session-scoped `mcpServers` (client/runtime-added), `meta`, `mode`, `handle` | kept — it belongs to the running session |
| `conversation` | model (`provider/id`), thinking level, name | adopted from the target transcript |

Saved on: open, switch, `/new`, `SetModel`, `SetThinkingLevel`,
`SetSessionName`, `UpdateSessionState`, end of every turn. Nothing is saved
after `Close`, so a late save cannot revive a forgotten session. A fork copies
the source's latest state.

## How it is restored

```
 TUI/print -c, --session, ── store opened ──┐
 ACP new/load/resume/rehydrate              ├─▶ session.Setup ─▶ CreateAgentSession
                                            │      1. load <transcript>.state.jsonll (last valid line)
          StateOverride (e.g. ACP client) ──┘      2. apply override
                                                   3. model/thinking/name → agent
                                                      (unless pinned by --model/--thinking)
                                                   4. start config MCP + runtime.mcpServers
                                                   5. save, bind handle
 TUI/ACP /resume, /continue ─▶ SwitchSession ─▶ same restoreState, runtime kept
```

Opening a transcript in any mode restarts its saved session-scoped MCP servers
(with their saved env) — the session comes back as it was. The handle is the
exception: it is dropped on open and only its owner re-claims it (ACP via the
override), so `fir --session` on an ACP transcript cannot steal its
`sessionId`, and switching (`/resume`) onto a transcript another owner holds
by handle forks it first, so the owner's saved runtime is never overwritten.

On `fir -c` the saved model/thinking beat the start-up `--model`/`--thinking`
recorded in the session header (the session resumes as it was left); flags on
the current command line still win.

## ACP glue

ACP keeps only protocol behaviour (`pkg/modes/acp/restore.go`):

- The ACP `sessionId` is the session's **handle**; core points it at the
  current transcript on every save, so a prompt on a reaped `sessionId` —
  even after a fir restart — rebuilds the session in place.
- `session/new`/`load`/`resume` values (cwd, `mcpServers`, `_meta`) override
  the saved ones via `StateOverride` and are saved in turn.
- If a restored client MCP server fails to start, the prompt returns `-32010`
  with `data.reason: "session_needs_reload"` and the failed server names; the
  client recovers with `session/load` and fresh `mcpServers`.
- `session/release`/`close` forget the handle; handles untouched for 30 days
  are pruned.
- Legacy v1.24.0 `<agentDir>/acp-sessions/<hash>.json` configs are migrated to
  state + handle on first use of their `sessionId` and deleted; leftovers age
  out after 30 days.
