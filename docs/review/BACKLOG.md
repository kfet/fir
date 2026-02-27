# Review Backlog — 2026-02-26

**Last reviewed:** MCP watch cycle 4, 2026-02-26 ~20:50 PST
**Build status:** ✅ `go build ./...` passes. ⚠️ `go test -race ./pkg/mcp/...` FAILS (`TestManager_ProgressNotification` data race + assertion failure)
**Commits reviewed this cycle:** `3b74aae` (roots), `fa2b102` (paginated iterator), plus in-progress unstaged work (progress notifications, tool-list-changed, verbose logging)

---

## Open Issues

### Correctness

- **[BACKLOG/CORRECTNESS]** `pkg/mcp/client.go` (unstaged) — `defer registry.unregister(toolCallID)` in `AdaptTool.Execute` closure runs immediately when `CallTool` returns. If the in-memory (or real) transport delivers the progress notification in a separate goroutine *after* the call response arrives, `unregister` fires first and the notification is silently dropped. The URGENT issue (broken test) is related to this, but even after fixing the test, the core design needs attention: consider using a WaitGroup or draining notifications before unregistering.
  Files: `pkg/mcp/tool_adapter.go:50-60`
  Suggested fix: Add a brief drain window after `CallTool` returns before unregistering, or use a WaitGroup to track in-flight dispatches.

- **[BACKLOG/CORRECTNESS]** `pkg/mcp/client.go:line with "file://" + cwd` — CWD path is concatenated into a `file://` URI without URL encoding. Paths with spaces or other characters requiring percent-encoding (valid on macOS/Linux) produce invalid RFC-3986 URIs like `file:///home/user/my project`.
  Files: `pkg/mcp/client.go` (unstaged version, roots default-to-CWD block)
  Suggested fix: `(&url.URL{Scheme: "file", Path: cwd}).String()` which produces correctly encoded `file:///home/user/my%20project`.

### Test Coverage

- **[WEAK ASSERTION]** `TestACP_E2E_MCP_ToolsAppearInSession` tool-call check
  (only when `FIR_E2E_AGENT_DIR` is set): the `ToolCallUpdate != nil` branch
  sets `gotMCPTool = true` for *any* tool-call update, not just
  `mcp__echo-srv__echo`. False positive possible if another tool fires first.
  Impact: low (test only logs on failure, doesn't call `t.Error`). Worth tightening.

- **[MISSING TEST]** `pkg/mcp/client.go` — `TestManager_VerboseLogging` is missing. The new `verbose bool` parameter to `NewManager` and the `SetLoggingLevel` call are not unit-tested. No test verifies that `verbose=true` requests `"debug"` level vs `verbose=false` requesting `"warning"`. Also the `LoggingMessageHandler` forwarding to slog is untested.
  Files: `pkg/mcp/client_test.go`, `pkg/mcp/client.go`
  Suggested fix: Add a test that creates a Manager with `verbose=true`, injects an in-memory server that sends a log message at "debug" level, and verifies the message appears in the slog output.

---

## Resolved This Cycle ✅

- **✅ FIXED 616f1b5** `TestRawConnMethodHandler_SessionNew_AcceptsMcpServers` hung 70 s:
  used `context.Background()` with a real `npx` command.  
  Fixed: `false` command + 5 s context timeout. Test: 70.4 s → 0.00 s.

- **✅ FIXED 616f1b5** `acp.go:NewSession` nil dereference on non-stdio MCP servers:
  `mcpServer.Stdio` was accessed unconditionally; nil for HTTP/SSE entries.  
  Fixed: skip with stderr warning + `TestRawConnMethodHandler_SessionNew_NonStdioMCPServerSkipped`.

- **✅ FIXED a646bc6** `acp_mcp_e2e_test.go` wrong `mcpServers` JSON format:
  all three tests sent `{name: {...}}` (JSON object) but SDK expects `[{...}]` (array).
  `ToolsAppearInSession` also sent `env` as `map[string]string` instead of
  `[{name,value}]`. Fixed to correct array format throughout.

---

## Previously Resolved (all ✅)

### Cycle 1–2

- **✅ FIXED ef4f139** `pkg/mcp/client.go:commandTransport` — `cmd.Env` not seeded
  with `os.Environ()`. Tests: `TestCommandTransport_EnvInheritsParent` +
  `TestCommandTransport_EmptyCommand`.
- **✅ FIXED 329d5c9** `pkg/mcp/tool_adapter_test.go` — `convertResult` default
  branch untested. Added `TestConvertResult_UnknownContentType`.
- **✅ NOTED** `docs/plan/16-mcp-support.md` described non-existent SDK fields.
  Fixed upstream in `504e9f5`.
- **✅ FIXED 2a44062** `loadProjectMCPConfigs` — silent nil on parse error; now
  warns to stderr.

### Earlier (pre-MCP)

*(full list retained in git history)*
