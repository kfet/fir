# fir poe channel bridge

A Poe.com server-bot ↔ fir MCP channel bridge, written in Go.

Brings the same "chat with your fir session from a phone" UX the telegram bridge
provides, but routed through Poe's server-bot protocol instead of Telegram's bot
API. The killer advantage vs the telegram DM bridge: Poe gives you a stable
`conversation_id` on every request, so one bot can serve **multiple independent
task threads** for the same user — solving the telegram DM limitation of "one
thread per (user, bot) pair."

Status: **v0.2.0 — relay mode, agent mode, multi-conversation routing, live on krpi2one.**
and logs progress/findings as we go.

---

## Goals

1. **Inbound**: user sends a message in a Poe chat → fir receives it via the
   MCP `notifications/claude/channel/message` notification, with `user_id` and
   `conversation_id` carried through in meta so fir can treat separate Poe
   chats as separate threads.
2. **Outbound replies**: fir calls a `reply` MCP tool; the bridge streams the
   text back as the SSE response to Poe's open POST.
3. **Pairing**: first message from an unknown Poe `user_id` triggers a pending
   code; user runs a slash command in their real fir terminal session to
   approve. Channel-delivered approvals are refused (injection risk).
4. **Per-worktree isolation**: the bridge runs inside an MCP process spawned by
   one fir worktree, with its own state dir (`TELEGRAM_STATE_DIR`-style env
   var), its own access.json, its own Poe bot token. Multiple worktrees = multiple
   bots, same as the telegram model. Multi-worktree-under-one-bot is a later
   relay phase.
5. **Zero host configuration for public HTTPS**: use `tailscale.com/tsnet` to
   embed a tailnet node directly in the bridge binary and call
   `ListenFunnel` to get a public HTTPS listener with a valid cert. No separate
   `tailscale funnel` dance on the host.

## Non-goals (v1)

- **No server→user push.** Poe has no mechanism for bot-initiated messages.
  Permission requests from fir will be **queued** and drained on the user's
  next inbound message. Async push notifications stay on the telegram bridge.
- **No multi-worktree routing.** v1 is one bot per worktree (one Poe bot
  dashboard entry, one access key). Multi-worktree-under-one-bot via a
  tailnet-hosted relay is a deliberate v2.
- **No group chats / bot-to-bot.** Single user per bridge instance.
- **No attachments round-trip** in v1. Poe sends attachment URLs (10-minute
  expiry); we'll plumb them through as metadata but not download them in v1.

## Architecture

```
  ┌──────────┐   HTTPS POST (query)      ┌────────────────────────┐
  │   Poe    │ ─────────────────────────▶│  bridge binary (Go)    │
  │ servers  │ ◀──────── SSE ────────────│  - tsnet Funnel :443    │
  └──────────┘                           │  - /poe POST handler    │
                                         │  - SSE response writer  │
                                         │  - per-msg reply queue  │
                                         │  - mcp-go stdio server  │
                                         │  - access.json / pairing│
                                         └────────────┬────────────┘
                                                      │ stdio
                                                      ▼
                                              ┌──────────────┐
                                              │   fir MCP    │
                                              │   (worktree) │
                                              └──────────────┘
```

Single Go binary. Two I/O surfaces:

- **stdio** → MCP server (mcp-go) speaking to fir. Exposes the `reply` tool and
  the standard channel notification types.
- **HTTPS :443 via tsnet Funnel** → Poe protocol endpoint. Receives `query`,
  `settings`, `report_reaction`, `report_error` POSTs; returns SSE.

The bridge between the two is a map:

```
pendingRequests: map[message_id] → chan PartialResponse
```

When Poe POSTs a `query`, the handler:
1. Verifies `Authorization: Bearer <access_key>`.
2. Checks `access.allowFrom` against `user_id`. If absent, enters the pairing
   flow and responds with a code + instructions (single SSE event + done).
