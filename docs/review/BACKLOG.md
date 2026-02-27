# Review Backlog — 2026-02-27

**Last reviewed:** MCP watch cycle 7, 2026-02-27 ~01:15 PST
**Build status:** ✅ `go build ./...` passes. ✅ `go test -race ./...` passes.
**Commits reviewed this cycle:** `6ddadcd` (progress-callback race fix), unstaged: `resources.go` (MCP resources as tools), `client.go` (URI fix + transport validation)

---

## Open Issues

### Correctness

- **[BACKLOG/CORRECTNESS]** `pkg/core/agentsession.go:runAutoCompaction` — inconsistency with
  `HasPendingWork()`. `runAutoCompaction` resumes only on message role (`"user"` / `"toolResult"`),
  not on `PendingToolCalls`. `HasPendingWork()` checks both. Low probability edge case (threshold
  compaction firing while a tool is mid-execution), but removes an inconsistency.
  **Fix:** Replace the inline role check in `runAutoCompaction` with `s.HasPendingWork()`.

### Simplification / Design

*(none currently — unknown transport validation and URI encoding fixes were applied in the unstaged diff)*

### Test Coverage

- **[MISSING TEST]** `pkg/mcp/resources.go` — new file with no corresponding `resources_test.go`.
  Should test: `listResourcesTool` (empty server → "No resources available"; server with resources →
  correct formatted listing), `readResourceTool` (missing uri param → error result; valid uri →
  text content returned; blob content returned), `convertResourceResult` (text content, blob content,
  empty content), `formatResource`, `formatResourceTemplate`, `resourceErrResult`.

- **[MISSING TEST]** `pkg/core/agentsession.go:HasPendingWork` — no direct unit test.
  Should cover: empty messages → false; last role "user" → true; last role "toolResult" → true;
  last role "assistant" → false; `PendingToolCalls` non-empty → true.
  **Files:** `pkg/core/agentsession_test.go`

- **[MISSING TEST]** `pkg/mcp/client.go:createTransport` — no test for unknown transport strings
  (e.g., `Transport: "ftp"`). Now that the guard returns an error, this behaviour should be tested.
  **Files:** `pkg/mcp/client_test.go`

- **[WEAK ASSERTION]** `TestACP_E2E_MCP_ToolsAppearInSession` — `ToolCallUpdate != nil` fires for
  *any* tool call, not just `mcp__echo-srv__echo`. False positive if another tool fires first.
  **Files:** `pkg/modes/acp/acp_mcp_e2e_test.go`

---

## Resolved This Cycle ✅

- **✅ FIXED (unstaged)** `pkg/mcp/client.go` — URI encoding bug: `"file://" + cwd` replaced with
  `(&url.URL{Scheme: "file", Path: cwd}).String()`.
- **✅ FIXED (unstaged)** `pkg/mcp/client.go:createTransport` — unknown transport strings now return
  an error instead of silently falling through to stdio.
- **✅ FIXED 6ddadcd** `pkg/mcp/tool_adapter.go` — `defer registry.unregister` races with SDK
  notification goroutine (formally committed).

---

## Previously Resolved (all ✅)

- **✅ FIXED 215890d** `pkg/mcp/client_test.go` — Missing tests for `createTransport` SSE/streamable paths added.
- **✅ FIXED ef4f139** `pkg/mcp/client.go:commandTransport` — `cmd.Env` not seeded with `os.Environ()`.
- **✅ FIXED 329d5c9** `pkg/mcp/tool_adapter_test.go` — `convertResult` default branch untested.
- **✅ FIXED 2a44062** `loadProjectMCPConfigs` — silent nil on parse error.
- **✅ FIXED 616f1b5** `TestRawConnMethodHandler_SessionNew_AcceptsMcpServers` hung 70 s.
- **✅ FIXED 616f1b5** `acp.go:NewSession` nil dereference on non-stdio MCP servers.
- **✅ FIXED a646bc6** `acp_mcp_e2e_test.go` wrong `mcpServers` JSON format.
