# Review Backlog — 2026-02-27

**Last reviewed:** MCP watch cycle 9, 2026-02-27 ~01:30 PST
**Build status:** ✅ `go build ./...` passes. ✅ `go test -race ./...` passes.
**Commits reviewed this cycle:** `7e102e0`, `f6c7931`, `19f0c28`, `92e6174`

---

## Open Issues

*(none)*

---

## Resolved This Cycle ✅

- **✅ FIXED `f6c7931`** `pkg/core/agentsession.go:runAutoCompaction` — now checks
  `state.PendingToolCalls` in addition to message role, matching `HasPendingWork()`.
- **✅ FIXED `f6c7931`** `pkg/core/agentsession_test.go` — `HasPendingWork` unit tests added
  (empty, user-last, toolResult-last, assistant-last).
- **✅ FIXED `19f0c28`** `TestACP_E2E_MCP_ToolsAppearInSession` — assertion tightened to
  `strings.Contains(Title, "echo-srv__echo")` to avoid false positives from resource tools.
- **✅ FIXED `92e6174`** `pkg/mcp/tool_adapter.go` — `*sdk.ImageContent` now guarded by
  `strings.HasPrefix(v.MIMEType, "image/")` matching the fix in `resources.go`. Test added
  in `TestConvertResult_NonImageContent`.
- **✅ FIXED `7e102e0`** `pkg/mcp/client_test.go` — deduplicated `TestCreateTransport_UnknownType`
  vs `TestCreateTransport_UnknownTransport`.

---

## Previously Resolved (all ✅)

- **✅ FIXED `6ddadcd`** `pkg/mcp/tool_adapter.go` — `defer registry.unregister` progress-callback race.
- **✅ FIXED `5e9235d`** `pkg/mcp/client.go` — URI encoding: `"file://" + cwd` → `url.URL.String()`.
- **✅ FIXED `5e9235d`** `pkg/mcp/client.go:createTransport` — unknown transport now returns an error.
- **✅ FIXED `5e9235d`** `pkg/mcp/resources.go` — blob `→ContentTypeImage` for non-image blobs; MIME gate added.
- **✅ FIXED `5e9235d`** `pkg/mcp/resources_test.go` — full coverage: list, read, missing URI, conversion, non-image blob.
- **✅ FIXED `215890d`** `pkg/mcp/client_test.go` — `createTransport` SSE/streamable tests added.
- **✅ FIXED `ef4f139`** `pkg/mcp/client.go:commandTransport` — `cmd.Env` not seeded with `os.Environ()`.
- **✅ FIXED `329d5c9`** `pkg/mcp/tool_adapter_test.go` — `convertResult` default branch untested.
- **✅ FIXED `2a44062`** `loadProjectMCPConfigs` — silent nil on parse error.
- **✅ FIXED `616f1b5`** `TestRawConnMethodHandler_SessionNew_AcceptsMcpServers` hung 70 s.
- **✅ FIXED `616f1b5`** `acp.go:NewSession` nil dereference on non-stdio MCP servers.
