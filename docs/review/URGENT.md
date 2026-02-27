# URGENT — 2026-02-27

## Active Issues

### `pkg/mcp/tool_adapter.go:117` + `pkg/mcp/tool_adapter_test.go:193` — Audio content: double MIME prefix + stale test

**Filed:** 2026-02-27 ~01:57 PST

**Test failure:**
```
--- FAIL: TestConvertResult_UnknownContentType
    expected: "[unsupported MCP content type]"
    actual  : "[audio/audio/mpeg] AQI="
```

**Root cause 1 — double prefix bug (correctness):**
`tool_adapter.go:117` formats audio as `fmt.Sprintf("[audio/%s] %s", mime, ...)` where `mime`
is already `"audio/mpeg"`. This produces `"[audio/audio/mpeg] ..."` — the `audio/` prefix is
doubled. Fix: use `"[%s]"` instead of `"[audio/%s]"`:
```go
Text: fmt.Sprintf("[%s] %s", mime, base64.StdEncoding.EncodeToString(v.Data)),
```

**Root cause 2 — stale test:**
`TestConvertResult_UnknownContentType` was written when `*sdk.AudioContent` fell through to the
`default` case. Now that audio has an explicit case, the test hits that path instead. The test
must be updated to:
- Expect `"[audio/mpeg] AQI="` (once root cause 1 is fixed) for the audio case.
- Rename the test or add a separate one covering the true `default` branch. Note: triggering
  the `default` branch may require a mock content type not natively handled by the SDK.

**Root cause 3 — stale comment:**
`tool_adapter.go:151` still reads `// Audio, resource links, etc.` in the `default` branch, but
audio and resource links are now explicitly handled above. Update to `// Catch-all for any future
MCP content types not yet handled.` or similar.

**Files:** `pkg/mcp/tool_adapter.go:117`, `pkg/mcp/tool_adapter.go:151`, `pkg/mcp/tool_adapter_test.go:193`

---

## Recently Fixed ✅

### `pkg/mcp/elicitation_test.go` — missing `agent` import ✅ FIXED `dee08d8`

### `pkg/mcp/sampling.go` — Build break: 5 compile errors ✅ FIXED (2026-02-27)
- Variable shadowing (`ctx := ai.Context{...}`) fixed: renamed to `prompt`
- `ai.StopReasonEndTurn` → `ai.StopReasonStop`, `ai.StopReasonMaxTokens` → `ai.StopReasonLength`
- `sampling_test.go` added with unit + integration tests.
Gates on `strings.HasPrefix(c.MIMEType, "image/")`. Non-image blobs returned as base64 text.
Tests: `TestConvertResourceResult/non-image blob`, `TestConvertResourceResult/blob no mime`.

### `pkg/mcp/tool_adapter.go:87` — `*sdk.ImageContent` with non-image MIME unconditionally tagged `ContentTypeImage` ✅ FIXED (unstaged, `TestConvertResult_NonImageContent` added)
Same guard applied: `strings.HasPrefix(v.MIMEType, "image/")`.

### `pkg/mcp/tool_adapter.go` — `defer registry.unregister` races with SDK notification goroutine ✅ FIXED `6ddadcd`
Removed eager unregister; registry entries live for session lifetime.

### `pkg/mcp/debug_test.go` — committed debug investigation file ✅ CLEANED UP

### `pkg/mcp/client_test.go` — `TestManager_ProgressNotification` data race ✅ FIXED `9708bd2`

### `pkg/mcp/client.go:commandTransport` — subprocess env drops `os.Environ()` ✅ FIXED `ef4f139`

### Earlier pre-MCP fixes ✅
- `pkg/tui/components/input.go` — `handleBackspace` undo leak ✅ FIXED 2026-02-23
- `cmd/fir/app.go:33` — `//go:embed CHANGELOG.md` build break ✅ FIXED `ce07547`
- `pkg/core/compaction/runner_test.go:110` — `TokensBefore` always 0 ✅ FIXED 2026-02-19
- `pkg/ai/oauth/callback_server.go:25` — Reflected XSS ✅ FIXED
- `pkg/core/agentsession.go:636` — `WrapToolsWithHooks` never called ✅ FIXED
- `pkg/extensions/notify/notify.go:30` — Direct `os.Stdout` write ✅ FIXED
- `pkg/core/authstorage.go:401-446` — Deadlock in `refreshOAuthToken` ✅ FIXED
- `pkg/modes/acp/acp.go:381` — Non-zero exit code not returned as error ✅ FIXED
- `pkg/modes/acp/acp.go:82` — No session cleanup on exit ✅ FIXED
- `pkg/modes/interactive/mode.go:1806` — `proc *exec.Cmd` data race ✅ FIXED `ce07547`
- `pkg/core/agentsession.go:686` — `ScopedModelsRef()`/`SetScopedModels()` no mutex guards ✅ FIXED `ce07547`
- `pkg/modes/acp/terminal.go` — `pendingBashTerminals` leak on abort ✅ FIXED `ce07547`
