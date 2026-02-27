# Review Backlog — 2026-02-27

**Last reviewed:** MCP watch cycle 26, 2026-02-27 ~02:30 PST
**Build status:** ✅ all 24 packages pass with -race. 0 URGENT, 3 open BACKLOG.
**Commits reviewed this cycle:** No new commits; `completions.go` doc fixed (errors-propagated comment); `client.go` race fix still pending commit.

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

### Test Coverage

*(none)*

### Simplification

- **[DUPLICATE FUNCTION]** `pkg/mcp/client.go` — `configsEqual` (line 421) and `serverConfigEqual`
  (line 547) are identical: both JSON-marshal both args and compare strings. One should be
  removed; `Reload` and `applyConfigDiff` should share a single helper.
  **Files:** `pkg/mcp/client.go:421,547`

---

## Resolved This Cycle ✅

- **✅ FIXED (completions.go)** Doc mismatch fixed — comments now accurately say errors are propagated (not "empty slice on unsupported")..
- **✅ SELF-FIXED prompts_test.go** `pkg/mcp/prompts_test.go` created — covers list/get integration, missing-name error, `convertPromptResult` for all content types (text, image, embedded-text, embedded-blob, empty, no-description).
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
