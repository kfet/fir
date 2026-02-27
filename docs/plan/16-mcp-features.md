# 16 — MCP Feature Implementation Plan (Exhaustive)

## 1. Current Implementation

What fir supports today in `pkg/mcp/`:

| Feature | Status | Files |
|---|---|---|
| Stdio command transport | ✅ | `client.go` — `commandTransport()` |
| Connect to MCP servers | ✅ | `client.go` — `Manager.Start()` |
| List tools | ✅ | `client.go` — `startServer()` |
| Call tools | ✅ | `tool_adapter.go` — `AdaptTool()` |
| Text content results | ✅ | `tool_adapter.go` — `convertResult()` |
| Image content results | ✅ | `tool_adapter.go` — `convertResult()` |
| Tool name namespacing | ✅ | `tool_adapter.go` — `mcp__<server>__<tool>` |
| Config from `.fir/mcp.json` | ✅ | `config.go` — `LoadConfigFile()` |
| Graceful session close | ✅ | `client.go` — `Manager.Close()` |
| In-memory transport testing | ✅ | `client_test.go`, `testserver_test.go` |

**Not implemented:** Everything else in the MCP spec. The SDK (v1.3.1) provides client-side support for all of the features below.

---

## 2. Feature Gap Analysis

### 2.1 Client-Side Features (fir connecting TO MCP servers)

#### A. Resources (`ListResources`, `ReadResource`, `ListResourceTemplates`)
**SDK support:** `ClientSession.ListResources()`, `ReadResource()`, `ListResourceTemplates()`, `Resources()` iterator, `ResourceTemplates()` iterator.

MCP servers can expose files, database records, API responses as "resources" — URIs the client can read. Resources could be injected into the LLM context as system prompt fragments or made available as a tool ("read_resource").

#### B. Resource Subscriptions (`Subscribe`, `Unsubscribe`)
**SDK support:** `ClientSession.Subscribe()`, `Unsubscribe()`. Plus `ResourceUpdatedHandler` in `ClientOptions`.

Subscribe to resource changes and get `notifications/resources/updated`. Could trigger re-reads and context refresh.

#### C. Prompts (`ListPrompts`, `GetPrompt`)
**SDK support:** `ClientSession.ListPrompts()`, `GetPrompt()`, `Prompts()` iterator.

MCP servers can expose reusable prompt templates. These could be offered as slash commands or injected into system prompts.

#### D. Completions (`Complete`)
**SDK support:** `ClientSession.Complete()`.

Argument autocompletion for prompt arguments and resource URIs. Useful for interactive mode.

#### E. Logging (`SetLoggingLevel`)
**SDK support:** `ClientSession.SetLoggingLevel()`. Plus `LoggingMessageHandler` in `ClientOptions`.

Receive log messages from MCP servers, display or route them.

#### F. Sampling / CreateMessage (server requests LLM call)
**SDK support:** `ClientOptions.CreateMessageHandler`.

An MCP server can request that the client sample from an LLM (i.e., the server asks fir to make an AI call). This is a powerful agentic feature — it lets MCP servers be "inner agents."

#### G. Elicitation (server requests user input)
**SDK support:** `ClientOptions.ElicitationHandler`, `ElicitationCompleteHandler`.

An MCP server can ask the user a question through the client. Requires UI integration.

#### H. Roots (exposing filesystem roots to server)
**SDK support:** `Client.AddRoots()`, `Client.RemoveRoots()`. Sends `notifications/roots/list_changed`.

Tell MCP servers what directories they can operate on. Important for filesystem-oriented servers.

#### I. Progress Notifications
**SDK support:** `ClientOptions.ProgressNotificationHandler`, `ClientSession.NotifyProgress()`.

Receive progress updates from long-running tool calls. Display in UI.

#### J. Tool List Changed Notifications
**SDK support:** `ClientOptions.ToolListChangedHandler`.

Server can notify that its tool list changed. Client should re-list tools and update the agent's tool set dynamically.

#### K. Prompt/Resource List Changed Notifications
**SDK support:** `ClientOptions.PromptListChangedHandler`, `ClientOptions.ResourceListChangedHandler`.

Similar dynamic updates for prompts and resources.

#### L. Ping / Keepalive
**SDK support:** `ClientSession.Ping()`, `ClientOptions.KeepAlive`.

Health checking and automatic session cleanup.

