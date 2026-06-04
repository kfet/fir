---
builtin: true
name: acp-shepherd
description: Spawn, drive, and observe independent coding agents over ACP (--mode acp) as subprocesses. Replaces tmux-driver+shepherd for agent fleets — each agent is a real JSON-RPC session with structured updates, typed tool calls, and cancellation. No PTY scraping. Supports mixed providers/models, per-session MCP configs, working directories. Use for fleets, skill testing, A/B experiments.
---

# ACP Shepherd

Drive a fleet of agents via the **Agent Client Protocol** instead of
typing into tmux panes. Each agent is a subprocess launched with
`fir --mode acp` (or any ACP-speaking agent binary). You get structured
session updates, typed tool-call events, plan updates, and clean
cancellation — no ANSI scraping, no `INSERT`-mode dances.

## When to use this vs. tmux-driver

| Want to drive… | Use |
|---|---|
| Another coding agent (fir, claude-code, gemini, codex) | **acp-shepherd** |
| A REPL, debugger, server, arbitrary interactive CLI | `tmux-driver` |
| A fleet of mixed-provider agents | **acp-shepherd** |
| A/B testing skills or MCP configs | **acp-shepherd** |

## Architecture

```
┌─ fleet daemon (fleetd.py) ──────────────────────────────┐
│   control socket: /tmp/fleet-<name>/ctl.sock            │
│                                                         │
│   ┌─ AcpAgent "worker-1" ─┐   ┌─ AcpAgent "reviewer" ─┐ │
│   │ fir --mode acp        │   │ fir --mode acp        │ │
│   │   --provider anthropic│   │   --model opus        │ │
│   │   --model sonnet      │   │                       │ │
│   └───────────────────────┘   └───────────────────────┘ │
│                                                         │
│   JSONL logs per agent: /tmp/fleet-<name>/<agent>.jsonl │
└─────────────────────────────────────────────────────────┘
           ▲                       ▲
           │                       │
    `fleet` CLI (one-shot)   `fleet_bridge.py` (streams to tmux pane)
```

The daemon is long-lived because ACP is a **persistent stdio connection** —
you can't reconnect to a running agent from a fresh CLI invocation. All
CLI commands are thin wrappers talking to the daemon over the Unix socket.

## Setup

```bash
source "$SKILL_DIR/scripts/helpers.sh"   # provides the `fleet` function

# Start a daemon for this fleet:
fleet --fleet myproj init
export FLEET_NAME=myproj                 # so subsequent calls find it
# (or: export FLEET_SOCKET=/tmp/fleet-myproj/ctl.sock)
```

## Spawning agents

Default command is `fir --mode acp` with noise extensions disabled:

```bash
# Cheap fast coder
fleet spawn worker-1 --provider anthropic --model sonnet --cwd "$WORKTREE"

# Expensive reviewer
fleet spawn reviewer --provider anthropic --model opus    --cwd "$WORKTREE"

# Different agent binary entirely
fleet spawn gem --cmd "gemini --experimental-acp" --cwd "$WORKTREE"

# With a specific MCP config (for skill testing)
fleet spawn probe --mcp-config ./mcp-test.json --cwd "$WORKTREE"
```

MCP config accepts either the ACP wire shape (a list) or the common
Claude-Desktop shape `{"mcpServers": {name: {command, args, env}}}`.

## Driving a session

```bash
# Send a prompt, block until the turn ends (returns stop_reason):
fleet prompt worker-1 "Read pkg/foo/client.go. Add an OnReconnect field. Commit as 'foo: hook'."

# Cancel in progress:
fleet cancel worker-1

# Kill entirely:
fleet kill worker-1
```

`fleet prompt` blocks. To run prompts in parallel, background them:

```bash
fleet prompt worker-1 "$TASK_A" &
fleet prompt worker-2 "$TASK_B" &
wait
```

## Observing

```bash
# Compact table — all agents, one line each:
fleet status

# Live pretty-printed stream of session updates for one agent:
fleet tail worker-1

# Raw JSON (pipeable, diffable, grep-able):
fleet tail worker-1 --raw

# Last N updates from the persisted log:
fleet capture worker-1 --last 100

# Path to the JSONL log, e.g. for `jq` or custom tooling:
fleet log-path worker-1
```

Every `session/update` is appended to `<state_dir>/<agent>.jsonl`, so
you have a perfect replayable record. No regex scraping.

## Observability bridge (tmux view)

You can render each agent's update stream into a tmux pane, giving you
scrollback, copy-mode search, splits, detach/reattach, and SSH-viewable
monitoring for free. The bridge is just a small ANSI formatter that
reads the tail and prints to stdout.

```bash
STATE=/tmp/fleet-$FLEET_NAME
VIEW=$STATE/view.sock         # a separate tmux socket just for the view

# Create a read-only observability tmux with one window per agent.
tmux -S "$VIEW" -f /dev/null new-session -d -s view \
    "python3 $SKILL_DIR/scripts/fleet_bridge.py worker-1"

tmux -S "$VIEW" new-window -n reviewer \
    "python3 $SKILL_DIR/scripts/fleet_bridge.py reviewer"

tmux -S "$VIEW" new-window -n worker-2 \
    "python3 $SKILL_DIR/scripts/fleet_bridge.py worker-2"

tmux -S "$VIEW" set-option -g history-limit 100000

# Tell the user:
echo "Attach:  tmux -S $VIEW attach -t view"
```

### How the bridge works

1. `fleet_bridge.py NAME` opens the control socket and sends
   `{"cmd": "tail", "name": NAME}`.
