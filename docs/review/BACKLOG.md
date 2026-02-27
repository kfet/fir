# Review Backlog — 2026-02-27

**Last reviewed:** MCP watch cycle 23, 2026-02-27 ~02:18 PST
**Build status:** ✅ `go build ./...` passes. ✅ `go test -race ./...` passes (all 24 pkgs) — race intermittent, not consistently reproducible but confirmed by detector. URGENT still open.
**Commits reviewed this cycle:** No new commits; unstaged: `client.go` (applyConfigDiff `changed` bool optimization, Reload method, serverConfigEqual), `config_watch.go`/`config_watch_test.go`/`reload_test.go` untracked new files

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

- **[MISSING TEST / DOC MISMATCH]** `pkg/mcp/completions.go` — `CompletePromptArg` and
  `CompleteResourceURI` both say "Returns an empty slice (not an error) when the server does not
  support completions." But the implementation passes through any error from `session.Complete`.
  If the server doesn't support the `completion/complete` method, callers get a JSON-RPC error
  rather than an empty slice. Either: (a) update the comment to say errors are propagated, or
  (b) add error-type detection to convert "method not found" into an empty result.
  **Files:** `pkg/mcp/completions.go:12,29`

### Simplification

- **[DUPLICATE FUNCTION]** `pkg/mcp/client.go` — `configsEqual` (line 421) and `serverConfigEqual`
  (line 547) are identical: both JSON-marshal both args and compare strings. One should be
  removed; `Reload` and `applyConfigDiff` should share a single helper.
  **Files:** `pkg/mcp/client.go:421,547`

---

## Resolved This Cycle ✅

- **✅ COMMITTED (config_watch.go)** `WatchConfig` dual-implementation conflict self-resolved; `config_watch_test.go` and `reload_test.go` added with comprehensive coverage.
- **✅ COMMITTED (client.go)** `Reload` + `applyConfigDiff` hot-reload methods added; `serverConfigEqual` added (duplicate of `configsEqual` — filed in Simplification).
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
