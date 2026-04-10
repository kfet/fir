# Poe relay — multi-conversation routing

## Overview

The relay is a dumb router that sits between Poe and N fir agent
processes. Each fir handles one or more Poe conversations. The relay
owns the public HTTPS endpoint (via Tailscale Funnel) and the Poe
access key. Agents connect outbound to the relay over the tailnet.

The relay makes zero decisions about when to spawn agents, what to
reply, or how to manage sessions. All intelligence lives in the
agents (fir + skills + bash).

## Architecture

```
                         ┌──────────────────────┐
    Poe ──HTTPS/SSE───▶  │       relay          │
                         │  :443 Funnel (public) │
                         │  :9090 ws (tailnet)   │
                         │                       │
                         │  conv→conn map        │
                         │  lobby (queued msgs)  │
                         └──────┬───────┬────────┘
                           ws   │       │   ws
                    ┌───────────┘       └───────────┐
                    ▼                               ▼
              ┌──────────┐                    ┌──────────┐
              │ agent A  │                    │ agent B  │
              │ (fir)    │                    │ (fir)    │
              │ conv α,β │                    │ conv γ   │
              └──────────┘                    └──────────┘
              (any box on tailnet)            (any box on tailnet)
```

## Relay interfaces

### Public HTTPS (:443 via Funnel)

Serves the Poe protocol. Identical to the current v1 bridge:
- POST /poe — bearer auth, dispatch by type
- query → route to registered agent or queue
- settings/report_* → handled directly by relay

### Tailnet websocket (:9090)

Agent connections. No bearer auth — tailnet identity is sufficient.

**Agent → relay messages (JSON over ws):**

```json
{"type": "register", "conv_id": "c-alpha"}
{"type": "reply", "message_id": "m-xyz", "text": "hello", "final": false}
{"type": "reply", "message_id": "m-xyz", "text": "", "final": true}
```

**Relay → agent messages (JSON over ws):**

```json
{"type": "query", "conv_id": "c-alpha", "message_id": "m-xyz", "user_id": "u-...", "content": "hello", "query": [...]}
{"type": "pending", "conv_id": "c-gamma", "user_id": "u-...", "content": "first msg"}
{"type": "register_ok", "conv_id": "c-alpha"}
{"type": "register_rejected", "conv_id": "c-alpha", "reason": "active"}
```

### Keepalive

- Websocket ping/pong. Agent sends ws ping every 30s.
- Relay expects a ping within 60s. Missed → conn dead → all its
  registrations removed → affected conv_ids become unregistered →
  queued messages trigger "pending" notifications to remaining agents.

## Registration state machine

Each conv_id has one of three states:

```
(unregistered) ──register──▶ provisional ──first reply──▶ active
       ▲                         │                          │
       │                         │ override by              │ conn dies
       │                         │ another agent            │
       │                         ▼                          │
       └─────────────────── (unregistered) ◀────────────────┘
```

**Unregistered:**
- No agent handling this conv_id
- Inbound Poe queries are queued in the lobby
- Relay emits SSE `meta` + "connecting to agent..." text + holds stream open
- Relay broadcasts `pending` notification to all connected agents

**Provisional:**
- An agent has registered but hasn't written a reply yet
- Inbound queries are forwarded to the registered agent
- Another agent CAN override by registering the same conv_id
  (first-write-wins: the pre-registering agent wanted to hand off)
- If the agent writes a reply → transitions to `active`

**Active:**
- Agent is actively handling this conv_id (has written at least one reply)
- Locked to this agent's conn. Other register attempts are rejected.
- Remains active until the conn dies (disconnect, ping timeout)
- On conn death → returns to unregistered

## Query routing flow

1. Poe POST arrives with conv_id + message_id
2. Relay emits SSE `meta` event immediately (5-second rule)
3. Lookup conv_id in registration map:
   - **Registered (provisional or active):** forward query to agent's ws conn.
     Hold SSE stream open. Agent sends reply chunks back via ws. Relay
     streams them as SSE text events. Agent sends final=true → relay
     emits SSE done.
   - **Unregistered:** queue in lobby. Emit "connecting to an agent..."
     as SSE text. Broadcast `pending` to all agents. Hold SSE stream
     open (up to 60s). If an agent registers within that window and
     sends a reply, relay streams it. If timeout → emit SSE error + done.

## Agent (fir-side) flow

Each fir spawns poe-bridge in agent mode as its MCP server:

```json
{
  "mcpServers": {
    "poe": {
      "command": "poe-bridge",
      "args": ["--agent", "--relay", "ws://krpi2one:9090"],
      "env": { "POE_CONV_ID": "c-alpha" }
    }
  }
}
```

On startup:
1. Connect to relay via ws
2. If POE_CONV_ID is set: send `register` for that conv_id
3. Expose MCP tools to fir: `reply`, `register`, `list_pending`
4. Forward inbound queries from relay → fir via notifications/claude/channel
5. Forward fir's reply tool calls → relay as reply messages

MCP tools available to fir:
- `reply(message_id, text, final)` — same as v1
- `register(conv_id)` — register this agent for a new conv_id
- `list_pending()` — returns conv_ids in the relay's lobby
- `spawn(conv_id, session_name)` — convenience: calls register (provisional),
  then the fir model uses bash+tmux to spawn a new fir

## Spawn skill

When a connected fir sees a `pending` notification, its model (guided
by the poe-spawn skill) does:

```bash
# Create per-conv directory
mkdir -p ~/fir-poe/convs/c-gamma/.fir

# Write mcp.json
cat > ~/fir-poe/convs/c-gamma/.fir/mcp.json << 'EOF'
{
  "mcpServers": {
    "poe": {
      "command": "poe-bridge",
      "args": ["--agent", "--relay", "ws://krpi2one:9090"],
      "env": { "POE_CONV_ID": "c-gamma" }
    }
  }
}
EOF

# Spawn in tmux
tmux new-window -t poe "fir --session poe-c-gamma ~/fir-poe/convs/c-gamma"
```

Optionally pre-registers via the `register` tool first for low latency,
then the new fir's agent overrides (since pre-registration is provisional).

## What the relay does NOT do

- Spawn processes
- Make decisions about which agent handles what
- Store conversation history (Poe provides it)
- Authenticate agents (tailnet handles it)
- Parse or understand message content

## Implementation plan

1. `internal/relay/` package — registration map, lobby queue, ws hub
2. `internal/relay/poe.go` — Poe HTTP handler (reuses internal/poe)
3. `internal/relay/agent.go` — ws handler for agent connections
4. `--relay` flag in main.go — starts relay mode
5. `--agent --relay <url>` flags — starts agent mode
6. Tests: in-process relay + agents over in-memory ws, verify routing,
   provisional→active→death lifecycle, override, pending broadcast

## Config

```
# Relay mode
poe-bridge --relay \
  --poe-port 8080 \
  --agent-port 9090 \
  --access-key $POE_ACCESS_KEY \
  --state-dir ~/.config/poe-relay

# Agent mode  
poe-bridge --agent \
  --relay ws://krpi2one:9090 \
  --conv c-alpha
```

v1 mode (no --relay, no --agent) continues working as before —
single bridge, single fir, no relay.
