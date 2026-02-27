# Review Backlog — 2026-02-27

**Last reviewed:** MCP watch cycle 27, 2026-02-27 ~02:35 PST
**Build status:** ✅ all 24 packages pass with -race. 0 URGENT, 2 open BACKLOG.
**Commits reviewed this cycle:** `12734c4` — hot reload, race fix, configsEqual dedup, completions docs

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

- **[BACKLOG/COMPLETENESS]** `pkg/mcp/client.go:Manager.Status` — `serverErrors` is only populated
  on initial connection failure during `Start()`. If a server connects successfully but later
  disconnects with an error, `serverErrors[name]` stays nil and `Status()` incorrectly reports
  no error for that server. The `sessions` map also wouldn't be cleaned up automatically on
  disconnect.
  **Impact:** Low today (callers only call `Status()` after startup, and disconnections are rare).
  **Fix:** Hook into a disconnect/error callback if the SDK provides one, or clear `sessions[name]`
  and set `serverErrors[name]` when a session terminates unexpectedly.
  **Files:** `pkg/mcp/client.go`

---

## Resolved This Cycle ✅

- **✅ FIXED `12734c4`** `pkg/mcp/client.go` — `ToolListChangedHandler` race on `m.OnToolsChanged` fixed; stale-session guard added; `applyConfigDiff` removed; `WatchAndReload` calls `Reload` and reads `OnToolsChanged` under lock.
- **✅ FIXED `12734c4`** `pkg/mcp/client.go` — `serverConfigEqual` duplicate removed; renamed to `configsEqual`; single canonical helper shared by `Reload` and `WatchAndReload`.
- **✅ FIXED `12734c4`** `pkg/mcp/completions.go` — doc/error mismatch fixed: `CompletePromptArg`/`CompleteResourceURI` now say errors are propagated (not empty slices).
- **✅ FIXED `12734c4`** `pkg/mcp/config_watch.go` + `config_watch_test.go` — `WatchConfig` (debounce 200ms, dir-watch for atomic renames) and `Manager.WatchAndReload` implemented with full tests.
- **✅ FIXED `12734c4`** `pkg/mcp/reload_test.go` — `Manager.Reload` tests (remove/add/unchanged servers) all pass under `-race`.
- **✅ FIXED (completions.go)** Doc mismatch fixed — comments now accurately say errors are propagated.
- **✅ SELF-FIXED prompts_test.go** `pkg/mcp/prompts_test.go` created — covers list/get integration, missing-name error, `convertPromptResult` for all content types.
- **✅ FIXED b76bd20** `pkg/mcp/client.go` — `OnResourceUpdated` callback + `ResourceUpdatedHandler` + startup subscription loop. Test: `TestManager_ResourceSubscription` passes including `-race`.
- **✅ FIXED 92e6174** `pkg/mcp/tool_adapter.go:87` — `*sdk.ImageContent` MIME guard added. Test: `TestConvertResult_NonImageContent`.
- **✅ FIXED f6c7931** `pkg/core/agentsession.go:runAutoCompaction` — now checks `PendingToolCalls`.
- **✅ FIXED f6c7931** `pkg/core/agentsession_test.go` — `HasPendingWork` unit tests added.
- **✅ FIXED 7e102e0** `pkg/mcp/client_test.go` — duplicate unknown-transport test deduplicated.
- **✅ FIXED 19f0c28** `pkg/modes/acp/acp_mcp_e2e_test.go` — assertion tightened to `echo-srv__echo`.
