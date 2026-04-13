# Bridge Reconnect & Graceful Upgrade

## Problem

When the relay restarts (binary upgrade, crash, etc.), every agent's
poe-bridge ws connection drops. The bridge MCP server process dies,
fir marks it as "failed to connect", and the agent is dead. Recovering
requires manually restarting each agent. This is brittle and breaks
every time.

## Architecture Recap

```
fir ←stdio→ poe-bridge (MCP server, child process)
                ↕ websocket
             relay (:8080 HTTP from Poe, :9090 ws to agents)
```

Two independent connections:
1. **fir ↔ poe-bridge** — MCP over stdio. Lives as long as the child process.
2. **poe-bridge ↔ relay** — websocket. Dies when relay restarts.

Key insight: connection #1 never needs to break. The bridge process
stays alive; only the ws (#2) needs reconnecting.

## Design

### Bridge ws reconnect loop

`agent.Connect()` currently dials once and dies on read error. Change to:

```
Connect(ctx, cfg) → Agent
  └─ dial loop (background goroutine):
       1. Dial relay ws
       2. If conv_id set → re-register
       3. Run readPump until error
       4. On error: log, backoff, goto 1
       5. On ctx cancel: exit
```

States:
- **connected**: ws is up, tools work normally
- **disconnected**: ws is down, reconnecting. Tool calls (Reply, Register)
  return transient error: "relay unavailable, reconnecting"
- Bridge never exits on its own (only when fir kills it via ctx/signal)

Backoff: 500ms, 1s, 2s, 4s, 8s, 15s max. Reset on successful connect.

### What changes

**`internal/agent/agent.go`**:
- `Connect()` starts reconnect loop goroutine, returns immediately after first successful dial
- `readPump()` returns error instead of closing agent
- New `reconnectLoop()` manages dial → readPump → backoff cycle
- `sendJSON()` returns error when disconnected (no ws)
- `done` channel semantics: closed only when ctx is cancelled (not on ws drop)
- New `connected` field (atomic bool) for fast status checks
- `Reply()` checks connected before sending, returns clear error

**`internal/agent/agent_test.go`**:
- Test: agent reconnects after relay ws drops
- Test: Reply returns error during disconnect
- Test: agent re-registers conv_id on reconnect  
- Test: pending/query callbacks work after reconnect
- Test: backoff increases then caps at max

**No changes needed in**:
- fir (MCP stdio connection stays alive)
- relay (stateless, accepts new connections)
- catch-all (its bridge also reconnects)

### Upgrade procedure

After this change, upgrading all binaries:

```bash
# 1. Build & install
make install bridges-install

# 2. Restart relay (new binary)
#    Agent bridges temporarily disconnect, auto-reconnect
kill -HUP $(pgrep -f 'poe-bridge --relay') 
# or: kill & restart if relay doesn't support SIGHUP yet

# 3. Restart each agent fir (new fir + new poe-bridge binary)
#    SIGHUP → reexec, session preserved, new bridge reconnects
tmux list-panes -t agents -F '#{pane_pid}' | xargs kill -HUP

# 4. Restart catch-all
tmux list-panes -t poe-air:0 -F '#{pane_pid}' | xargs kill -HUP
```

Poe queries during relay restart (~2s window) get HTTP timeout → Poe
auto-retries. Queries during agent SIGHUP are buffered in relay lobby
and delivered when agent reconnects.

### E2E test plan

`internal/agent/reconnect_test.go` — tests run against a real relay
(in-process) to validate the full flow:

1. **TestReconnectAfterRelayRestart**: start relay → connect agent →
   kill relay → verify agent is disconnected → start new relay on
   same port → verify agent reconnects and re-registers
2. **TestReplyDuringDisconnect**: disconnect relay → call Reply() →
   verify transient error returned (not hang)
3. **TestQueryDeliveryAfterReconnect**: disconnect → reconnect →
   send query from relay → verify agent receives it
4. **TestBackoffTiming**: mock dialer that fails N times → verify
   delays increase geometrically up to cap
5. **TestClaimAfterReconnect**: catch-all agent reconnects → relay
   sends pending → agent claims successfully

Tests use real TCP listeners (port 0 for random assignment) and run
in `go test` — no external dependencies. Should complete in <5s.
