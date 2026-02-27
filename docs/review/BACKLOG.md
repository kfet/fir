# Review Backlog — 2026-02-27

**Last reviewed:** MCP watch cycle 8, 2026-02-27 ~01:20 PST
**Build status:** ✅ `go build ./...` passes. ✅ `go test -race ./...` passes.
**Commits reviewed this cycle:** `5e9235d` (MCP resources as tools), unstaged fixes: blob MIME gate, `TestCreateTransport_UnknownType`

---

## Open Issues

### Correctness

- **[BACKLOG/CORRECTNESS]** `pkg/core/agentsession.go:runAutoCompaction` — inconsistency with
  `HasPendingWork()`. `runAutoCompaction` resumes based only on message role (`"user"` / `"toolResult"`),
  not on `PendingToolCalls`. `HasPendingWork()` checks both. Low probability edge case (threshold
  compaction during active tool execution), but the inconsistency is easy to remove.
  **Fix:** Replace the inline role check in `runAutoCompaction` with `s.HasPendingWork()`.

### Simplification / Design

- **[BACKLOG/SIMPLIFICATION]** `pkg/mcp/tool_adapter.go:87` — `*sdk.ImageContent` always maps
  to `ContentTypeImage` with no MIME-type guard. Acceptable because MCP's `ImageContent` is spec'd
  to carry image data, but adding `strings.HasPrefix(v.MIMEType, "image/")` would be consistent
  with the fix applied to `resources.go`. Low priority.

### Test Coverage

- **[MISSING TEST]** `pkg/core/agentsession.go:HasPendingWork` — no direct unit test.
  Should cover: empty messages → false; last role "user" → true; last role "toolResult" → true;
  last role "assistant" → false; `PendingToolCalls` non-empty → true.
  **Files:** `pkg/core/agentsession_test.go`

- **[WEAK ASSERTION]** `TestACP_E2E_MCP_ToolsAppearInSession` — `ToolCallUpdate != nil` fires for
  *any* tool call, not just `mcp__echo-srv__echo`. False positive if another tool fires first.
  **Files:** `pkg/modes/acp/acp_mcp_e2e_test.go`

---

## Resolved This Cycle ✅

- **✅ FIXED (resources.go)** `convertResourceResult` blob → `ContentTypeImage` for non-image blobs.
  Now gates on `strings.HasPrefix(c.MIMEType, "image/")`. Non-image blobs returned as `[base64 mime/type] …` text.
  Tests added: `TestConvertResourceResult/non-image blob`, `TestConvertResourceResult/blob no mime`.
- **✅ FIXED (client_test.go)** `TestCreateTransport_UnknownType` added — verifies `Transport: "ftp"`
  returns an error.
- **✅ ADDED** `pkg/mcp/resources_test.go` — full coverage: list, read, missing URI, resource-list-changed, conversion.

---

## Previously Resolved (all ✅)

- **✅ FIXED 6ddadcd** `pkg/mcp/tool_adapter.go` — `defer registry.unregister` progress-callback race.
- **✅ FIXED 215890d** `pkg/mcp/client_test.go` — `createTransport` SSE/streamable tests added.
- **✅ FIXED 5e9235d** `pkg/mcp/client.go` — URI encoding: `"file://" + cwd` → `(&url.URL{Scheme:"file",Path:cwd}).String()`.
- **✅ FIXED 5e9235d** `pkg/mcp/client.go:createTransport` — unknown transport now returns an error.
- **✅ FIXED ef4f139** `pkg/mcp/client.go:commandTransport` — `cmd.Env` not seeded with `os.Environ()`.
- **✅ FIXED 329d5c9** `pkg/mcp/tool_adapter_test.go` — `convertResult` default branch untested.
- **✅ FIXED 2a44062** `loadProjectMCPConfigs` — silent nil on parse error.
- **✅ FIXED 616f1b5** `TestRawConnMethodHandler_SessionNew_AcceptsMcpServers` hung 70 s.
- **✅ FIXED 616f1b5** `acp.go:NewSession` nil dereference on non-stdio MCP servers.
- **✅ FIXED a646bc6** `acp_mcp_e2e_test.go` wrong `mcpServers` JSON format.
