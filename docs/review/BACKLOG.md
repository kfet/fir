# Review Backlog — 2026-02-27

**Last reviewed:** MCP watch cycle 10, 2026-02-27 ~01:28 PST
**Build status:** ✅ `go build ./...` passes. ✅ `go test -race ./...` passes (all 24 packages).
**Commits reviewed this cycle:** `b76bd20` (resource subscription + `OnResourceUpdated`); `92e6174` (ImageContent MIME guard); `f6c7931`, `7e102e0`, `19f0c28`, `262e314` (previously resolved backlog items)

---

## Open Issues

### Completeness / Design

- **[BACKLOG/COMPLETENESS]** `pkg/mcp/client.go:startServer` — resources are subscribed once at
  startup (in the `for res := range session.Resources(...)` loop). If the server later sends a
  `resources/list_changed` notification and new resources appear in the refreshed tool list, those
  new resources are never subscribed to. The `ToolListChangedHandler` refreshes tools (and
  resource tools) but does not re-run the subscription loop.
  **Impact:** Low. Dynamic resource addition to running servers is rare, and `list_resources` /
  `read_resource` tools still work (they query the server on each call). Only `OnResourceUpdated`
  callbacks are affected.
  **Fix:** Call the subscription loop inside `ToolListChangedHandler`, comparing against the
  already-subscribed URIs to avoid duplicate subscriptions.
  **Files:** `pkg/mcp/client.go`

---

## Resolved This Cycle ✅

- **✅ FIXED b76bd20** `pkg/mcp/client.go` — `OnResourceUpdated` callback + `ResourceUpdatedHandler`
  + startup subscription loop (best-effort; ignores errors for servers without subscription support).
  Test: `TestManager_ResourceSubscription` passes including `-race`.
- **✅ FIXED 92e6174** `pkg/mcp/tool_adapter.go:87` — `*sdk.ImageContent` MIME guard added,
  consistent with `resources.go`. Test: `TestConvertResult_NonImageContent`.
- **✅ FIXED f6c7931** `pkg/core/agentsession.go:runAutoCompaction` — now checks `PendingToolCalls`.
- **✅ FIXED f6c7931** `pkg/core/agentsession_test.go` — `HasPendingWork` unit tests added.
- **✅ FIXED 7e102e0** `pkg/mcp/client_test.go` — duplicate unknown-transport test deduplicated.
- **✅ FIXED 19f0c28** `pkg/modes/acp/acp_mcp_e2e_test.go` — assertion tightened to `echo-srv__echo`.

---

## Previously Resolved (all ✅)

- **✅ FIXED 5e9235d** `resources.go`: blob→ContentTypeImage MIME gate + URI encoding fix + unknown transport guard.
- **✅ FIXED 6ddadcd** `tool_adapter.go`: progress-callback defer-unregister race.
- **✅ FIXED 215890d** `client_test.go`: createTransport SSE/streamable tests.
- **✅ FIXED ef4f139** `client.go:commandTransport`: `cmd.Env` missing `os.Environ()`.
- **✅ FIXED 329d5c9** `tool_adapter_test.go`: `convertResult` default branch.
- **✅ FIXED 2a44062** `loadProjectMCPConfigs`: silent nil on parse error.
- **✅ FIXED 616f1b5** Hung test + nil dereference in ACP mode.
- **✅ FIXED a646bc6** E2E test wrong JSON format.
