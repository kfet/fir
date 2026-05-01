# Design: session observation

**Status**: design, ready to implement
**Branch**: `tmux-attach-pty`

**Scope:** any fir session that writes a transcript — interactive, ACP, or
print mode. The mechanism is a per-session extension; extensions run in all
modes (`extension.Setup` is called from `cmd/fir/app.go`,
`pkg/modes/acp/acp.go`, etc.), so observation is mode-agnostic.

## Problem

A running fir session — whether headless ACP, headless print, or
interactive — is opaque to outside observers. A second human wanting to
check on a session, or another fir agent debugging one, has no way in.

ACP and print modes are the most affected (no TUI at all), but the same
mechanism works for interactive mode for use cases like recording,
post-mortem replay of past `fir -p` invocations, and side-by-side debugging.

## What already exists

Two things in fir do almost all the work:

1. **`SessionStore` writes a JSONL transcript per session** at
   `<agentDir>/<sessionDir>/<timestamp>_<uuid>.jsonl`. Every message and
   tool call is appended. ACP mode wires it up in `createSession`
   (`pkg/modes/acp/acp.go:223`). The file exists whether anyone observes or
   not. **The file is the buffer.**

2. **Extensions are per-session subprocesses with stdio JSON-RPC into fir.**
   They get `session_start` / `session_shutdown`, and can call
   `send_user_message` back into fir.

The OS and filesystem handle: buffering, replay, live tail, fan-out,
post-mortem persistence, surviving crashes, surviving reboots, multi-reader
concurrency. **We do not need to build any of that.**

## Architecture

A builtin extension `observe.py` does two things per session:

1. **Writes a sidecar** at
   `$XDG_STATE_HOME/fir/agents/<session-id>.json` describing how to
   reach the session.
2. **Listens on a Unix socket** at
   `<runtime-dir>/fir/observe/<session-id-prefix>.sock` (mode 0600).
   `<runtime-dir>` resolves through `$FIR_OBSERVE_DIR` →
   `$XDG_RUNTIME_DIR` → `$TMPDIR` → `/tmp`. See "Socket" section for
   why-not the session store dir. On accepted lines (NDJSON), it calls
   `send_user_message`.

Observers (human CLI or agent script):

1. Resolve the session id by reading sidecars from
   `$XDG_STATE_HOME/fir/agents/`.
2. `tail -n +1 -F <store_path>` — that's the entire history + live stream.
3. For interactive mode, also connect the socket and forward stdin lines.

The whole observer is conceptually a shell pipeline. A 30-line bash version
works. `fir observe` is that pipeline in Go with a formatter,
prefix-matching, and a clean detach.

```
sidecar (JSON)        socket (NDJSON)         transcript (JSONL)
─────────────────     ───────────────         ─────────────────
discovery/state       input back to fir       history + live
$XDG_STATE_HOME       $XDG_RUNTIME_DIR        SessionStore-managed
persistent            ephemeral               persistent
```

## Mode interactions

Observation works in all three fir modes. Caveats per mode:

| Mode | Live observe | Live send (`--interact`) | Post-mortem |
|---|---|---|---|
| **ACP** | primary use case | primary use case | yes |
| **interactive** | works (e.g. recording) | confusing — typed input from `fir send` races with the user's TTY input; same agent, two streams | yes |
| **print** | sessions are short; usually missed | same race issue; usually pointless given session length | yes — read transcript after `fir -p` exits |

The sidecar records `mode` so the CLI can warn before `--interact` against
an interactive session. We don't block it (composable Unix semantics) but
we make sure the user knows what they're doing.

Print-mode post-mortem is a real use case: `fir -p "do thing"` from a
script, then `fir observe --cwd <project> <id-prefix>` to inspect what the
agent did. Falls out of the design as-is.

## Sidecar — discovery and state

**Why a sidecar per session, not a fir-level registry?** A registry (single
file, SQLite, daemon, anything centralised) imports problems sidecars
don't have:

| Concern | Sidecar | Registry |
|---|---|---|
| N concurrent fir-acp writers (the common case — one per IDE workspace) | each writes its own file, zero contention | needs file lock / DB / daemon coordination |
| Crash blast radius | partial sidecar = one session shows `crashed`; others unaffected | corrupted registry = all sessions invisible |
| Schema migration | per-session `"schema": N` fields, mixed versions coexist | single global schema; bumping requires whole-file rewrite |
| Post-mortem (process dead, observer wants history) | file persists naturally | core is gone; in-memory state lost |
| Cleanup | per-session, independent | requires GC / compaction logic |
| Bash `fir sessions` equivalent | `ls + cat` | parse format, possibly hold lock |