3. Creates an entry in `pendingRequests[message_id]`, emits an immediate `meta`
   SSE event (to satisfy Poe's 5-second first-event rule), then loops on the
   channel draining `PartialResponse` chunks into `event: text` SSE writes.
4. On fir's `done` call, emits `event: done` and closes the stream.
5. Enforces the 3600-second total timeout; on overrun, emits `event: error`.

When fir calls the `reply` MCP tool with `{conversation_id, user_id, message_id, text}`:
1. Look up `pendingRequests[message_id]`.
2. Push chunks onto the channel.
3. When `final: true`, close the channel.

Inbound → fir notification path:
- On receiving a valid `query` from an allowlisted user, emit
  `notifications/claude/channel/message` with meta containing
  `{source: "poe", user_id, conversation_id, message_id, bot: <bot_name>}`.
- fir sees `[Channel message from <user> via poe]` with conv/user_id in meta.
  fir's reply tool call routes back via `message_id`.

Permission-request queue:
- When fir emits `notifications/claude/channel/permission_request`, the bridge
  appends to a per-user queue on disk.
- On the next inbound `query` from that user, the bridge prepends the queue
  contents to the reply stream as a text block: `🔐 <n> pending permission
  requests: ...` with a hint to respond `/allow <req_id>` or `/deny <req_id>`.
- User's reply text is parsed for those commands BEFORE being forwarded to fir,
  so permission decisions don't pollute the conversation context.

## Poe protocol facts (verified)

Source: creator.poe.com/docs/server-bots/poe-protocol-specification +
github.com/poe-platform/fastapi_poe types.py.

- Transport: HTTP POST + SSE response. No websockets.
- Auth: `Authorization: Bearer <access_key>` (32 chars).
- Identifiers are tagged: `u-…` user, `c-…` conversation, `m-…` message, `d-…`
  metadata. Match regex `^[a-z]{1,3}-[a-z0-9=]{32}$`.
- Request types: `query`, `settings`, `report_reaction`, `report_error`, plus
  deprecated `report_feedback`. Unknown types → 501.
- `query` body carries: `query[]`, `user_id`, `conversation_id`, `message_id`,
  `metadata`, `attachments`, `parameters`.
- `conversation_id` is stable per chat BUT **resets when context is cleared**.
  user_id stays stable across resets.
- Hard limits:
  - First SSE event within **5 seconds**.
  - Response complete within **3600 seconds**.
  - ≤ 512,000 total chars, ≤ 10,000 events.
  - Context may be truncated past 1000 messages.
  - Attachment URLs expire **10 minutes** after request.
- SSE event types: `meta`, `text`, `replace_response`, `suggested_reply`,
  `error`, `done`. `meta` should be first.
- No server→user push anywhere in the spec. Confirmed.

## Pairing model (chosen: C — per-user allowlist, per-conversation routing)

- Pair ONCE on `user_id`. First message from an unknown user_id:
  - Bridge generates a 6-char code, stores `pending[code] = {user_id, expires_at}`.
  - Responds with SSE text: "Not paired. In your fir terminal, run:
    `/poe:access pair <code>`".
- User runs the pair skill in their real fir terminal session. The skill reads
  `pending/`, moves the entry into `access.allowFrom`, writes an ack file the
  bridge polls.
- Each `conversation_id` is surfaced to fir as a distinct thread via the channel
  meta. Multiple parallel tasks under one user = multiple conversations = fir
  sees them as separate threads. This is the multi-thread feature telegram DMs
  can't provide.
- Channel-delivered approvals are **refused** — the pair skill must be invoked
  from the user's actual terminal session, never from a message coming in
  through the bridge (prompt-injection defense, same rule as the telegram
  bridge).

## State directory layout

Mirrors the telegram bridge. Set via `POE_STATE_DIR` env var; without it the
bridge refuses to start (no shared global fallback — every worktree must be
explicit).

```
<state_dir>/
  .env                  # POE_ACCESS_KEY=...  POE_BOT_NAME=...
  access.json           # { "allowFrom": [user_id…], "pending": {code: {...}} }
  approved/<user_id>    # written by pairing skill, polled by bridge
  queue/<user_id>.jsonl # pending permission_requests for this user
  tsnet/                # tsnet state (tailnet identity, prefs)
```

## Runtime dependencies

- Go 1.22+ (matches fir).
- `tailscale.com/tsnet` — embedded tailnet node + Funnel listener.
- `github.com/mark3labs/mcp-go` — MCP server over stdio.
- stdlib `net/http`, `encoding/json`, `crypto/rand` — Poe protocol is small
  enough that no HTTP framework is needed.

## Repo layout (proposed)

```
external/poe/
  README.md             # this file
  PROGRESS.md           # running log of what's done, what's next, open questions
  go.mod                # separate module (external to fir's main module)
  cmd/poe-bridge/main.go
  internal/
    poe/                # Poe protocol types + SSE writer
    mcpsrv/             # mcp-go wiring: reply tool, inbound notifications
    access/             # access.json + pending/approved pairing
    funnel/             # tsnet listener setup
    queue/              # on-disk permission queue
  skills/
    poe-bot-setup/SKILL.md     # how to provision a new bridge for a worktree
    poe-access/SKILL.md        # `pair <code>` terminal-side command
```

The module lives under `external/` inside the fir repo so it can evolve
alongside fir but ships independently (separate go.mod = separate binary,
separate release cadence, no bloat to the main fir binary). If it proves out
we can decide later whether to fold it into fir proper as a builtin.

**Open question**: should this live in fir's `external/` or as a subdir in
claude-plugins-official's `external_plugins/`? The telegram bridge is in the
latter. Putting it in fir/external keeps it close to mcp-go integration and
lets it use fir's CI; putting it in claude-plugins-official keeps all channel
bridges together. See PROGRESS.md for the working decision.

## Milestones

- [x] M0: design doc + progress log committed (this commit).
- [x] M1: Go module + skeleton `cmd/poe-bridge/main.go` that speaks MCP stdio
      (mcp-go hello world) and serves a hardcoded 200 OK on HTTP :8080 (not
      yet Poe-shaped, not yet tsnet).
- [x] M2: Poe protocol types + SSE writer; handle `query` with a hardcoded
      "hello from fir" reply. Run locally with `curl` posing as Poe.
- [x] M3: Wire the `reply` MCP tool to the SSE queue. End-to-end: fir says
      hello, curl sees it stream back.
- [x] M4: Pairing flow (pending → /poe:access pair → allowFrom). Refuse
      channel-delivered approvals.
- [x] M5: tsnet Funnel integration. Register a real Poe bot against the
      `<machine>.ts.net` URL. First real end-to-end message from Poe app.
- [x] M6: Permission-request queue + drain on next inbound.
- [x] M7: Multiple conversation_id threads verified in practice.
- [x] M8: `poe-bot-setup` skill mirroring telegram-bot-setup.
- [x] M9: docs + release.

Relay (multi-worktree under one bot) is deliberately post-M9, tracked as a
separate v2 effort.

## Modes

The binary supports three modes:

### v1 (default): single bridge
```
poe-bridge
```
One bridge, one fir, one Poe bot. Env: `POE_ACCESS_KEY`, `POE_BOT_NAME`, `POE_STATE_DIR`.

### Relay mode
```
poe-bridge --relay
```
Poe HTTP frontend (:8080) + agent websocket listener (:9090). Routes by conversation_id. Env: `POE_ACCESS_KEY`, `POE_BOT_NAME`, `POE_STATE_DIR`, `POE_AGENT_PORT`.

### Agent mode
```
poe-bridge --agent ws://relay:9090/ws
```
Connects to relay, registers for `POE_CONV_ID` (or catch-all if empty). Bridges relay ↔ fir MCP. Env: `POE_RELAY_URL`, `POE_CONV_ID`.

## Conversation history

When Poe sends a query, it includes the full conversation history (up to 1000
messages) in `query[]`. The bridge handles this at two levels:

### Bridge side (agent mode)

The agent always passes `query[]` as `meta["history"]` in the channel
notification to fir. The bridge is a dumb pipe — it makes no decisions about
whether or how to use history. The latest user message is extracted and sent
as the notification content; everything else rides in meta.

### Fir side

Fir's `formatMeta` in `pkg/mcp/inject.go` excludes `history` from the
message header (it would be too large). The raw history is available in
`cm.Meta["history"]` for any fir code or skill that wants to inspect it.

**Session continuity model:**

- Each Poe conversation maps to a fir agent in its own directory
- `fir -c` (continue) resumes the session from disk after restart
- `meta.history` from Poe is a **fallback** for when no local session exists
  (e.g. first start, or new box without shared storage)
- Cross-box session sharing is a future enhancement