#### M. Cancellation
**SDK support:** Built into the SDK via context cancellation → `notifications/cancelled`.

Already works through Go context propagation, but we should verify it behaves correctly.

### 2.2 Transport Features

#### N. SSE Transport (deprecated but common)
**SDK support:** `SSEClientTransport`.

Connect to MCP servers over HTTP+SSE instead of stdio. Many hosted MCP servers use this.

#### O. Streamable HTTP Transport
**SDK support:** `StreamableClientTransport`.

The modern replacement for SSE transport. Supports bidirectional streaming over HTTP.

### 2.3 Server-Side Features (fir AS an MCP server)

#### P. Expose fir tools as MCP server
**SDK support:** Full `mcp.Server` + `mcp.AddTool` API.

Let external MCP clients call fir's built-in tools (file read, bash, etc.) through MCP. Would enable fir to be used as a "tool provider" for other agents.

#### Q. Expose fir as MCP sampling server
Could let other tools request LLM calls through fir.

### 2.4 Configuration & UX

#### R. Multiple config file sources
Load MCP configs from: project `.fir/mcp.json`, user `~/.fir/mcp.json`, ACP `session/new` params, CLI flags.

#### S. Config file watching / hot reload
Detect changes to `mcp.json` and reconnect servers.

#### T. Server health status & error reporting
Surface connection failures, tool errors, and disconnects to the user.

#### U. Pagination support
**SDK support:** `ListToolsParams` etc. have cursor fields. `Tools()`, `Resources()`, `Prompts()` iterators handle pagination automatically.

We currently don't paginate — we call `ListTools` once. Should use the iterator or handle `NextCursor`.

---

## 3. Prioritized Task Breakdown

### Priority 1: High-value, frequently needed

#### Task 1: SSE/HTTP Transport Support [M]
**Why:** Many MCP servers are HTTP-based (Smithery, hosted services). Blocking adoption.

**Files:**
- `pkg/mcp/config.go` — add `Transport` field to `ServerConfig` (`"stdio"` | `"sse"` | `"streamable"`) plus `URL` field
- `pkg/mcp/client.go` — update `commandTransport()` → `createTransport()` to handle all types
- `pkg/mcp/config_test.go` — test new config variants
- `pkg/mcp/client_test.go` — test SSE/streamable transport creation

**Tests:** Unit test config parsing for all transport types. Integration test with an in-process streamable HTTP server using the SDK's `StreamableServerTransport`.

**Commit:** `feat(mcp): support SSE and streamable HTTP transports`

---

#### Task 2: Roots Support [S]
**Why:** Filesystem MCP servers need roots to know their scope. Simple to implement.

**Files:**
- `pkg/mcp/client.go` — accept `[]Root` in `NewManager`, call `client.AddRoots()` before connect
- `pkg/mcp/config.go` — add optional `Roots` to config or derive from project directory

**Tests:** Verify roots are advertised during initialization.

**Commit:** `feat(mcp): advertise filesystem roots to MCP servers`

---

#### Task 3: Tool List Changed Notifications [S]
**Why:** Dynamic tool updates are important for long-running sessions where servers evolve.

**Files:**
- `pkg/mcp/client.go` — set `ToolListChangedHandler` in `ClientOptions`, re-list tools and update agent
- `pkg/mcp/client.go` — add `OnToolsChanged` callback to `Manager`

**Tests:** Test that a tool list change notification triggers re-enumeration.

**Commit:** `feat(mcp): handle dynamic tool list changes`

---

#### Task 4: Logging Support [S]
**Why:** Essential for debugging MCP server issues. Easy to implement.

**Files:**
- `pkg/mcp/client.go` — set `LoggingMessageHandler` in `ClientOptions`, route to `slog`
- `pkg/mcp/client.go` — call `SetLoggingLevel` based on fir's verbosity

**Tests:** Verify log messages from server are captured.

**Commit:** `feat(mcp): receive and display MCP server log messages`

---

#### Task 5: Progress Notifications [S]
**Why:** Long-running tool calls should show progress. Simple handler.

**Files:**
- `pkg/mcp/tool_adapter.go` — pass `ProgressToken` in `CallToolParams.Meta`
- `pkg/mcp/client.go` — set `ProgressNotificationHandler`, route to `onUpdate` callback

**Tests:** Verify progress notifications during tool call are forwarded.

**Commit:** `feat(mcp): display progress for long-running MCP tool calls`

---

