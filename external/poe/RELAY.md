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
                         │  :8080 HTTP (public)  │
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
{"type": "register", "conv_id": "c-alpha", "claim": true}   // race gate — no queries forwarded
{"type": "register", "conv_id": "c-alpha"}                   // real agent — gets queries
{"type": "reply", "message_id": "m-xyz", "text": "hello", "final": false}
{"type": "reply", "message_id": "m-xyz", "text": "", "final": true}
```

The `claim` flag on register controls whether this is a lightweight
claim (race gate for spawning) or a real registration (takes ownership
and receives queries). See "Registration state machine" below.

**Relay → agent messages (JSON over ws):**

```json
{"type": "query", "conv_id": "c-alpha", "message_id": "m-xyz", "user_id": "u-...", "content": "hello", "query": [...]}
{"type": "pending", "conv_id": "c-gamma", "user_id": "u-...", "content": "first msg"}
{"type": "register_ok", "conv_id": "c-alpha"}
{"type": "register_rejected", "conv_id": "c-alpha", "reason": "claimed"}
{"type": "register_rejected", "conv_id": "c-alpha", "reason": "active"}
```

### Keepalive

- Websocket ping/pong. Agent sends ws ping every 30s.
- Relay expects a ping within 60s. Missed → conn dead → all its
  registrations removed → affected conv_ids become unregistered →
  queued messages trigger "pending" notifications to remaining agents.

## Registration state machine

Each conv_id has one of two states: `provisional` or `active`.
The `register` message carries an optional `claim` flag that controls
behaviour.

```
                  register(claim=true)                register(claim=false)          first reply
(unregistered) ─────────────────────▶ provisional ──────────────────────▶ provisional* ──────────▶ active
       ▲                                   │                                  │                      │
       │                        timeout /  │                       conn dies  │               conn dies│
       │                        conn dies  │                                  │                      │
       └───────────────────────────────────┘──────────────────────────────────┘──────────────────────┘

* same state, but conn replaced by the spawned agent
```

### register(claim=true) — race gate

- **Purpose:** claim a conv_id so only one agent spawns a new fir for it.
- **(none) → provisional:** accepted. Conn recorded. **No lobby queries
  delivered.** Lobby stays intact until a real agent takes over.
- **provisional → rejected:** already claimed. Returns `register_rejected`
  with `reason: "claimed"`.
- **active → rejected:** conv is locked. Returns `register_rejected`
  with `reason: "active"`.

### register(claim=false) — real agent takes ownership

- **Purpose:** the spawned fir's agent takes ownership and receives queries.
- **(none) → provisional:** accepted. Conn recorded. **Lobby queries
  delivered immediately.**
- **provisional → provisional (override):** accepted. New conn replaces
  the claimer's conn. **Lobby queries delivered to the new agent.**
  Queries arriving while provisional are also forwarded to this agent.
- **active → rejected:** conv is locked. Returns `register_rejected`
  with `reason: "active"`.

### First reply write → active

When an agent sends its first `reply` for a conv_id, the registration
transitions from provisional to **active**. The conv is now locked to
this agent's conn. No further register attempts (claim or otherwise)
are accepted until the conn dies.

### Conn death → unregistered

When a conn dies (disconnect, ping timeout), all its registrations are
removed. Affected conv_ids return to unregistered. If queries were
in-flight, they time out on the Poe SSE side.

**Unregistered:**
- No agent handling this conv_id
- Inbound Poe queries are queued in the lobby
- Relay emits SSE `meta` + "connecting to agent..." text + holds stream open
- Relay broadcasts `pending` notification to all connected agents

**Active:**
- Agent is actively handling this conv_id (has written at least one reply)
- Locked to this agent's conn. All register attempts are rejected.
- Remains active until the conn dies (disconnect, ping timeout)
- On conn death → returns to unregistered

## Query routing flow

1. Poe POST arrives with conv_id + message_id
2. Relay emits SSE `meta` event immediately (5-second rule)
3. Lookup conv_id in registration map:
   - **Active:** forward query to agent's ws conn. Hold SSE stream open.
     Agent sends reply chunks back via ws. Relay streams them as SSE
     text events. Agent sends final=true → relay emits SSE done.
   - **Provisional (claimed, no real agent yet):** queue in lobby. Hold
     SSE stream open. When a real agent registers (claim=false), lobby
     queries are delivered.
   - **Provisional (real agent):** forward query to agent's ws conn.
     Same streaming as active.
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

### Dedicated agent (POE_CONV_ID set)

On startup:
1. Connect to relay via ws
2. Send `register` (claim=false) for that conv_id → gets lobby queries
3. Forward inbound queries from relay → fir via notifications/claude/channel
4. Forward fir's reply tool calls → relay as reply messages

### Catch-all agent (POE_CONV_ID empty)

A catch-all agent participates in the claim race for new conversations
and spawns dedicated fir instances.

On startup:
1. Connect to relay via ws (no auto-register)
2. Wait for `pending` notifications from the relay

On receiving `pending`:
1. Send `register(claim=true)` for the conv_id — race gate
2. If accepted: spawn a new fir instance (via bash/tmux) with
   `POE_CONV_ID=<conv_id>`. The new fir's agent will `register(claim=false)`
   and take ownership.
3. If rejected (`reason: "claimed"`): another agent won the race. Do nothing.

MCP tools available to fir:
- `reply(message_id, text, final)` — same as v1
- `register(conv_id)` — register this agent for a new conv_id

## Spawn flow

When a catch-all agent receives a `pending` notification, the bridge
claims the conv and posts a channel notification to the LLM. The
**LLM/skill** handles the actual spawn — this decouples the harness
(tmux, directory layout, fir flags) from the poe channel/relay.

### Bridge side (automatic)

1. Receive `pending` for conv_id
2. Send `register(conv_id, claim=true)` — race gate
3. If accepted: post channel notification with `type: "spawn"` containing
   conv_id, user_id, and first message content
4. If rejected: another agent won the race. Do nothing.

### LLM/skill side (harness-specific)

On receiving a `spawn` notification, the LLM (guided by a skill like
`poe-spawn`) executes the harness-specific spawn steps:

```bash
mkdir -p ~/.local/state/fir/agents/<conv_id>/.fir

