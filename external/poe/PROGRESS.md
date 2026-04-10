# Progress log — fir poe channel bridge

Reverse-chronological. Most recent entry on top. Each entry dated; findings,
decisions, and open questions recorded in-place so future sessions can pick
up without re-deriving context.

---

## 2026-??-?? — M3: real fir handoff

**Done**
- New package `internal/router` (`router.go`):
  - `Router` + `Chunk{Text, Final}` + `Register/Push/Unregister/Len`.
  - Per-entry `{ch, done}` design: Register returns a receive-only chunk
    channel and stashes a companion `done` channel. Unregister closes
    `done` but never closes `ch`, so there's no data race between a
    concurrent Push and Unregister. Push does `select { ch <- c;
    <-done }` and returns `ErrClosed` if Unregister wins the race, or
    `ErrUnknownMessage` if the id was never registered.
  - Register panics on a duplicate id — callers must ensure uniqueness.
- New package `internal/mcpnotify` (`notifier.go`):
  - The go-sdk does not currently expose a public API for sending
    arbitrary server→client JSON-RPC notifications (only NotifyProgress
    and LoggingMessage are wired). Since fir's channel protocol uses
    a custom `notifications/claude/channel` method, we work around this
    by wrapping the `mcp.Transport` handed to `Server.Run`:
    `Notifier.Wrap(inner)` returns a capturing Transport whose Connect
    method stashes the underlying `mcp.Connection` on the Notifier.
  - `Notifier.SendChannel(ctx, ChannelMessage{Content, Meta})` writes a
    `&jsonrpc.Request{Method: "notifications/claude/channel", Params:
    params}` directly on the captured connection. A Request with no ID
    is a notification per JSON-RPC 2.0, and the `Connection.Write`
    contract documents concurrent Write as permitted, so this is
    safe even while the MCP server is handling other traffic on the
    same connection.
  - `ChannelMessage` shape mirrors fir's `pkg/mcp/channel.go` exactly.
  - `ErrNotConnected` returned if SendChannel is called before the
    wrapped transport has Connected.
- `internal/poe/poe.go`: added an `OnQuery func(ctx, *QueryRequest,
  *SSEWriter) error` hook to `Handler`. `handleQuery` still emits the
  mandatory `meta` event first (preserving the 5-second-rule behaviour),
  then delegates to OnQuery if set, otherwise falls back to the M2
  canned echo. If OnQuery returns an error, handleQuery emits a
  best-effort `error` + `done` pair.
- `cmd/poe-bridge/main.go`: full wiring.
  - `newOnQuery(rt, notif, botName)` factory returns the production
    OnQuery closure: registers the message_id, pulls the latest user
    text out of the query history, sends a ChannelMessage to fir with
    meta carrying `{source: "poe", bot, user, user_id, conversation_id,
    message_id}`, then loops on the router channel draining Text chunks
    into SSE `text` events until a Final chunk arrives or the per-query
    50-minute budget expires. Always unregisters via defer. Takes the
    notifier through a small `channelSender` interface (a 1-method
    subset of `*mcpnotify.Notifier`) so tests can substitute a stub.
  - `newMCPServer(rt)` adds the real `reply` MCP tool with a typed
    `replyArgs{MessageID, Text, Final}` input schema; the handler calls
    `rt.Push` and returns an IsError tool result on `ErrUnknownMessage`,
    `ErrClosed`, or missing message_id.
  - Server capabilities now advertise the experimental `claude/channel`
    extension via `ServerOptions.Capabilities.AddExtension("claude/
    channel", …)`, so fir recognises this server as channel-capable.
  - `main` constructs the router + notifier, builds the `poe.Handler`
    with `OnQuery: newOnQuery(...)`, wraps the `StdioTransport` in
    `notif.Wrap(...)` before passing to `mcpSrv.Run`.
- Version bumped to `0.0.3-m3`.

**Tests added**
- `internal/router/router_test.go` (7 tests):
  - Register + Push + receive round-trip.
  - Push to unknown id → ErrUnknownMessage.
  - Unregister removes the entry and subsequent Push → ErrUnknownMessage.
    (The chunk channel is intentionally not closed — no race test.)
  - Unregister on never-registered id is a no-op.
  - Register twice panics.
  - Concurrent Push + Unregister → buffer-full Push blocks until
    Unregister releases it with ErrClosed (exercises the `select { ch
    <- c; <-done }` path).
  - 100 concurrent Pushes all delivered.
