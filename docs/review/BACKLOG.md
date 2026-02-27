# Review Backlog — 2026-02-26

**Last reviewed:** MCP watch cycle 5, 2026-02-26 ~21:10 PST
**Build status:** ✅ `go build ./...` passes. ❌ `go test -race ./pkg/mcp/...` FAILS (`TestManager_ProgressNotification` timeout — see URGENT.md)
**Commits reviewed this cycle:** `fa21bd2` (logging + progress + tool-list-changed integration) + unstaged (transport, config, test fix, debug_test.go)

---

## Open Issues

### Correctness

- **[BACKLOG/CORRECTNESS]** `pkg/mcp/client.go` — `"file://" + cwd` does not URL-encode the path. Paths with spaces or special characters (valid on macOS/Linux) produce invalid RFC-3986 URIs like `file:///home/user/my project`.
  Files: `pkg/mcp/client.go` (roots default-to-CWD block)
  Suggested fix: `(&url.URL{Scheme: "file", Path: cwd}).String()` — produces correctly encoded `file:///home/user/my%20project`.

### Simplification / Design

- **[BACKLOG/DESIGN]** `pkg/mcp/client.go` (unstaged `createTransport`) — the `default` case silently falls through to stdio for ANY unknown transport name (e.g., `"ftp"`, `"ws"`). This masks misconfiguration. Consider returning an error for unrecognised transport values:
  ```go
  default:
      if cfg.Transport != "" && cfg.Transport != "stdio" {
          return nil, fmt.Errorf("unsupported transport %q; valid: stdio, sse, streamable", cfg.Transport)
      }
      return commandTransport(cfg)
  ```
  Files: `pkg/mcp/client.go` (unstaged)

### Test Coverage

- **[WEAK ASSERTION]** `TestACP_E2E_MCP_ToolsAppearInSession` tool-call check
  (only when `FIR_E2E_AGENT_DIR` is set): the `ToolCallUpdate != nil` branch
  sets `gotMCPTool = true` for *any* tool-call update, not just
  `mcp__echo-srv__echo`. False positive possible if another tool fires first.
  Impact: low. Worth tightening.

- **[MISSING TEST]** `pkg/mcp/client.go` (unstaged `createTransport`) — SSE and streamable transport creation paths are not unit-tested. `TestServerConfig_TransportFields` tests JSON only; there is no test verifying that `createTransport` returns the right concrete transport type for `"sse"` and `"streamable"`, or that it returns an error for a missing URL.
  Files: `pkg/mcp/client_test.go`
  Suggested fix: Add `TestCreateTransport_SSE`, `TestCreateTransport_Streamable`, `TestCreateTransport_MissingURL`, `TestCreateTransport_UnknownTransport` unit tests.

---

## Resolved This Cycle ✅

- **✅ FIXED fa21bd2** Missing test for `verbose bool` / `SetLoggingLevel`: `TestManager_LoggingHandler` and `TestManager_LoggingLevelVerbose` added, verifying that MCP log messages are forwarded to slog at the correct level.

---

## Previously Resolved (all ✅)

### Cycle 1–4

- **✅ FIXED ef4f139** `pkg/mcp/client.go:commandTransport` — `cmd.Env` not seeded with `os.Environ()`.
- **✅ FIXED 329d5c9** `pkg/mcp/tool_adapter_test.go` — `convertResult` default branch untested.
- **✅ NOTED** `docs/plan/16-mcp-support.md` described non-existent SDK fields.
- **✅ FIXED 2a44062** `loadProjectMCPConfigs` — silent nil on parse error.
- **✅ FIXED 616f1b5** `TestRawConnMethodHandler_SessionNew_AcceptsMcpServers` hung 70 s.
- **✅ FIXED 616f1b5** `acp.go:NewSession` nil dereference on non-stdio MCP servers.
- **✅ FIXED a646bc6** `acp_mcp_e2e_test.go` wrong `mcpServers` JSON format.

*(earlier pre-MCP items in git history)*
