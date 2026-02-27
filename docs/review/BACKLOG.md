# Review Backlog — 2026-02-27

**Last reviewed:** MCP watch cycle 13, 2026-02-27 ~01:37 PST
**Build status:** ❌ `go build ./...` FAILS — `pkg/mcp/sampling.go` 5 compile errors (see URGENT.md).
**Commits reviewed this cycle:** `4b1970e` (prompts feature); unstaged: `sampling.go` (new, broken), `client.go` (SamplingFn + CreateMessageHandler wiring)

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

### Test Coverage

- **[MISSING TEST]** `pkg/mcp/sampling.go` — no `sampling_test.go`. Should cover:
  `NewSamplingFn` (text+image messages, max tokens, temperature, nil result, error result),
  `samplingMessagesToAI` (user text, user image, assistant text, unsupported role),
  `assistantToCreateMessageResult` (text, stop reasons), `samplingStopReason` (all cases).
  **Files:** `pkg/mcp/sampling_test.go` (to be created)

---

## Resolved This Cycle ✅

- **✅ SELF-FIXED prompts.go** `pkg/mcp/prompts.go` — null→`[]` fix applied (`if prompts == nil { prompts = []promptInfo{} }`); small blobs (≤4096 bytes) now inline as base64 in `formatEmbeddedResourceContent`.
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
