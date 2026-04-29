# Design: `fir pty attach` — PTY sub-process attach

**Status**: design, lower priority (implement after `acp-observe.md`)
**Branch**: `tmux-attach-pty`

## Goal

Let a human run `fir pty attach <target>` against a ptydriver-managed session
and get:

1. **Scrollback** — replay of recent PTY output for context.
2. **Live tail** — every new byte as the PTY emits it.
3. **Input proxy** — keystrokes forwarded to the PTY; or read-only with
   `--observe`.

This is useful when a fir agent (in any mode) is driving an interactive
sub-process via the ptydriver — a build, a REPL, a long-running test run —
and you want to watch or take over.

## Why not `tmux attach`

`tmux attach` speaks tmux's internal binary IPC protocol: undocumented,
version-locked, and requires reimplementing the tmux server. Not viable.

A future `--tmux` backend where fir spawns sessions inside a real tmux server
it owns is fine and stays available if splits/copy-mode are ever needed. Not
the goal here.

## Scrollback sizing

Bounded by the largest model context we'd feed it into. Frontier contexts
≈ 1 M tokens ≈ 3.5 MB text; with ANSI overhead 7 MB raw is the practical
ceiling. Fits in RAM. No on-disk log needed.

**Default cap: `FIR_PTY_SCROLLBACK_MAX = 10 MB`.**

## Architecture

```
PTY (ptmx) ──► Screen      (parsed grid — capture/wait, unchanged)
            ├► Scrollback  (bounded raw bytes, ≤ 10 MB)
            └► Subscribers (live attachers)
```

PTY-reader goroutine is the only writer. Single-writer eliminates most
concurrency complexity:

```go
for {
    n, err := ptmx.Read(buf)
    if n > 0 {
        chunk := buf[:n]
        screen.Write(chunk)        // existing
        scrollback.Append(chunk)   // new
        session.broadcast(chunk)   // new
    }
    if err != nil { return }
}
```

### Bounded buffer trim policy

When append would exceed cap:
- Drop bytes from the front until size ≤ cap.
- Snap forward to next newline if one is within 1 KiB.
- Never trim mid-escape-sequence or mid-UTF-8 rune.

Logical `start` offset is monotonic; subscribers track caught-up-to offset so
trims don't confuse them.

### Alt-screen tracking

Track the most recent `\e[?1049h` / `\e[?1049l` toggle:

```go
type Scrollback struct {
    buf                 []byte
    start               int64
    lastAltScreenOffset int64 // -1 if none in current buffer
    inAltScreen         bool
}
```

Scan each chunk for the escape sequences on Append (cheap — sequences are
sparse). On trim, if `lastAltScreenOffset < start`, reset to -1.

### Subscriber fan-out + no-gap handoff

```go
func (s *Session) Subscribe(sub *subscriber) (snapshot []byte, startOffset int64) {
    s.writerMu.Lock()
    defer s.writerMu.Unlock()
    snapshot = s.scrollback.Snapshot()
    startOffset = s.scrollback.EndOffset()
    s.subs[sub] = struct{}{}
    return
}
```

Writer holds the same mutex around the `Write/Append/broadcast` triple.
Subscriber added between two writes sees: snapshot ending at offset E, then
live bytes starting at E. No gap, no overlap.

Slow client policy: bounded per-subscriber channel; if full, close the channel
and let the attach handler clean up. **The slow-client-must-not-stall-the-PTY
rule is the single most important correctness invariant.** Test it explicitly.

## Wire protocol

Identical to the `acp-observe.md` protocol post-handshake, with the addition
of `RESIZE`.

### Handshake

```json
{"method":"attach","params":{
    "target":   "agent:shell",
    "cols":     120,
    "rows":     40,
    "readonly": false,
    "replay":   "smart"
}}
```

### Frames

```
[1 byte type][4 byte BE length][payload]

0x01 DATA    PTY output (server→client) or stdin (client→server)
0x02 RESIZE  payload = 4 bytes: u16 cols, u16 rows (client→server)
0x03 DETACH  empty payload, either direction
```

### Replay policy

- `smart` (default): if in alt-screen and `lastAltScreenOffset` is in buffer,
  replay from there. Otherwise replay from `max(start, end − 256 KiB)`.
- `full`: replay from `start` (up to 10 MB).
- `none`: live tail only.

## Window resize

Client watches SIGWINCH and sends RESIZE frames. Server calls `pty.Setsize`.
**Last writer wins** — most recent attacher's size is the PTY's size.

## Detach hotkey

`Ctrl-\` (`0x1c`). Doesn't collide with tmux (Ctrl-B) or screen (Ctrl-A).
Default action is SIGQUIT, which nobody wants in a TUI; reclaiming it is a
feature. Client scans stdin for `0x1c` and treats it as detach, not forwarded.

## CLI surface

```
fir pty attach <target>            # smart replay + live tail
fir pty attach <target> --full     # full buffer replay
fir pty attach <target> --observe  # read-only
fir pty scrollback <target>        # dump buffer to stdout, no live tail
```

`fir pty scrollback <t> | grep` — with a 10 MB ceiling this is instant.

## What we explicitly don't get

- No persistence across fir restarts.
- No archival history.
- No splits, copy mode, status line.
- No on-disk privacy footprint.

## Implementation order

1. **`Scrollback` type** — bounded `[]byte`, append/trim, snap-on-newline,
   never-mid-escape/UTF-8, alt-screen tracking. ~120 LoC + tests.

2. **Subscriber fan-out in `Session`** — `subs` map, `writerMu`, bounded
   per-sub channel, slow-client drop, `Subscribe`/`Unsubscribe`. ~80 LoC +
   tests (including explicit slow-client test).

3. **`attach` server handler** — handshake, snapshot, live stream, frame
   codec, RESIZE handling. ~120 LoC + tests.

4. **`fir pty attach` client** — raw stdin, SIGWINCH, Ctrl-\ detach. ~120 LoC.

5. **`--observe` flag** — thread `readonly` through handshake. ~10 LoC.

6. **`fir pty scrollback`** — new `scrollback` server method, dump to stdout.
   ~30 LoC.

Total ~480 LoC + tests. No new dependencies.

## Risks

- **Slow client stalls PTY**: bounded channel + drop. Test with non-reading
  attacher.
- **Replay across alt-screen boundary**: `lastAltScreenOffset` mitigates.
  Test attach during a TUI (e.g. vim).
- **Subscribe / process-exit race**: test attach to session whose process exits
  mid-handshake.
- **DSR (`\e[6n`) responses stale on replay**: most TUIs only DSR at startup.
  Acceptable; document as caveat.