- `internal/mcpnotify/notifier_test.go` (4 tests):
  - `NotConnected` before Wrap → ErrNotConnected.
  - `Wrap` captures the connection; after Connect, SendChannel writes
    exactly one `*jsonrpc.Request` with method `notifications/claude/
    channel`, no ID (confirmed via `req.ID.IsValid()`), and a JSON
    params body that round-trips to the original `ChannelMessage`.
    Uses a hand-rolled recording Connection implementing
    `mcp.Connection`.
  - `Wrap` Connect error leaves the notifier disconnected.
  - 50 concurrent SendChannel calls all land.
- `cmd/poe-bridge/main_test.go` (new tests):
  - `CallReply_Ok`: register a message, call the `reply` tool over the
    in-memory MCP transport, assert a matching Chunk arrives on the
    router channel.
  - `CallReply_UnknownMessage`: tool returns IsError for a non-existent
    message_id.
  - `CallReply_MissingMessageID`: tool returns IsError when the arg is
    missing.
  - `OnQuery_EndToEnd`: real production `newOnQuery` closure against a
    stub `channelSender`; spins up an `httptest.Server` with a wired
    `poe.Handler`, POSTs a query, then (in a goroutine) waits for the
    handler to register on the router and pushes two text chunks + a
    final chunk. Asserts the resulting SSE body contains `meta` →
    `part-1` → `part-2` → `done` in order, and that the stub notifier
    received exactly one ChannelMessage with the expected content and
    meta.message_id. This is the full fir-to-poe round-trip minus the
    stdio/Connection plumbing.
  - `NewOnQuery_MissingMessageID`: the closure rejects queries with no
    message_id up front, without touching the router.
- `TestMCPServer_ListTools` updated to expect 2 tools (ping + reply)
  and checks both are present with non-empty descriptions.
- Helper `connectInMemory(t, rt)` now takes an optional router so tests
  that drive the reply tool can register ids on the same instance.