The strongest pro-registry argument — "core already has
`firAgent.sessions` in memory, just expose it" — fails because (a) it
puts discovery back into core which we just removed it from, and (b) it
can't answer post-mortem queries by definition. Once on-disk state is
required for post-mortem, the registry adds nothing the sidecar doesn't
do more cleanly.

Path: `$XDG_STATE_HOME/fir/agents/<session-id>.json` (XDG default
`~/.local/state/fir/agents/<id>.json`).

Contents (atomic write — `tmp` + `rename`, never partial):

```json
{
  "schema":       1,
  "session_id":   "7f3a8b2c-...",
  "pid":          12345,
  "mode":         "acp",
  "socket_path":  "/run/user/1000/fir/observe/7f3a8b2c1234abcd.sock",
  "store_path":   "/Users/x/.fir/sessions/.../2026-04-27T...jsonl",
  "cwd":          "/path/to/project",
  "started_at":   "2026-04-27T12:34:56Z",
  "status":       "running",
  "session_name": "my-feature"
}
```

Why each field:
- `cwd` — UUID-free disambiguation. `fir observe --cwd $PWD` works.
- `started_at` — sort newest-first.
- `pid` — `kill -0` liveness check without touching the socket.
- `mode` — `acp`/`interactive`/`print`; CLI warns before `send --interact`
  against an interactive session.
- `status` — UX hint (`running`/`idle`/`ended`/`crashed`); not load-bearing
  for liveness.
- `session_name` — set on `session_named`.
- `schema` — sidecars outlive the fir version that wrote them; bake it in.

**Lifecycle:**

| Event | Action |
|---|---|
| `session_start` | Write sidecar, status=`running`. |
| `session_named`, `agent_start`, `agent_end` | Update name/status atomically. |
| `session_shutdown` | status=`ended`. **Do not unlink** — post-mortem. |

**Cleanup of stale sidecars:**

- On `fir sessions` read: if `status != ended` AND `kill -0 pid` fails AND
  socket connect fails → display as `crashed`.
- Auto-prune `ended` sidecars older than 7 days on every `fir sessions`
  read. Users won't run a manual command.
- A new fir-acp on startup reaps any sidecar whose `pid` is dead and whose
  socket is gone (could be a previous crashed run of itself).

## Socket — input back to fir

Path: `<runtime-dir>/fir/observe/<session-id-prefix>.sock`, mode 0600,
inside a parent dir at mode 0700.

`<runtime-dir>` is the first defined of:

| Env var / fallback | Platform | Notes |
|---|---|---|
| `$FIR_OBSERVE_DIR` | any | escape hatch for path-length / unusual setups |
| `$XDG_RUNTIME_DIR` | Linux desktop | typically `/run/user/<uid>` (tmpfs, mode 0700) |
| `$TMPDIR` | macOS, BSD | typically `/var/folders/.../T` (per-user) |
| `/tmp` | last resort | mkdir `fir/observe` mode 0700 |

`<session-id-prefix>` is the **first 16 chars** of the ACP session UUID.
Why not the full UUID: Unix-domain socket paths are capped at ~104 bytes on
macOS / ~108 on Linux, and `$TMPDIR` paths on macOS are already ~50 chars
before we add anything. A full UUID + suffix exceeds the cap. Sixteen hex
chars (~64 bits) of UUID space is sufficient — collisions across concurrent
sessions of one user are astronomically improbable. Observers always read
the *absolute* `socket_path` from the sidecar; they never reconstruct it,
so the prefix shortening is invisible to clients.

**Why not `<session-store-dir>/observe.sock`?** That was the obvious-looking
choice. Rejected because:

1. The session store dir is mode 0755 (`pkg/session/store/session.go:1406`).
   World-readable directory listings would expose other users' running
   sessions as a sidechannel.
2. Sessions can fork (`ForkFrom`) and change their on-disk file mid-life;
   the socket should be bound to the stable ACP session id, not to a
   particular `.jsonl`.
3. Lifetimes are wrong: session files are persistent records, sockets are
   ephemeral process-lifetime artifacts. Mixing them clutters a persistent
   data dir with stale sockets after every crash.
4. `agentDir` may live on NFS / network filesystems where Unix-domain
   sockets do not work; `$XDG_RUNTIME_DIR` is guaranteed local (tmpfs).

