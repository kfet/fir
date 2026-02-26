# Review Backlog — 2026-02-26

**Last reviewed:** MCP watch cycle 3, 2026-02-26 04:23 PST
**Build status:** ✅ BUILD PASSING (`go vet ./...` clean, `go test ./...` all 24 packages pass)
**Commit reviewed this cycle:** `2988bbe` test(mcp): comprehensive e2e tests with real stdio subprocess

---

## Open Issues

### Simplification

- **[STYLE]** `pkg/modes/acp/acp.go:NewSession` merge loop — nil-map init was
  previously inside the loop; fixed in `2a0f1e6`. Current code is clear. ✅

### Test Coverage

- **[WEAK ASSERTION]** `TestACP_E2E_MCP_ToolsAppearInSession` tool-call check
  (only when `FIR_E2E_AGENT_DIR` is set): the `ToolCallUpdate != nil` branch
  sets `gotMCPTool = true` for *any* tool-call update, not just
  `mcp__echo-srv__echo`. False positive possible if another tool fires first.
  Impact: low (test only logs on failure, doesn't call `t.Error`). Worth tightening.

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