#### Task 6: Pagination for Tool Listing [S]
**Why:** Servers with many tools may paginate. We'd silently miss tools.

**Files:**
- `pkg/mcp/client.go` — replace `session.ListTools()` with `session.Tools()` iterator (handles pagination automatically)

**Tests:** Test with a server that returns paginated tool lists.

**Commit:** `fix(mcp): handle paginated tool lists`

---

### Priority 2: Valuable, moderate effort

#### Task 7: Sampling (CreateMessage) Support [L]
**Why:** Enables powerful agentic MCP servers. Major differentiator.

**Files:**
- `pkg/mcp/sampling.go` — implement `CreateMessageHandler` that calls fir's AI backend
- `pkg/mcp/client.go` — wire handler into `ClientOptions`
- `pkg/mcp/sampling_test.go` — test with mock AI backend

**Design:** When an MCP server calls `sampling/createMessage`, fir calls its configured LLM provider with the provided messages and returns the result. Must respect `maxTokens`, `modelPreferences`, `temperature`, `stopSequences`.

**Tests:** End-to-end test: MCP server tool calls sampling, fir routes to mock LLM, result returned.

**Commit:** `feat(mcp): implement sampling (createMessage) support`

---

#### Task 8: Resources Support [M]
**Why:** Resources enable MCP servers to expose context (docs, data) to the LLM.

**Files:**
- `pkg/mcp/resources.go` — `ListResources()`, `ReadResource()` wrappers
- `pkg/mcp/client.go` — enumerate resources on startup, expose as system context or tools
- `pkg/mcp/resources_test.go`

**Design decisions:**
- Option A: Expose resources as tools (`mcp__server__read_resource`)
- Option B: Auto-inject resource content into system prompt
- Option C: Both, configurable

**Tests:** List and read resources from test server. Verify content injection.

**Commit:** `feat(mcp): support MCP resources`

---

#### Task 9: Resource Subscriptions [S]
**Why:** Keep resource context up-to-date. Depends on Task 8.

**Files:**
- `pkg/mcp/resources.go` — subscribe to resources, handle update notifications
- `pkg/mcp/client.go` — set `ResourceUpdatedHandler`

**Tests:** Verify re-read on update notification.

**Commit:** `feat(mcp): subscribe to resource updates`

---

#### Task 10: Prompts Support [M]
**Why:** Reusable prompt templates from MCP servers. Could power slash commands.

**Files:**
- `pkg/mcp/prompts.go` — `ListPrompts()`, `GetPrompt()` wrappers
- `pkg/mcp/prompts_test.go`

**Design:** Expose MCP prompts as `/mcp:<server>:<prompt>` slash commands in interactive mode.

**Tests:** List and get prompts from test server.

**Commit:** `feat(mcp): support MCP prompts`

---

#### Task 11: Completions Support [S]
**Why:** Better UX for interactive prompt/resource argument completion. Depends on Tasks 8, 10.

**Files:**
- `pkg/mcp/completions.go` — wrapper around `session.Complete()`
- Integration with interactive mode tab completion

**Tests:** Verify completion results for prompt arguments.

**Commit:** `feat(mcp): support argument completions`

---

#### Task 12: Elicitation Support [M]
**Why:** Lets MCP servers ask the user questions. Important for approval flows.

**Files:**
- `pkg/mcp/elicitation.go` — implement `ElicitationHandler`
- Wire into interactive mode's user input system
- `pkg/mcp/elicitation_test.go`

**Tests:** Mock server sends elicitation request, verify user is prompted and response returned.

**Commit:** `feat(mcp): support server-initiated elicitation`

---

### Priority 3: Advanced / nice-to-have

#### Task 13: Ping / Keepalive [S]
**Why:** Auto-detect dead servers, clean up resources.

**Files:**
- `pkg/mcp/client.go` — set `KeepAlive` in `ClientOptions`

**Tests:** Verify session closes on unresponsive server.

**Commit:** `feat(mcp): enable keepalive for MCP sessions`

---

#### Task 14: Cancellation Verification [S]
**Why:** Ensure context cancellation properly sends `notifications/cancelled`.

**Files:**
- `pkg/mcp/tool_adapter_test.go` — add test that cancels a tool call mid-flight

**Tests:** Cancel a long-running tool call, verify cancellation notification sent.

**Commit:** `test(mcp): verify tool call cancellation`

---

