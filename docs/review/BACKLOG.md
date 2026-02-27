# Review Backlog — 2026-02-27

**Last reviewed:** MCP watch cycle 6, 2026-02-27 ~01:10 PST
**Build status:** ✅ `go build ./...` passes. ✅ `go test -race ./...` passes. ✅ `go test -race -count=20 -run TestManager_ProgressNotification ./pkg/mcp/...` passes.
**Commits reviewed this cycle:** `7f336a9` (auto-resume after compaction), `e30163d` (checkpoint), `215890d` (SSE transports), `9708bd2` (progress notification test fix)

---

## Open Issues

### Correctness

- **[BACKLOG/CORRECTNESS]** `pkg/mcp/client.go` — `"file://" + cwd` does not URL-encode the path.
  Paths with spaces or special characters (valid on macOS/Linux) produce invalid RFC-3986 URIs
  like `file:///home/user/my project`.
  **Files:** `pkg/mcp/client.go` (roots default-to-CWD block)
  **Fix:** `(&url.URL{Scheme: "file", Path: cwd}).String()` — produces correctly encoded `file:///home/user/my%20project`.

- **[BACKLOG/CORRECTNESS]** `pkg/core/agentsession.go:runAutoCompaction` — inconsistency with
  `HasPendingWork()`. `runAutoCompaction` resumes based only on message role (`"user"` or
  `"toolResult"`); it does NOT check `PendingToolCalls`. `HasPendingWork()` checks both. If
  threshold auto-compaction fires while a tool is mid-execution (`PendingToolCalls > 0`), auto-
  compaction will not resume, but manual `/compact` will. Low probability in practice (threshold
  compaction fires at token boundaries, not mid-tool), but the inconsistency is worth removing.
  **Fix:** Replace the inline role check in `runAutoCompaction` with `s.HasPendingWork()`.

### Simplification / Design

- **[BACKLOG/DESIGN]** `pkg/mcp/client.go:createTransport` — the `default` case silently falls
  through to stdio for ANY unknown transport name (e.g., `"ftp"`, `"ws"`). This masks
  misconfiguration. Consider returning an error for unrecognised values:
  ```go
  default:
      if cfg.Transport != "" && cfg.Transport != "stdio" {
          return nil, fmt.Errorf("unsupported transport %q; valid: stdio, sse, streamable", cfg.Transport)
      }
      return commandTransport(cfg)
  ```
  **Files:** `pkg/mcp/client.go`

### Test Coverage

- **[MISSING TEST]** `pkg/core/agentsession.go:HasPendingWork` — no direct unit test. The method
  is tested indirectly through the RPC compact tests, but a focused unit test would catch regressions
  faster. Should cover: empty messages → false; last role "user" → true; last role "toolResult" →
  true; last role "assistant" → false; PendingToolCalls non-empty → true.
  **Files:** `pkg/core/agentsession_test.go`

- **[MISSING TEST]** `pkg/modes/interactive/mode.go` — the manual compaction auto-resume path has
  no test. The RPC mode tests cover their own handler; interactive mode has no equivalent coverage
  for the `HasPendingWork() → Agent.Continue()` branch in `performCompact`.
  Acceptable if interactive mode is considered hard to unit-test (TUI dependency), but worth noting.

- **[WEAK ASSERTION]** `TestACP_E2E_MCP_ToolsAppearInSession` — `ToolCallUpdate != nil` fires for
  *any* tool call, not just `mcp__echo-srv__echo`. False positive possible if another tool fires first.
  **Files:** `pkg/modes/acp/acp_mcp_e2e_test.go`

- **[MISSING TEST]** `pkg/mcp/client.go:createTransport` — no test for an unknown transport string
  (e.g., `Transport: "ftp"`). With the current `default:` fallthrough, this silently succeeds using
  stdio; after the fix above it should return an error, and that behaviour should be tested.
  **Files:** `pkg/mcp/client_test.go`

---

## Resolved This Cycle ✅

- **✅ FIXED** `pkg/mcp/tool_adapter.go` — `defer registry.unregister` races with SDK notification goroutine (fix: removed defer, added explanatory comment).
- **✅ FIXED** `pkg/mcp/debug_test.go` — debug file with `fmt.Printf` cleaned up before commit.
- **✅ FIXED 215890d** Missing tests for `createTransport` SSE/streamable paths — `TestCreateTransport_SSE`, `TestCreateTransport_Streamable`, `TestCreateTransport_*_MissingURL`, `TestManager_StreamableTransport_Integration` added.

---

## Previously Resolved (all ✅)

- **✅ FIXED ef4f139** `pkg/mcp/client.go:commandTransport` — `cmd.Env` not seeded with `os.Environ()`.
- **✅ FIXED 329d5c9** `pkg/mcp/tool_adapter_test.go` — `convertResult` default branch untested.
- **✅ FIXED 2a44062** `loadProjectMCPConfigs` — silent nil on parse error.
- **✅ FIXED 616f1b5** `TestRawConnMethodHandler_SessionNew_AcceptsMcpServers` hung 70 s.
- **✅ FIXED 616f1b5** `acp.go:NewSession` nil dereference on non-stdio MCP servers.
- **✅ FIXED a646bc6** `acp_mcp_e2e_test.go` wrong `mcpServers` JSON format.
