# Poe Bot Deployment & Operations

## Architecture

```
Poe HTTP → Tailscale Funnel → :8080 (relay) → ws :9090 → agent bridges → fir instances
```

**Relay** (`poe-bridge --relay`): receives Poe HTTP queries, manages agent
websocket connections, routes queries to registered agents, holds lobby for
unregistered conversations.

**Catch-all agent** (tmux window 0): a fir instance with poe-bridge MCP
(no `POE_CONV_ID`). Its bridge claims new conversations and spawns
dedicated agents. No LLM involved in spawn flow.

**Dedicated agents**: spawned fir instances, each handling one Poe
conversation. Bridge auto-registers with `POE_CONV_ID` on connect.

## Runtime Layout

| Component | Location |
|---|---|
| Relay state | `~/.local/state/fir/poe/relay` |
| Catch-all state | `~/.local/state/fir/agents/catch-all` |
| Agent state dirs | `~/.local/state/fir/agents/<conv_id>/` |
| Binaries | `~/go/bin/fir`, `~/go/bin/poe-bridge` |
| Auth | `~/.config/fir/auth.json` (shared) |
| Tmux session | `poe` (relay + catch-all + agent windows) |

## Deploy

```bash
# Full deploy: test → install → respawn all windows
make poe-deploy

# Just start (first time)
make poe-start
```

### What `poe-deploy` Does

1. Runs `make test` and `make bridges-test`
2. Runs `make install` and `make bridges-install` (`go install`)
3. Respawns relay window (index 0 relay process)
4. Waits 2 seconds for relay to be ready
5. For each agent window (index > 0):
   - Kills fir child processes (`pgrep -P`)
   - Kills the pane PID
   - Waits 0.3s for cleanup
   - `tmux respawn-window -k` with new binary
6. Reports status per window

### Key Design Decisions

- **Window index, not name** for respawn — avoids name collision issues
- **Kill children first** — ensures fir's child processes (poe-bridge,
  MCP servers) are cleaned up before the parent is killed
- **Dead pane handling** — `respawn-window -k` works on dead panes
  (remain-on-exit), no PID hunting needed
- **Adaptive grace period** — relay waits for a quiet window (1s after
  last agent registration, hard cap 5s) before broadcasting lobby,
  preventing catch-all from spawning duplicates during relay restart

## Agent Lifecycle

### New Conversation
1. Poe query → relay queues in lobby
2. Relay broadcasts `pending` to all agents
3. Catch-all claims (`register claim=true` — race gate)
4. Bridge execs `spawn-poe-agent <conv_id>` (~80ms)
5. New fir starts → bridge connects → registers (`claim=false`)
6. Relay delivers lobby query → bridge sends channel notification
7. Fir injects history preamble (if empty session) + user message
8. LLM replies via auto-reply → relay streams SSE back to Poe

### Agent Crash/Disconnect
- Relay detects websocket drop
- Any in-flight queries get an error reply ("Agent crashed or
  disconnected") sent to Poe — user sees error UI with retry button
- `RemoveAgent` cleans up registrations and orphans pending entries

### Agent Restart (SIGHUP)
- `kill -HUP <pid>` → graceful reexec with new binary
- Session state, extension data, queue preserved
- Bridge reconnects automatically (exponential backoff 100ms → 15s)

## Auto-Reply System

The auto-reply module (`pkg/mcp/autoreply`) intercepts agent events and
streams them to Poe without explicit reply tool calls:

| Event | Rendering |
|---|---|
| Text delta | Streamed as-is |
| Thinking start/delta/end | Italic blockquotes: `> *thinking...*` |
| Tool call start | Code fence with language hint |
| Tool call end | Truncated output (≤8 lines) |
| Plan update (1st) | Full: progress bar + all entries |
| Plan update (nth) | Compact blockquote: bar + active items |
| Agent end | Final empty chunk closes SSE |

### Attachment Support

- **Inbound images**: downloaded, base64-encoded, sent as multi-modal
  content blocks (vision). Max 20 MB per image.
- **Inbound documents**: `parsed_content` inlined as code blocks.
- **Other files**: linked with 📎 markdown.

## Troubleshooting

```bash
# Check tmux session
tmux ls
tmux attach -t poe

# View relay logs
tmux select-window -t poe:relay

# Check agent status
ls ~/.local/state/fir/agents/

# Restart a single agent
tmux respawn-window -k -t poe:<window-index>

# Full redeploy
make poe-deploy
```