**Results**
- `make all` green:
  - gofmt clean, go vet clean, `staticcheck ./...` clean.
  - `go test -race ./...` passes all 4 packages.
  - **Total coverage 78.6%** (up from 75.7% in M2):
    - `internal/router`: **100.0%**
    - `internal/mcpnotify`: **94.7%** (uncovered: JSON marshal error
      path — would need a non-marshalable payload)
    - `internal/poe`: 82.4% (unchanged; new `OnQuery` delegate path
      hits `handleQuery` coverage at 50% now because the OnQuery
      branch isn't exercised by poe's own tests, but is by
      cmd/poe-bridge's e2e)
    - `cmd/poe-bridge`: 64.6% (up from 57.5%; new tests hit
      `newOnQuery`, `newMCPServer`, the reply tool handler,
      `newHTTPHandler`, `rootHandler`, `pingResult`, `newHTTPServer`,
      `installShutdown`. Remaining uncovered is all inside `main`.)
  - Binary builds.

**Live smoke test (confirms wire format)**
Ran `POE_ACCESS_KEY=devkey POE_BOT_NAME=m3 ./bin/poe-bridge` with a
piped stdin and curl-POSTed a query to `/poe`. The bridge wrote exactly
this line to its stdout (= what fir's MCP stdio transport would read):

    {"jsonrpc":"2.0","method":"notifications/claude/channel","params":{"content":"hi","meta":{"bot":"m3","conversation_id":"c-1","message_id":"m-xyz","source":"poe","user":"u-1","user_id":"u-1"}}}

Shape matches `pkg/mcp/channel.go#ChannelMessage` in the fir repo
exactly. The curl POST then stayed blocked waiting for reply chunks
(correctly — nothing called the `reply` tool because nothing is
actually wired as the MCP peer in the smoke test). Clean shutdown on
stdin close.

**Findings**
- The go-sdk gap (no public custom-notification API) is real. Filing
  an upstream issue/PR is on the TODO list, but the transport-wrap
  workaround is stable and small enough to live with: if/when
  `ServerSession.Notify(method, params)` lands, the mcpnotify package
  collapses to a single wrapper call.
- The initial router design closed the chunk channel on Unregister.
  That tripped the race detector under concurrent Push+Unregister (the
  recover-from-send-on-closed-channel trick handles the panic but not
  the underlying data race flag). Rewrote to the `{ch, done}` pattern
  where Unregister only closes the `done` signal channel. Clean under
  `-race` now. This is the standard Go idiom for "unblock a blocked
  send without closing the channel."
- `jsonrpc.Request` with an empty `ID` is a notification per both the
  JSON-RPC 2.0 spec and the sdk's internal comment (`jsonrpc2.Request`
  "If it has an ID it is a call, otherwise it is a notification").
  Writing such a request directly with `Connection.Write` produces a
  well-formed notification on the wire.
- Fir doesn't appear to gate channel-message delivery on the
  `claude/channel` experimental capability (`pkg/mcp/channel.go`
  wraps every transport), but we advertise it anyway as the
  conventional handshake; fir's `channel_e2e_test.go` explicitly uses
  it so the cost of matching is zero.
- `len(q.Query) > 0` guard is important: a reported_feedback or
  stripped query would otherwise panic on the last-message indexing.
- The 50-minute query timeout leaves comfortable headroom under Poe's
  3600-second hard cap without holding connections open forever on
  a misbehaving fir session.

**Open questions**
- (carry-forward Q4) Permission queue UX: still deferred to M6.
- (carry-forward Q5) Tailscale auth key bootstrap: still deferred to
  M5.
- New: should the reply tool be idempotent / support out-of-order
  delivery? Current design is FIFO and fir must not send `final` more
  than once per message. Documenting this in the tool description is
  enough for now.

**Next**
- M4: pairing flow. On an unknown `user_id`, reply with a 6-char
  pending code + instructions, store it in a state dir (`POE_STATE_DIR`
  env var), add a terminal-side pair skill that the user runs to move
  pending → allowFrom. Channel-delivered approvals are refused.
  Concretely:
  - `internal/access/` package: `Store` loading/saving `access.json`
    with `allowFrom []string`, `pending map[code]{user_id, expires_at}`.
  - On first touch from an unknown user_id in `newOnQuery`, if the id
    is not in allowFrom, emit a canned SSE text with a fresh pending
    code instead of the channel notification.
  - `skills/poe-access/SKILL.md` + a terminal-side implementation.
  - Tests: new user → code flow, paired user → normal flow,
    channel-delivered `/pair` attempt → refused.

---

## 2026-??-?? — M2: poe protocol types, SSE writer, /poe handler

**Done**
- New package `internal/poe` (`internal/poe/poe.go`):
  - Protocol types: `BaseRequest`, `ProtocolMessage`, `QueryRequest`,
    `SettingsRequest`, `SettingsResponse`, `ReportReactionRequest`,
    `ReportErrorRequest`. `Identifier` is a string type alias —
    not validated structurally because the bridge has no reason to
    refuse a structurally-odd id Poe might mint.
  - `SSEWriter` with `NewSSEWriter(w)` + `WriteEvent(name, data)`.
    Sets `Content-Type: text/event-stream`, `Cache-Control: no-cache`,
    `Connection: keep-alive`, and `X-Accel-Buffering: no` (the last to
    suppress reverse-proxy buffering, which would break the 5-second
    first-event rule). Returns `ErrFlushUnsupported` if the underlying
    writer can't flush.
  - `Handler` (http.Handler) with `AccessKey` + `BotName` fields:
    - POST-only; non-POST → 405 with `Allow: POST`.
    - Bearer auth via constant-time compare (`crypto/subtle`). Empty
      `AccessKey` = dev mode (skip auth). Wrong scheme / wrong key →
      401.
    - 4 MiB body cap via `io.LimitReader`.
    - Dispatches on `type` field: `query` → SSE stream, `settings` →
      JSON `SettingsResponse`, `report_reaction|report_error|report_feedback`
      → 200 OK ack, unknown → 501 Not Implemented (per spec).
    - `handleQuery` emits `meta` first (5-second rule), then a single
      `text` event echoing the inbound `user_id`/`conversation_id`/
      `message_id` for debug visibility, then `done`. M3 will replace
      this with the real fir wiring.
- Wired into `cmd/poe-bridge/main.go`:
  - `newHTTPHandler` now takes a `poeHandler http.Handler` and registers
    it at `/poe`; `/` remains a plain health/version line.
  - `main` constructs `&poe.Handler{AccessKey: $POE_ACCESS_KEY, BotName:
    $POE_BOT_NAME}`. Empty access key falls back to dev mode (logged
    intent: never deploy without one).
  - Version bumped to `0.0.2-m2`.

**Tests added** (`internal/poe/poe_test.go`)
- `NewSSEWriter`: header smoke, `ErrFlushUnsupported` path via a
  hand-rolled non-flushing ResponseWriter.
- `SSEWriter.WriteEvent`: framing assertion (each event is
  `event: <name>\ndata: <json>\n\n`, exactly two `\n\n` terminators
  for two events), payload echoing, marshal-error path via a `chan int`.
- `Handler.MethodNotAllowed`: matrix over GET/PUT/DELETE/PATCH; checks
  status + `Allow` header.
- `Handler` auth: missing header / wrong key / wrong scheme (Basic) /
  dev mode (no key set).
- `Handler` dispatch: bad JSON → 400; unknown type → 501; settings →
  200 + `application/json` + decoded `SettingsResponse`; each report_*
  type → 200; query → SSE with mandatory ordering (`meta` index <
  `text` index < `done` index), bot name echoed, all three identifiers
  echoed.
- `Handler.Query_BadJSON`: well-formed envelope, ill-typed inner field
  → 400.
- `Handler.EndToEnd`: real `httptest.Server`, real http.Client POST
  with bearer, asserts the streamed body contains all expected markers.
- Test count: poe pkg now 14 tests; cmd/poe-bridge unchanged at 10.

**Results**
- `make all` green:
  - gofmt clean, `go vet` clean, `staticcheck ./...` clean.
  - `go test -race ./...` passes both packages.
  - **Total coverage 75.7%** (up from 56.4% in M1.2):
    - `internal/poe` package: **87.3%**
      - `NewSSEWriter` 100%, `checkAuth` 100%, `handleSettings` 100%
      - `WriteEvent` 85.7% (uncovered: error path on Fprintf write
        failure — exercising it requires a faulty ResponseWriter
        which adds boilerplate; deferred)
      - `ServeHTTP` 91.7% (uncovered: `io.ReadAll` error path)
      - `handleQuery` 61.5% (uncovered: the three `if err != nil`
        early-returns from `sse.WriteEvent` — same boilerplate cost
        as above; deferred)
    - `cmd/poe-bridge`: 57.5%, unchanged in spirit (only main is
      uncovered).
- End-to-end smoke verified by hand: ran `POE_ACCESS_KEY=devkey
  POE_BOT_NAME=smoke-bot ./bin/poe-bridge`, posted a query with curl,
  saw the streamed `event: meta` / `event: text` / `event: done` arrive
  correctly. Missing bearer → 401. `/` → health line.

**Findings**
- The Poe spec's 5-second first-event rule is trivially satisfied by
  emitting `meta` synchronously at the top of `handleQuery`. The real
  challenge will hit in M3 when fir is the source of the text events
  and may take longer than 5s to start streaming — the meta-first
  pattern is exactly what saves us there.
- `crypto/subtle.ConstantTimeCompare` is the right primitive for the
  bearer check; without it the access key is a timing-leaky string
  comparison.
- `io.LimitReader` + a hard 4 MiB cap is enough headroom for the 1000-
  message context window plus metadata, while protecting against
  oversize POSTs.
- `httptest.NewRecorder()` doesn't implement `http.Flusher` in older
  Go versions, but does in current ones (its `*ResponseRecorder` has a
  no-op Flush). Confirmed the SSE tests work against it. For testing
  the no-flusher failure path I added a hand-rolled `fakeNonFlusher`.
- `X-Accel-Buffering: no` is a defensive header for nginx-style
  proxies; harmless when no proxy is in front. Worth keeping for the
  eventual relay.

**Open questions resolved**
- (carry-forward Q4) Permission queue UX: still deferred to M6, no
  decision needed yet.
- (carry-forward Q5) Tailscale auth key bootstrap: still deferred to
  M5.

**Next**
- M3: replace `handleQuery`'s hardcoded body with a real handoff to the
  fir MCP side. Concretely:
  - Add a `pendingRequests map[Identifier]chan PartialResponse`
    keyed by `message_id`, guarded by a mutex.
  - On each query: register a channel, emit meta, emit
    `notifications/claude/channel/message` to the MCP peer, then loop
    draining the channel into `text` events until a `done` chunk
    arrives or the 3600-second budget expires.
  - Add a `reply` MCP tool with input
    `{message_id, text, final}`; handler pushes a chunk on
    `pendingRequests[message_id]`.
  - Tests: simulate fir's side by calling the tool from an in-memory
    MCP client while a fake Poe POST is in flight; assert end-to-end
    delivery.

---

## 2026-??-?? — M1.2: unit tests, coverage, Makefile

**Done**
- Added `cmd/poe-bridge/main_test.go` with unit + integration tests:
  - `rootHandler` — status, content-type, body content, method matrix.
  - `newHTTPHandler` — real `httptest.Server` round-trip on `/` and on an
    unknown path (locks in the current catch-all behaviour so the M2 `/poe`
    switch is explicit).
  - `pingResult` — content type + payload.
  - `newHTTPServer` — Addr / Handler / ReadHeaderTimeout sanity.
  - `installShutdown` — two flavours: triggered by a signal on sigCh, and
    triggered by ctx cancel. Both assert the goroutine exits within 3s and
    (for the signal case) that ctx is cancelled afterwards.
  - MCP end-to-end over `mcp.NewInMemoryTransports()`: spin up the server,
    connect a test client, `ListTools` → 1 tool, `CallTool("ping")` →
    TextContent containing "pong" + version.
- Refactored `main.go` to extract `newHTTPServer(addr, h)` and
  `installShutdown(ctx, cancel, httpSrv, sigCh)` so main is thin glue and
  the testable surface is maximised.
- Added `Makefile` with targets:
  - `default` (= `fmt-check vet test build`)
  - `all` (= `fmt-check vet test-race cover build`) — CI-shaped pipeline
  - `build`, `run`, `test`, `test-race`, `cover`, `cover-html`
  - `vet`, `fmt`, `fmt-check`, `tidy`, `clean`, `help`
  - `help` auto-generates from `## ` doc comments on targets.
- `.gitignore` extended for `bin/`, `coverage.out`, `coverage.html`.

**Results**
- `make all` green:
  - gofmt clean, `go vet` clean.
  - `go test -race ./...` — 10 tests, all pass.
  - `go test -covermode=atomic -coverprofile=coverage.out ./...` — **56.4%**
    total, **100%** on every function except `main` itself (which is a
    ~25-statement signal/listen/log glue and intentionally left
    unit-test-free; it's exercised by the end-to-end smoke test instead).
    Functions at 100%: `newHTTPHandler`, `rootHandler`, `pingResult`,
    `newMCPServer`, `newHTTPServer`, `installShutdown`.
  - Binary builds to `bin/poe-bridge`.

**Findings**
- `mcp.NewInMemoryTransports()` is the idiomatic way to test server + client
  without stdio or sockets. It returns two InMemoryTransports backed by a
  `net.Pipe()`; one is handed to `server.Run`, the other to `client.Connect`.
  Clean and fast (single in-memory test finishes in <10ms).
- The SDK's generic `mcp.AddTool[In, Out]` auto-derives JSON schema from the
  Go struct tag `jsonschema:"…"`; for a zero-arg tool an empty struct is fine.
- Main's 0% coverage is expected and not worth contorting the code for. If
  that changes we'll add a small `run(ctx)` wrapper that main delegates to
  once there's enough inside to warrant testing it independently.

**Next**
- M2 (unchanged plan): Poe protocol types + SSE writer, `/poe` POST handler
  with bearer auth, immediate meta event, hardcoded text+done reply. Tests
  will cover the bearer check (pass/fail), the 5-second-rule meta event
  emission, SSE framing, and JSON parsing for `query` / `settings` /
  `report_reaction` / `report_error` / unknown (501).

---

## 2026-??-?? — M1.1: switch to official MCP Go SDK

**Done**
- Replaced `github.com/mark3labs/mcp-go` with the official
  `github.com/modelcontextprotocol/go-sdk v1.5.0` (package `mcp`).
- Rewrote `cmd/poe-bridge/main.go` to use the official API shape:
  - `mcp.NewServer(&mcp.Implementation{Name, Version}, nil)` instead of
    `server.NewMCPServer(name, version, opts...)`.
  - Generic `mcp.AddTool[In, Out]` which auto-derives JSON schema from the
    Go struct — `pingArgs struct{}` is enough for a zero-arg tool.
  - Tool handler signature is now
    `func(ctx, *mcp.CallToolRequest, In) (*mcp.CallToolResult, any, error)`
    returning a CallToolResult with typed `Content` (e.g. `&mcp.TextContent{}`).
  - `server.Run(ctx, &mcp.StdioTransport{})` instead of `server.ServeStdio`.
    Run takes a context, so shutdown is now context-cancel driven rather
    than stdin-close driven — cleaner.
- Rewired signal handling around the root context: SIGINT/SIGTERM cancels
  ctx, which unblocks both the HTTP server shutdown and Server.Run.
- `go mod tidy` dropped mcp-go and its transitive deps; go.mod now has a
  single MCP require: `github.com/modelcontextprotocol/go-sdk v1.5.0`.

**Smoke test passed**
- `go vet ./...` clean.
- `go build` succeeds.
- Ran the binary with a piped stdin, curl `http://127.0.0.1:8080/` →
  200 OK `poe-bridge 0.0.1-m1 ok`. Exited cleanly on stdin close.

**Findings**
- Official SDK is ergonomically nicer for our use case: generics mean we'll
  get automatic schema for the future `reply` tool's `{user_id, conversation_id,
  message_id, text, final}` struct for free.
- `TextContent` lives at `mcp.TextContent` (not under a sub-package).
- `Server.Run` is context-aware → shutdown is by cancelling ctx, so we no
  longer need the "close stdin to unblock ServeStdio" hack. Simpler lifecycle.
- Transitive dep footprint shifted: picked up `segmentio/encoding`,
  `segmentio/asm`, `golang.org/x/oauth2`, `golang.org/x/sys`. All are
  standard in the Go ecosystem; none are runtime surprises.

---

## 2026-??-?? — M1: skeleton binary, stdio+http coexisting

**Done**
- `go mod init github.com/kfet/fir/external/poe` — separate module as planned,
  does not touch fir's root go.mod.
- Added `github.com/mark3labs/mcp-go v0.47.1` as the MCP server dep.
- `cmd/poe-bridge/main.go` — skeleton with both I/O surfaces live:
  - MCP stdio server via `server.NewMCPServer` + `server.ServeStdio`, with a
    placeholder `ping` tool so `tools/list` returns something.
  - Plain `net/http` server on `:8080`, `/` returns `poe-bridge <version> ok`.
  - Graceful shutdown on SIGINT/SIGTERM: cancels the HTTP server and closes
    stdin so ServeStdio returns.
  - `log.SetOutput(os.Stderr)` — stdout belongs to the MCP stdio transport.
- `.gitignore` for build output.

**Smoke test passed**
- `go vet ./...` clean.
- `go build -o /tmp/poe-bridge ./cmd/poe-bridge` — builds.
- Ran the binary with a dummy stdin and hit `http://127.0.0.1:8080/` — got
  `HTTP/1.1 200 OK` with `poe-bridge 0.0.1-m1 ok`. Binary logged startup and
  exited cleanly on stdin close.

**Findings**
- mcp-go v0.47.1's `NewMCPServer` + `ServeStdio` is the right API; no surprises.
  `WithToolCapabilities(true)` is needed if we want the server to advertise
  tool support in initialize.
- ServeStdio blocks on stdin read, so when launching from a non-interactive
  shell the parent must keep stdin open or the process exits immediately.
  Confirmed by the first smoke-test attempt failing (backgrounded with no
  stdin pipe), fixed by piping `sleep 3` into it.
- Binary size: not measured; irrelevant until tsnet lands (tsnet adds a lot).

**Open questions carried forward**
- Q4 (permission queue UX) and Q5 (tailscale auth key bootstrap) still open —
  both belong to later milestones (M6 and M5 respectively), will address then.

**Next**
- M2: Poe protocol types + SSE writer. Replace the `/` placeholder with a
  `/poe` POST handler that:
  - Verifies `Authorization: Bearer <POE_ACCESS_KEY from env>`.
  - Parses the JSON request into a typed `QueryRequest` (and settings,
    report_reaction, report_error as 501 stubs).
  - Immediately writes a `meta` SSE event (5-second rule) + a hardcoded
    `text` event + `done`, so `curl -X POST` can round-trip against it.
  - No fir wiring yet — that's M3.

---

## 2026-??-?? — M0: design committed

**Done**
- Worktree created: `/Users/kfet/dev/ai/fir-wt-poe-bridge` on branch
  `wt/poe-bridge` off `main@02f253a`.
- `external/poe/README.md` written with goals, non-goals, architecture,
  verified Poe protocol facts, pairing model, state layout, repo layout,
  milestones.
- This progress log initialized.

**Decisions locked in**
- **Language: Go.** Rationale: static binary (trivial deploy across tailnet
  boxes), `tailscale.com/tsnet` gives in-process Funnel with no host config,
  matches fir's codebase and tooling, Poe protocol is small enough
  (~200 LOC) that fastapi_poe's head-start doesn't justify the Python
  deployment cost.
- **Location: `external/poe/` inside the fir repo.** Separate `go.mod` so it
  ships as its own binary with its own release cadence — does not bloat
  the main fir binary. Keeps it close to fir's conventions and CI without
  coupling the build. (Alternative considered: put it under
  claude-plugins-official's `external_plugins/`. Rejected for v1 because
  that repo is Node/TS-centric and we want Go tooling first-class. Can
  move later if the other bridges matter more than the Go ergonomics.)
- **Pairing model C: per-user allowlist + per-conversation routing.**
  user_id is paired once; every conversation_id from that user is auto-allowed
  and surfaced to fir as a distinct thread via channel meta. This is the
  headline feature Poe enables that Telegram DMs don't.
- **No server push in v1.** Permission requests queue on disk and drain on
  the user's next inbound message. Telegram bridge stays in place for
  true async push; poe complements rather than replaces it.
- **One bot per worktree in v1.** Multi-worktree-under-one-bot via a
  tailnet-hosted relay is explicitly deferred to v2 post-M9.
- **tsnet Funnel over host `tailscale funnel`.** Bridge joins the tailnet
  as its own device, calls `ListenFunnel`, gets public HTTPS with a LE cert,
  no host config beyond an initial tailnet auth key in the state dir.

**Findings from the protocol verification pass**
- Spec is stable and short. No surprises vs memory except two hard limits
  that shape the code:
  - **5-second first-event rule.** Handler MUST emit a `meta` SSE event
    immediately on POST arrival, BEFORE waiting for fir. This is why the
    per-message reply channel design exists: handler owns the SSE lifecycle
    independently of when/whether fir responds.
  - **conversation_id resets on context clear.** user_id stays stable, so
    pairing on user_id is correct. But any per-conversation state fir
    accumulates (e.g. plan, scratchpad) evaporates on `/clear` and that's a
    user-facing UX fact we should surface somewhere.
- Attachment URLs expire after 10 minutes. If we ever download them (post-v1),
  it must happen inside the request handler's lifetime, not lazily.
- No websockets anywhere. Relay design (v2) will use SSE or ws over the
  tailnet leg; the public leg is forced to be SSE.

**Open questions**
1. `external/` placement — double-check with kfet whether this belongs in
   fir's tree or claude-plugins-official. Current pick: fir/external/poe.
   Easy to move if wrong.
2. Module path — proposed `github.com/kfet/fir/external/poe` vs a fresh
   `github.com/kfet/fir-poe-bridge` repo. Leaning toward the in-tree path
   for now so worktree tooling + PR flow stay uniform.
3. Should the `reply` MCP tool name collide with telegram's `mcp__telegram__reply`
   or get its own namespace (`mcp__poe__reply`)? Almost certainly its own —
   fir can have both bridges loaded simultaneously in different worktrees and
   tool namespaces shouldn't collide.
4. Permission queue UX — prepend to reply text, or send as a separate SSE
   text event before the fir reply? Probably separate event for cleanliness.
5. Tailscale auth key handling — bridge needs a tailnet auth key on first
   run to join the tailnet. Reusable vs one-time? Stored where? Probably
   `<state_dir>/tsnet/` owned by tsnet itself after bootstrap. Research
   needed at M5.

**Next**
- M1: `go mod init`, skeleton `cmd/poe-bridge/main.go` that opens an MCP
  stdio server via mcp-go and serves a plain `200 OK` on HTTP :8080 for
  smoke testing. No tsnet yet, no Poe shape yet.