cat > ~/.local/state/fir/agents/<conv_id>/.fir/mcp.json << 'EOF'
{
  "mcpServers": {
    "poe": {
      "command": "poe-bridge",
      "args": ["--agent", "ws://relay:9090/ws"],
      "env": { "POE_CONV_ID": "<conv_id>" }
    }
  }
}
EOF

tmux new-window -t agents "fir -c --session-name '<conv_id>' ~/.local/state/fir/agents/<conv_id>"
```

The new fir’s agent sends `register(conv_id, claim=false)`, overrides
the claim, gets lobby queries, and handles them.

**Crash recovery:** if the spawned fir dies, its conn dies, the
registration is removed, and the conv returns to unregistered. The next
Poe query re-triggers lobby → pending → claim → spawn. Poe provides
full history in each query, so the new fir has full context.

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
poe-bridge --relay

# Environment variables:
#   POE_ACCESS_KEY   — bearer secret from Poe bot dashboard
#   POE_HTTP_ADDR    — Poe HTTP listen address (default :8080)
#   POE_AGENT_PORT   — agent websocket port (default 9090)
#   POE_STATE_DIR    — state directory for access control persistence

# Agent mode
poe-bridge --agent <relay-ws-url>

# Environment variables:
#   POE_RELAY_URL    — alternative to positional arg
#   POE_CONV_ID      — lock to a single conversation (dedicated agent)
```

## Future: Tailscale Funnel for non-tailnet relay

Currently the relay listens on a plain HTTP port and expects a reverse
proxy (nginx, Caddy, Cloudflare tunnel, etc.) in front for TLS. The
agent ws port (:9090) is typically only reachable within the tailnet.

For deployments where the relay host is **not on the tailnet** — or
where you want zero-config public HTTPS without a separate reverse
proxy — [Tailscale Funnel](https://tailscale.com/kb/1223/funnel) can
expose the relay directly to the internet with automatic TLS.

### How it would work

A `funnel` package (previously prototyped, removed in cleanup) would:

1. Start a [`tsnet.Server`](https://pkg.go.dev/tailscale.com/tsnet)
   with a configured hostname (e.g. `poe-relay`)
2. Call `ListenFunnel("tcp", ":443")` to get a public HTTPS listener
3. Serve the Poe HTTP handler on that listener

This gives you a stable `https://poe-relay.<tailnet>.ts.net` URL that
you paste into the Poe bot dashboard — no nginx, no Caddy, no DNS
config. The relay binary is the entire stack.

### Config sketch

```
POE_FUNNEL=1                          # enable Funnel mode
POE_FUNNEL_HOSTNAME=poe-relay         # tsnet device name
POE_FUNNEL_STATE_DIR=~/.local/state/poe-funnel  # tsnet state
TS_AUTHKEY=tskey-auth-...             # one-time; state persists after first run
```

### When to implement

This becomes worthwhile when:
- The relay needs to run on a machine outside the tailnet
- You want to eliminate the reverse proxy from the deployment
- You need automatic TLS certificate management

The `tailscale.com/tsnet` dependency is ~55 indirect deps, so it
should only be pulled in when Funnel support is actually built.
