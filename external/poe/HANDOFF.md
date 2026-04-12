# Poe Relay + Spawn Pipeline — Handoff Notes

## Architecture

```
Poe → Tailscale Funnel → :8080 (relay) → ws :9090 → agent bridges → fir instances
```

**Relay** (`poe-bridge --relay`): receives Poe HTTP queries, manages agent
websocket connections, routes queries to registered agents, holds lobby for
unregistered conversations.

**Catch-all agent**: a fir instance with poe-bridge MCP (no POE_CONV_ID).
Its bridge claims new conversations and execs `spawn-poe-agent` to create
dedicated agents. No LLM involved in the spawn flow.

**Dedicated agents**: spawned fir instances, each handling one Poe
conversation. Bridge auto-registers with POE_CONV_ID on connect.

## Key flows

### New conversation
1. Poe query arrives → relay queues in lobby
2. Relay broadcasts `pending` to all agents
3. Catch-all bridge claims (register claim=true) — race gate
4. Bridge execs `spawn-poe-agent <conv_id> <mcp_json>` (~80ms)
5. New fir starts → bridge connects → registers (claim=false)
6. Relay delivers lobby query → bridge sends channel notification to fir
7. Fir injects history preamble (if empty session) + user message
8. LLM replies via mcp__poe__reply → relay streams SSE back to Poe

### Subsequent messages
1. Poe query arrives → relay routes to registered agent
2. Bridge sends channel notification (no history, just current message)
3. Fir session already has context → LLM replies

### Agent restart
- `kill -HUP <pid>` or `/reexec` → graceful restart with new binary
- Session state, extension data, and queue preserved
- Agent resumes with `-c` flag

## Files changed today

### external/poe/
- `cmd/poe-bridge/main.go` — bridge spawns directly, dumb pipe for history
- `internal/relay/relay.go` — claim registration, no lobby TTL, reply ack
- `internal/relay/relay_test.go` — 5 new claim tests
- `internal/agent/agent.go` — async OnPending, sync Reply, done channel
- `internal/agent/agent_test.go` — deadlock tests
- `internal/mcpnotify/notifier.go` — SendChannel waits for connection
- `internal/history/` — restored FormatPreamble package

### pkg/
- `mcp/inject.go` — history preamble injection on empty sessions
- `mcp/inject_test.go` — 2 new history tests
- `session/factory.go` — ExtReady wait + sessionLen for history
- `modes/interactive/mode.go` — SIGHUP → reexec
- `resources/builtin_skills/spawn-agent/` — skill + shell script

### Build
- `Makefile` — bridges-install target
- `external/poe/Makefile` — install target

## Runtime

- Relay: poe-air tmux session, background job
- Catch-all fir: poe-air tmux session, window 0
- Agents: "agents" tmux session, one window per conversation
- Agent dirs: `~/.local/state/fir/agents/<conv_id>/`
- Auth: shared `~/.config/fir/auth.json`
- Tailscale Funnel: `https://kfetairm1.tail77d32.ts.net` → localhost:8080

## Current state (83e0b8a)
- All on main, CI green
- Binaries: `~/go/bin/fir` + `~/go/bin/poe-bridge`
- `spawn-poe-agent` script: `~/go/bin/spawn-poe-agent`