Wire: **NDJSON, one direction, client → server**. One JSON object per line:

```json
{"deliver_as": "",         "content": "what's the current plan?"}
{"deliver_as": "steer",    "content": "stop, read foo.go first"}
{"deliver_as": "followUp", "content": "also update the changelog"}
```

`deliver_as` maps directly to `SendUserMessage` semantics already in
`pkg/extension/session_bridge.go`:

| `deliver_as` | Effect |
|---|---|
| `""` (or omitted) | `session.Prompt(content)` — fresh turn (queues if mid-turn) |
| `"steer"` | `Agent.Steer(msg)` — interrupt current turn |
| `"followUp"` | `Agent.FollowUp(msg)` — queue for after this turn |

The extension reads each NDJSON line and calls
`ctx.send_user_message(content, deliver_as=...)`. ~50 LoC.

Server never writes back. The transcript file is the response channel.

## Observer CLI

Two primitives plus one convenience. No unified TUI — window management
belongs to the user's window manager (tmux, terminal app), not to us.

```
fir observe                        # list live sessions (no args)
fir observe <id-prefix>            # tail -F transcript with formatter
fir observe <id-prefix> --json     # raw JSONL
fir observe <id-prefix> --cwd .    # resolve by cwd (error if 0/many)
fir send    <id-prefix>            # cooked-mode stdin → socket
fir send    <id-prefix> --steer    # default deliver_as for typed lines
fir observe <id-prefix> --interact # convenience: tail + send in one process
```

**Relationship to `fir sessions`:** the existing `fir sessions` lists
transcript files on disk for the current cwd ("project history"). `fir
observe` (no args) lists *currently-running* sessions across all cwds
("what's alive right now"). Different scopes, different mental models —
keep them separate. A historical transcript that has no live process gets
listed by `fir sessions` (cwd-scoped) and is observable post-mortem via
`fir observe <id>` (the sidecar still points at its file).

**Why two commands instead of one with `--interact`?**

- Single-responsibility, pipe-friendly. `echo … | fir send X` and
  `fir observe X --json > log` work without flags.
- For serious use (debugging a stuck session) the right shape is two
  tmux panes: one running `observe`, one running `send`. Composes with
  the user's existing window manager — splits, copy mode, search, status
  line all already there.
- Plain-terminal users get the convenience flag; documented limitation
  is that output and input interleave on a single TTY.
- `fir send` works as a stdin filter for scripts: cron jobs nudging
  agents, completion-hooks, etc.

Resolution: prefix-match against `(session_id, session_name, basename(cwd))`.
Git-style ambiguity error.

`fir sessions` output (newest first):

```
ID        NAME           CWD              STATUS    AGE
7f3a8b2c  my-feature     fir              running   2m14s
4d2e91a0  -              other-project    idle      0m03s
8c1bff90  refactor       legacy-app       ended     2h17m
```

`fir send` connection greeting:

```
Connected to session 7f3a8b2c (my-feature). Enter to send. Ctrl-\ to disconnect.
  !  prefix → steer (interrupt current turn)
  +  prefix → followUp (queue for after current turn)
  \  prefix → escape literal !/+
> _
```

## Observer behaviour

**Tail mode (default, no `--interact`):**

```
fd, _ := os.Open(store_path)
buf := bufio.NewReader(fd)
for {
    line, err := buf.ReadBytes('\n')
    if len(line) > 0 { format(line) }
    if err == io.EOF { wait(); continue }   // fsnotify or 100ms poll
}
```

`tail -n +1 -F` semantics. JSONL is line-oriented; partial line at EOF
retries. ~30 LoC. No `--replay` flag — there's nothing to choose; reading
from byte 0 is what users want, the kernel page cache makes "cat 50 MB" free.
Pipe to `grep` if filtering is needed.

**Interactive mode (`--interact`):**

