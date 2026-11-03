# Session stores

fir keeps sessions in one writable store, `<agent-dir>/sessions/`
(label `local`). A fleet archives every host's sessions directory elsewhere
(e.g. `~/fir-traces/<host>/`, synced). `sessionStores` lets fir read those
archives without ever writing to them.

## Configuration

- Global `settings.json` → `"sessionStores": [{"path", "label", "readOnly"}]`.
  Project settings cannot add stores: a repository must not widen the ACP
  resume whitelist. A project entry produces a warning.
- `FIR_SESSION_STORES=label=path[,label=path]` replaces the setting (set to
  empty = no extra stores).
- `~` is expanded; paths must be absolute. Labels: `[A-Za-z0-9._-]+`,
  unique, not `local`. Duplicate paths dropped. Missing directories are
  kept and listed as `exists=false` (a sync mount may appear later).
- Extra stores are always read-only. A writable second store would need a
  rule for where new sessions go and how handles/state follow — no use case.

`cmd/fir` resolves the set once (`config.LoadSessionStores`) and registers
it with `store.ConfigureStores(defaultRoot, extras)` before any subcommand
or mode runs. Unconfigured (tests, embedders), there are no extras and
every path is writable.

## Catalog (`store.Catalog`)

Lists sessions across stores, newest first; each `SessionListInfo` carries
`Store` (label) and `ReadOnly`. Key = `<store>:<id>` — one id may live in
two stores.

- `local`: the existing `ListSessions` path (meta cache, slow-path parse).
- read-only: file names + `<transcript>.meta.json` only — never a
  transcript body, never a write (archives hold ~10k transcripts).
  Skipped: no `.meta.json`, or mtime within `MidWriteAge` (1 min, mid-sync).
  Empty cwd → decoded from the dir name (lossy). Parent paths recorded on
  the origin host are remapped into the store when the copy exists.

## Read-only enforcement (in `pkg/session/store`, not callers)

- `setSessionFile` on a path inside a read-only store forks it (`ForkFrom`)
  into a writable dir *before* locking (the flock creates `.meta.json`):
  `ss.sessionDir` when writable, else the local store's dir for the cwd.
  `ForkedFromStore()`/`ForkNotice()` tell the UI why it forked. If no
  writable dir exists the session opens in memory (`persist=false`).
- `checkWritable` guards every write in the package: transcript create /
  append / rewrite, branch + fork targets, meta sidecar, flock, trailing
  newline repair, state history (append, atomic write, legacy migration),
  observable cards, reexec sidecar, mkdir of session dirs.

## Consumers

| Consumer | Behaviour |
|---|---|
| `fir sessions list/dirs/search` | TSV / JSON lines; search = bookmarks then transcripts, newest first, ordered parallel grep |
| `ResolveSessionRef` (`--resume`) | local first, then each extra store; `label:id` picks one store |
| `/resume` (TUI, ACP command) | this project's archive sessions merged in, labelled |
| ACP `session/list` | extras included; title `[label] …`, `_meta.fir/store` |
| ACP `session/resume`/`load` | `isValidSessionPath` accepts paths in any configured store; UUID lookup searches extras last; opening forks |
| `observe` extension | `all=true` adds newest 20 archived rows; an unknown id falls back to the archive (via `$FIR_BIN sessions list`) |
| `recall` skill | `fir sessions` for enumeration and search |

Never: `session/new`, state sidecars, bookmarks, ctx logs, compaction
output — all follow the (writable) current session file.
