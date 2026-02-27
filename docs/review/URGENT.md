# URGENT — 2026-02-27

## Active Issues

### `pkg/mcp/elicitation_test.go` — Test build break: missing `agent` import

**Filed:** 2026-02-27 ~01:42 PST

**Errors:**
```
elicitation_test.go:101:12: undefined: agent
elicitation_test.go:156:12: undefined: agent
```

**Root cause:** `elicitation_test.go` uses `*agent.AgentTool` (lines 101, 156) but does not import
`"github.com/kfet/fir/pkg/agent"`.

**Fix:** Add the missing import:
```go
import (
    ...
    "github.com/kfet/fir/pkg/agent"
    ...
)
```

**Files:** `pkg/mcp/elicitation_test.go`

---

## Recently Fixed ✅

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