- Cooked terminal mode (kernel handles line editing).
- Blank-line submits. Multi-line paste is one message naturally.
- First-line sigils:
  - `!message…` → `deliver_as=steer` (interrupt)
  - `+message…` → `deliver_as=followUp` (queue)
  - anything else → default Prompt
  - escape with leading `\` (`\!literal bang`).
- Local echo stays on (kernel does it). Agent's echo of the injected message
  appears in the transcript with a `»` prefix + timestamp, distinguishable.
- Empty messages dropped. >64 KiB rejected with a warning.
- Ctrl-\ → trap SIGQUIT → close socket, restore termios, exit 0.

**Filters (formatter-side, cheap):**

```
--no-tokens     # drop message_update token-stream events
--tools-only    # only tool_execution_*
--bell-on-error # \a on is_error tool results — tmux bell-flag wakes user
```

**Tmux integration:**

When `$TMUX` is set, emit `\033]2;<status>\033\\` on every status change to
update the pane title. tmux picks it up automatically. No flicker, integrates
with the user's existing status line, degrades to no-op outside tmux.

## Why the extension is small

The extension stands on already-tested infrastructure:

| Concern | Provided by | Already exercised by |
|---|---|---|
| JSON-RPC framing on stdio | `fir_ext.py` SDK | ~9 builtin extensions |
| Event subscription | `events:` in init handshake | All event-driven extensions |
| `session_start`/`session_shutdown` per session in ACP | extension Manager | `aside.py`, `provider_usage.py` |
| `send_user_message(content, deliver_as=…)` | `session_bridge.go:92` + `bridge_test.go:651` | `aside.py` |
| Per-session lifecycle and config dirs | `fir_ext.config_dirs` | `aside.py`, `doctor.py`, `provider_usage.py` |

Genuinely new code lives in three small places only:

1. `observe.py` (~80 LoC of Python — the sidecar writer + socket loop).
2. `cmd/fir/observe.go` (~150 LoC — sidecar resolver, formatter, tailer,
   optional `--interact` writer).
3. `get_session_file` bridge method (~15 LoC).

There is no fourth place. No buffer corruption to debug, no wire-protocol
mismatch between two implementations of the same thing, no mutex-around-
snapshot review checklist. The bug surface is exactly those three boxes.

**One trailblazer note:** `observe.py` will be the first builtin extension
that binds an *external* Unix socket from inside the extension process
(extensions today only speak JSON-RPC to fir on stdio). The mechanism is
plain Python `socket` with no fir-side coupling — this should Just Work —
but it's the first time we exercise that pattern. Worth flagging during
review.

## What the filesystem actually does for us

"The filesystem handles it" is precise, not hand-waving. The kernel
mechanisms doing the work, named:

**Buffering.** `SessionStore` writes are absorbed by the page cache
(`struct address_space` per inode). Subsequent reads hit the same pages —
zero-copy, RAM-fast. Writeback to disk is the kernel's problem, not ours;
we never `fsync`. Per-fd read offsets (`f_pos`) are independent across
observers, advanced only by that fd's reads. Reader A catching up at byte 0
and reader B at EOF watching live both work without coordination.

**Replay.** Three syscalls:

```
open(path, O_RDONLY)        # fd at offset 0
read(fd, buf, n)            # bytes of history
…
read(fd, buf, n)             # eventually EOF; poll for more
```

That's it. No "replay code." `smart` vs `full` vs `none` collapses to one
`lseek` — `SEEK_SET` for full, `SEEK_END` for live-only — and we don't
ship the flag because `tail -n +1 -F` is the right default and `grep`
handles "skip noise."

**Live tail.** `stat(path)`; if `st_size` grew, read more; else sleep
100 ms. ~6 lines. The file's monotonic growth *is* the event stream; no
`inotify` setup, no subscribe/first-event race.

**Crash isolation.** Writer dies → file persists via writeback or
shutdown flush. Reader dies → kernel reaps fd, writer never noticed. No
mutex, no channel, no `select { default: close(ch) }` invariant to defend.

**Trim policy.** None of our concern — we inherit whatever `SessionStore`
already does (or doesn't do) for transcript size management. Long-running
sessions are someone else's problem at someone else's layer.

**Bonus, free, unsupported but works:** transcripts on a network filesystem
(NFS-mounted home dir) are tailable from another machine. Useful for fleet
debugging; tail latency depends on NFS attribute-cache settings. We don't
test or document it as a feature — it just falls out.

**The deeper observation:** every "hard problem" the original design
listed — bounded buffer, fan-out, backpressure, replay-vs-live race,
post-mortem retention, crash-isolation — exists *only inside the
abstraction "events live in process memory."* Step out of that
abstraction (events live on disk; processes are ephemeral) and the
problems don't get solved cheaper; they don't exist at all. When a
problem dissolves rather than shrinks under a redesign, you've found the
right abstraction.

## What core needs to change

Two small additive changes:

1. **Bridge method `get_session_file`** — returns
   `SessionStore.GetSessionFile()` so the extension can announce
   `store_path` in the sidecar. ~15 LoC + test. Also updates
   `docs/extension-protocol.md`, `fir_ext.py` docstring, `demo.py`,
   `demo_ext_test.py` per project rules.

2. **Verify SessionStore flushes per record.** `tail -F` lags by however
   long the writer buffers. If `SessionStore` batches, change to flush
   per event. ~30 min investigation; ~0–10 LoC fix if needed.

> **Update**: `fir observe` and `fir send` now run as **CLI verbs of
> observe.py** rather than Go subcommands in `cmd/fir/`. The mechanism is
> documented in `docs/design/extension-cli-verbs.md`. Behaviour is
> unchanged — sidecar discovery, formatter, `--interact`, sigil parsing,
> NDJSON wire format are all identical, just ported from Go to Python.
> The "Implementation order" section below is historical; the bridge
> method, sidecar/socket extension, and CLI verbs all shipped together.

The `message_update` / `tool_execution_update` extension event surface is
**not** needed — the transcript file already has them, and the extension
itself doesn't read events for any purpose other than lifecycle.

## Implementation order

1. Verify SessionStore JSONL completeness + per-record flush.
2. Bridge method `get_session_file` + extension protocol docs.
3. Extension `pkg/resources/builtin_extensions/observe.py`: sidecar
   atomic-write on lifecycle events; socket bind; NDJSON read loop calling
   `send_user_message`. Tests in `pkg/resources/testdata/observe_test.py`.
4. CLI `cmd/fir/observe.go`: sidecar reader, prefix resolver, file
   tailer with formatter, optional socket writer. Goldens for the formatter.
5. `--interact` path: cooked-mode stdin, sigil parser, NDJSON encoder,
   SIGQUIT trap.
6. Update `self` skill + CHANGELOG; `make all`.

**Total: ~265 LoC across all components**, mostly outside core.

| Component | Lang | LoC |
|---|---|---|
| `cmd/fir/observe.go` (resolver + tailer + formatter) | Go | ~120 |
| `cmd/fir/send.go` (cooked-mode stdin + sigils + NDJSON + socket) | Go | ~50 |
| `--interact` wrapper (tail + send in same process) | Go | ~15 |
| `observe.py` extension (sidecar + socket + send_user_message forward) | Python | ~80 |
| `get_session_file` bridge method | Go | ~15 |
| Sidecar atomic-write helper | Python | ~20 |

## Edge cases pinned

- **Buffered JSONL writes** → addressed in step 1.
- **Session forking** (`pkg/session/store/lock.go` forks on locked session
  → store path can change mid-session). Extension re-writes the sidecar with
  the new path on detection. CLI re-reads sidecar on EOF after a brief delay.
- **Send-during-shutdown.** Socket close races with extension shutdown;
  observer sees connection drop cleanly.
- **Stale sockets in runtime dir.** Harmless; `fir sessions` skips any
  whose `connect()` fails.
- **Post-mortem.** Sockets gone after fir exit. Sidecar remains. CLI:
  `fir observe X` against a dead session reads the sidecar's
  `store_path`, tails the file once (no live), warns no live tail, no
  `--interact`. Free post-mortem.
- **Multiple observers writing.** All NDJSON lines funnel through
  `send_user_message`; existing semantics apply (each line is an
  independent message).
- **Multi-line content via `\n` in JSON.** NDJSON requires `\n` escaping
  inside `content`. Handled by the JSON encoder; no special parsing.

## What we explicitly don't get

- **No remote observation.** Local Unix sockets only. Tunnel via SSH.
- **No protocol versioning beyond `schema: 1`.** NDJSON wire is one direction
  with a `deliver_as` enum; future fields land additively.
- **No authentication beyond filesystem permissions.** `--interact` over
  this socket is exactly as trusted as your `$XDG_RUNTIME_DIR`. Document.
- **No replay seek modes.** `tail -n +1 -F`; `grep` for filtering.

## Why not a custom server in core

Building observation as a Unix-socket server in core would import a stack of
generic problems with nothing fir-specific in them: bounded buffers, trim
policy, subscriber fan-out, slow-client backpressure, replay-vs-live races,
wire-protocol versioning, multi-instance discovery, post-mortem retention.
Every one is solved already — by the kernel and the filesystem. A file with
`tail -F` gives us the buffer, replay, fan-out, persistence, post-mortem,
and crash-survival for free.

The minimal thing fir actually has to do is announce the file's path and
accept user input back. That's a per-session extension, ~80 LoC, no new
infrastructure in core. Initial estimate for the alternative was ~530 LoC
of core code; this is ~265 LoC, mostly outside core, with strictly smaller
test surface and strictly fewer correctness invariants to defend.

The 30-line bash equivalent works:

```bash
#!/bin/bash
id=$(ls ~/.local/state/fir/agents/ | sed 's/\.json$//' | grep "^$1" | head -1)
sidecar=~/.local/state/fir/agents/$id.json
store=$(jq -r .store_path "$sidecar")
sock=$(jq -r .socket_path "$sidecar")
[[ "$2" == "--interact" ]] || exec tail -n +1 -F "$store"
tail -n +1 -F "$store" &
trap 'kill %1' EXIT
nc -U "$sock"
```

If the bash version works, the design is correctly sized.

## Consequences worth knowing

**Multi-observer is free and structural.** N readers each `open(2)` the
transcript; the kernel maintains independent `f_pos` per fd. No fan-out
code, no subscriber list, no backpressure logic — these aren't simplified,
they're absent. Adding the 100th observer costs the same as the first.

This enables, with no extra code:
- Two humans watching the same session in different tmux panes with
  independent scroll/filter state.
- An agent observing via `--json` while a human watches the formatted
  view, simultaneously.
- A `fir observe X --json > session.log` recording running in
  parallel with interactive observers, none aware of each other.
- `fir observe X | tee >(jq …) | grep …` ad-hoc analysis pipelines
  alongside live viewing.

**Observer crash is a no-op.** No subscriber cleanup, no `subs` map
removal, no mutex-protected close. The kernel reaps the fd at process
exit; that is the entire cleanup path.

**Replay-while-live works trivially.** Observer A reads from byte 0 to
catch up while observer B sits at EOF watching live — both proceed
independently. The original ring-buffer design would have needed snapshot
sequencing for this; here it's two `lseek` calls in two unrelated
processes.

**Live-tail mechanism: 100ms `os.Stat` poll, not `fsnotify`.** Trivially
correct, no per-uid watch limit, ~10 syscalls/sec when idle. Switch to
`fsnotify` only if anyone reports tail latency they care about.

If a future change to this feature would *break* any of the above, that
is the signal to step back and ask whether the change belongs somewhere
else.

## Principle (for future extensions of this feature)

**Multi-observer is free and structural.** N readers each `open(2)` the
transcript; the kernel maintains independent `f_pos` per fd. No fan-out
code, no subscriber list, no backpressure logic — these aren't simplified,
they're absent. Adding the 100th observer costs the same as the first.

This enables, with no extra code:
- Two humans watching the same session in different tmux panes with
  independent scroll/filter state.
- An agent observing via `--json` while a human watches the formatted
  view, simultaneously.
- A `fir observe X --json > session.log` recording running in
  parallel with interactive observers, none aware of each other.
- `fir observe X | tee >(jq …) | grep …` ad-hoc analysis pipelines
  alongside live viewing.

**Observer crash is a no-op.** No subscriber cleanup, no `subs` map
removal, no mutex-protected close. The kernel reaps the fd at process
exit; that is the entire cleanup path.

**Replay-while-live works trivially.** Observer A reads from byte 0 to
catch up while observer B sits at EOF watching live — both proceed
independently. The original ring-buffer design would have needed snapshot
sequencing for this; here it's two `lseek` calls in two unrelated
processes.

**Live-tail mechanism: 100ms `os.Stat` poll, not `fsnotify`.** Trivially
correct, no per-uid watch limit, ~10 syscalls/sec when idle. Switch to
`fsnotify` only if anyone reports tail latency they care about.

If a future change to this feature would *break* any of the above, that
is the signal to step back and ask whether the change belongs somewhere
else.



When tempted to add: a bounded buffer, a subscriber fan-out, a replay
policy, a wire protocol, multi-instance coordination, or post-mortem
retention — stop. The kernel and filesystem already provide all of those,
mature, tested, and faster than anything we'd build. Two syscalls
(`open`, `read`) plus one tool (`tail -F`) cover the entire stack.

The only things this feature's code does that the OS can't:

1. Announce *where* the transcript lives (sidecar JSON).
2. Accept user input *back* into a running session (NDJSON socket).

Anything else added here should pass the same test: "is this something the
OS structurally cannot do?" If not, lean on the OS.
