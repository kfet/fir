# Review Backlog — 2026-02-27

**Last reviewed:** MCP watch cycle 9, 2026-02-27 ~01:25 PST
**Build status:** ✅ `go build ./...` passes. ✅ `go test -race ./...` passes (all 24 packages).
**Commits reviewed this cycle:** `f6c7931` (HasPendingWork tests + runAutoCompaction fix), `7e102e0` (dedup unknown-transport test), `19f0c28` (ACP E2E assertion tightened)

---

## Open Issues

*(none — all previously tracked items have been resolved or are in-progress unstaged fixes)*

### Low-priority notes

- **[MONITOR]** `pkg/mcp/tool_adapter.go:87` — MIME-type guard for `*sdk.ImageContent` is currently
  unstaged. Verified correct in the diff (`strings.HasPrefix(v.MIMEType, "image/")`, test
  `TestConvertResult_NonImageContent` added). Will be RESOLVED once committed.

---

## Resolved This Cycle ✅

- **✅ FIXED f6c7931** `pkg/core/agentsession.go:runAutoCompaction` — now checks `PendingToolCalls`
  in addition to message role, consistent with `HasPendingWork()`.
- **✅ FIXED f6c7931** `pkg/core/agentsession_test.go` — `HasPendingWork` unit tests added (empty,
  user-last, toolResult-last, assistant-last cases).
- **✅ FIXED 7e102e0** `pkg/mcp/client_test.go` — duplicate `TestCreateTransport_UnknownType`
  deduplicated (kept as `TestCreateTransport_UnknownTransport`).
- **✅ FIXED 19f0c28** `pkg/modes/acp/acp_mcp_e2e_test.go` — MCP tool assertion now checks for
  `mcp__echo-srv__echo` specifically, not any ToolCallUpdate.

---

## Previously Resolved (all ✅)

- **✅ FIXED 5e9235d** `resources.go`: blob→ContentTypeImage fixed with MIME-type guard.
- **✅ FIXED 5e9235d** `client.go`: URI encoding `"file://" + cwd` → `url.URL.String()`.
- **✅ FIXED 5e9235d** `client.go:createTransport` — unknown transport returns error.
- **✅ FIXED 6ddadcd** `tool_adapter.go` — progress-callback defer-unregister race.
- **✅ FIXED 215890d** `client_test.go` — createTransport SSE/streamable tests.
- **✅ FIXED ef4f139** `client.go:commandTransport` — `cmd.Env` missing `os.Environ()`.
- **✅ FIXED 329d5c9** `tool_adapter_test.go` — `convertResult` default branch.
- **✅ FIXED 2a44062** `loadProjectMCPConfigs` — silent nil on parse error.
- **✅ FIXED 616f1b5** Hung test + nil dereference in ACP mode.
- **✅ FIXED a646bc6** E2E test wrong JSON format.