2. The daemon streams back JSON records (one per `session/update`).
3. The bridge formats each record as colored ANSI text and writes it
   to stdout — which *is* the tmux pane's PTY.
4. tmux handles scrollback, search (`prefix [` → `/regex`), resize,
   detach (`prefix d`), reattach, and remote viewing via SSH.

### Why a separate tmux socket?

Isolation. The fleet daemon owns agents; the tmux server owns the
*view*. Killing the tmux server just closes the windows — agents keep
running. Killing the fleet daemon stops agents; bridges exit cleanly.

### Offline view (log replay)

If the daemon isn't running, the bridge can read the persisted log
directly, `tail -F`-style:

```bash
python3 $SKILL_DIR/scripts/fleet_bridge.py worker-1 --from-log
```

### Extending the bridge

Common tweaks (edit `fleet_bridge.py`):

- **Collapse tool arguments** past N lines behind a header — render
  `tool_call` with just title+location, drop the huge input JSON.
- **Diff rendering** — when `tool_call_update` carries a `content` of
  `type: "diff"`, format with `+`/`-` coloring.
- **Status bar** — set `tmux set -g status-right` to show
  `fleet status` output (run a small watcher that writes the status
  line every 5s).
- **Input pane** — bind a tmux key that opens `$EDITOR`, then
  `fleet prompt <win> "$(cat /tmp/msg)"` on save. This lets humans
  inject prompts from inside tmux.

## Recipes

### Fleet with cheap workers + expensive reviewer

```bash
fleet --fleet refactor init
export FLEET_NAME=refactor

WORKTREE=$(pwd)-wt-refactor
git worktree add "$WORKTREE" -b fleet/refactor

for n in 1 2 3; do
  fleet spawn "worker-$n" --provider anthropic --model sonnet --cwd "$WORKTREE"
done
fleet spawn reviewer --provider anthropic --model opus --cwd "$WORKTREE"

fleet prompt worker-1 "Move pkg/foo/export.go to pkg/bar/. Update imports. Tests green. Do not commit."
# … shepherd loop reads `fleet status` and `fleet capture` each cycle
```

### Skill + MCP A/B test

```bash
fleet spawn probe-A --mcp-config ./mcp-A.json --cwd /tmp/eval-A
fleet spawn probe-B --mcp-config ./mcp-B.json --cwd /tmp/eval-B

TASK="Follow the xyz skill and solve problem 42"
fleet prompt probe-A "$TASK" &
fleet prompt probe-B "$TASK" &
wait

# Compare structured outputs:
diff <(fleet capture probe-A --last 500 --pretty) \
     <(fleet capture probe-B --last 500 --pretty)
```

### Shepherd outer loop

Use `fleet-loop` — a structured replacement for the old `tm-loop-tick`.
It polls `fleet status`, reads each agent's JSONL tail, and prints a
compact dashboard with flags for dead/idle/no-tool-progress agents.
Optionally runs tests and writes `FLEET.md`.

```bash
# One tick:
fleet-loop

# Continuous, 10s cadence, run tests each cycle, maintain FLEET.md:
fleet-loop --watch --interval 10 \
           --test "make test" --worktree "$WORKTREE" \
           --write-fleet-md
```

`/new` equivalent is just: `fleet kill w1 && fleet spawn w1 ...`.
No slash-command typing.

Manual shepherd loop skeleton when you want more control:

```bash
while true; do
  fleet-loop                      # prints dashboard line per agent
  for w in worker-1 worker-2 worker-3; do
    last=$(fleet capture "$w" --last 1)
    # decide: assign next task, cancel, restart
  done
  sleep 10
done
```

## Permissions

The default `session/request_permission` handler **auto-allows** all
tool calls (picks `allow_always` or the first option). This is right
for unattended fleets in worktrees. If you want human approval, patch
`AcpAgent._dispatch` in `acp_client.py` — the hook is marked.

## Comparison to tmux-driver/shepherd

| Concern | tmux-driver/shepherd | acp-shepherd |
|---|---|---|
| Transport | PTY (ANSI stream) | JSON-RPC over stdio |
| Send task | `tm-send` (types text + Enter) | `session/prompt` |
| Observe | `tm-capture` (regex-scrape ANSI) | `session/update` stream + JSONL log |
| Cancel | `tm-sendraw Escape` (best-effort) | `session/cancel` (real) |
| Routing | `NAME:WINDOW` discipline, races | session UUIDs, no ambiguity |
| Disable 4 extensions? | Yes (auto-namer/notify/…) | Built in |
| Multi-line paste | `Escape; Enter` workaround | Just a string field |
| Mixed providers in fleet | No (auth contention) | Yes |
| Human attach | Native (`tmux attach`) | Via observability bridge |

## Files

- `scripts/acp_client.py` — ACP JSON-RPC client (async, one class).
- `scripts/fleetd.py` — daemon: holds agents, exposes control socket.
- `scripts/fleet.py` — CLI (`fleet init|spawn|prompt|...`).
- `scripts/fleet_loop.py` — shepherd dashboard / loop tick (`fleet-loop`).
- `scripts/fleet_bridge.py` — ANSI renderer for tmux panes / log tailing.
- `scripts/helpers.sh` — sources `fleet`, `fleet-bridge`, `fleet-loop`.

## Cleanup

```bash
fleet shutdown                         # kills all agents, stops daemon
tmux -S "$VIEW" kill-server            # close view (agents unaffected)
rm -rf /tmp/fleet-$FLEET_NAME          # remove logs and socket dir
```