#### Task 15: Multiple Config Sources [S]
**Why:** Users need project-level AND user-level MCP configs, merged.

**Files:**
- `pkg/mcp/config.go` — `MergeConfigs()` function, load from multiple paths
- Update wiring to load both `~/.fir/mcp.json` and `.fir/mcp.json`

**Tests:** Verify merge precedence (project overrides user).

**Commit:** `feat(mcp): merge project and user MCP configs`

---

#### Task 16: Server Health & Error Reporting [S]
**Why:** Users need visibility into MCP server failures.

**Files:**
- `pkg/mcp/client.go` — add `Status()` method to `Manager`, track per-server health
- Wire into status display

**Tests:** Verify status reflects connection failures.

**Commit:** `feat(mcp): expose MCP server health status`

---

#### Task 17: fir as MCP Server [L]
**Why:** Let other MCP clients use fir's tools. Enables composability.

**Files:**
- `pkg/mcp/server.go` — new file, create `mcp.Server` exposing fir's built-in tools
- `pkg/mcp/server_test.go`
- `cmd/fir/app.go` — add `--mcp-server` flag or `mcp serve` subcommand

**Design:** Start fir in server mode over stdio or HTTP. Expose tools like `bash`, `read_file`, `write_file`, `grep`. Optionally expose sampling capability (forward to configured LLM).

**Tests:** Connect a test MCP client, list tools, call a tool.

**Commit:** `feat(mcp): expose fir as an MCP tool server`

---

#### Task 18: Config Hot Reload [M]
**Why:** Avoid restart when changing MCP config.

**Files:**
- `pkg/mcp/config.go` — file watcher
- `pkg/mcp/client.go` — diff configs, start/stop changed servers

**Tests:** Modify config file, verify server list updates.

**Commit:** `feat(mcp): hot reload MCP server configs`

---

#### Task 19: Audio/Embedded Resource Content Types [S]
**Why:** Complete content type handling in `convertResult`.

**Files:**
- `pkg/mcp/tool_adapter.go` — handle `*AudioContent`, `*EmbeddedResource`, `*ResourceLink`
- `pkg/mcp/tool_adapter_test.go`

**Commit:** `feat(mcp): handle audio and resource content types`

---

#### Task 20: Middleware / Logging Transport [S]
**Why:** Debug MCP wire protocol issues.

**Files:**
- `pkg/mcp/client.go` — optionally wrap transport in `LoggingTransport` when verbose

**Commit:** `feat(mcp): add debug transport logging`

---

## 4. Summary Table

| # | Task | Complexity | Depends On | Priority |
|---|---|---|---|---|
| 1 | SSE/HTTP transports | M | — | P1 |
| 2 | Roots | S | — | P1 |
| 3 | Tool list changed | S | — | P1 |
| 4 | Logging | S | — | P1 |
| 5 | Progress notifications | S | — | P1 |
| 6 | Pagination | S | — | P1 |
| 7 | Sampling (createMessage) | L | — | P2 |
| 8 | Resources | M | — | P2 |
| 9 | Resource subscriptions | S | 8 | P2 |
| 10 | Prompts | M | — | P2 |
| 11 | Completions | S | 8, 10 | P2 |
| 12 | Elicitation | M | — | P2 |
| 13 | Ping/keepalive | S | — | P3 |
| 14 | Cancellation verification | S | — | P3 |
| 15 | Multiple config sources | S | — | P3 |
| 16 | Server health reporting | S | — | P3 |
| 17 | fir as MCP server | L | — | P3 |
| 18 | Config hot reload | M | 15 | P3 |
| 19 | Audio/resource content types | S | — | P3 |
| 20 | Debug transport logging | S | — | P3 |

**Total: 7S + 4M + 2L (P1), 2S + 3M + 1L (P2), 6S + 1M + 1L (P3)**

## 5. Recommended Implementation Order

**Phase A** (P1 — immediate value, all small/medium):
1. Task 6: Pagination (bug fix — we may be missing tools)
2. Task 2: Roots
3. Task 4: Logging
4. Task 3: Tool list changed
5. Task 5: Progress notifications
6. Task 1: SSE/HTTP transports

**Phase B** (P2 — major features):
7. Task 8: Resources
8. Task 9: Resource subscriptions
9. Task 7: Sampling
10. Task 10: Prompts
11. Task 12: Elicitation
12. Task 11: Completions

**Phase C** (P3 — polish):
13–20: Remaining tasks in any order.
