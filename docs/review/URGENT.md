# URGENT — 2026-02-27

## Active Issues

### `pkg/mcp/sampling.go` — Build break: 5 compile errors

**Filed:** 2026-02-27 ~01:35 PST

**Errors:**
```
sampling.go:31:7: no new variables on left side of :=
sampling.go:31:10: cannot use ai.Context{…} as context.Context
sampling.go:44:53: cannot use ctx (context.Context) as ai.Context
sampling.go:119:10: undefined: ai.StopReasonEndTurn
sampling.go:121:10: undefined: ai.StopReasonMaxTokens
```

**Root causes:**

1. **Variable shadowing / type mismatch (lines 30–44):**
   `ctx := ai.Context{...}` shadows the `ctx context.Context` parameter.
   `ai.Context` is the fir prompt context (not `context.Context`).
   The call `ai.CompleteSimple(ctx, registry, model, ctx, opts)` then passes the same variable for both the `context.Context` arg and the `ai.Context` arg.
   **Fix:** Rename the AI context variable:
   ```go
   prompt := ai.Context{
       SystemPrompt: p.SystemPrompt,
       Messages:     msgs,
   }
   result := ai.CompleteSimple(ctx, registry, model, prompt, opts)
   ```

2. **Undefined constants (lines 119, 121):**
   `ai.StopReasonEndTurn` and `ai.StopReasonMaxTokens` do not exist in `pkg/ai/types.go`.
   The correct constants are `ai.StopReasonStop` and `ai.StopReasonLength`.
   **Fix:**
   ```go
   case ai.StopReasonStop:
       return "endTurn"
   case ai.StopReasonLength:
       return "maxTokens"
   ```

**Files:** `pkg/mcp/sampling.go:30–44`, `pkg/mcp/sampling.go:119,121`

---

## Recently Fixed ✅

### `pkg/mcp/resources.go` — blob resources unconditionally tagged `ContentTypeImage` ✅ FIXED `5e9235d`
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
