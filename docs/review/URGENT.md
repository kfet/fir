# URGENT — 2026-02-26

## Active Issues

### `pkg/mcp/tool_adapter.go:50-60` — `defer registry.unregister` races with SDK notification goroutine
**Commits:** introduced in `fa21bd2` (progress tracking feature), test failing now  
**Severity:** BLOCKER — `go test -race ./pkg/mcp/...` FAILS with timeout every run

**Root cause:** The SDK's `jsonrpc2` layer processes incoming notifications via a `handleAsync` goroutine that runs *concurrently* with the response delivery to `CallTool`. The sequence is:

1. Server sends progress notification → queued in `handleAsync` goroutine  
2. Server sends tool result → directly unblocks `session.CallTool` (response path is separate from notification path)  
3. `CallTool` returns → `Execute` returns → **`defer registry.unregister(toolCallID)` fires** — removes callback
4. `handleAsync` goroutine runs → calls `m.progressReg.dispatch(token, ...)` → callback is gone → notification silently lost
5. Test times out waiting for notification in `updates` channel

The comment in `TestManager_ProgressNotification` says "notification is delivered before the tool result (MCP message ordering), so it is already buffered by the time Execute returns" — but this is wrong. The notification is *received* by the client's reader goroutine before the response, but it's *dispatched* by a separate `handleAsync` goroutine that runs concurrently with `CallTool`'s return path.

**Files:** `pkg/mcp/tool_adapter.go:50-60`

**Suggested fix:**  
Remove the `defer registry.unregister(toolCallID)` call entirely. Since each `toolCallID` is unique per invocation, registry entries won't collide. The entries are cleaned up naturally when `Manager.Close()` is called (the `Manager` and its `progressReg` are garbage collected). If memory pressure from accumulated entries is a concern, add cleanup to `Manager.Close()`:

```go
// In AdaptTool.Execute — remove the defer:
// BEFORE (racy):
//   registry.register(toolCallID, onUpdate)
//   defer registry.unregister(toolCallID)

// AFTER (correct):
//   registry.register(toolCallID, onUpdate)
//   // No defer unregister — the SDK dispatches notifications asynchronously
//   // after CallTool returns. Cleanup happens in Manager.Close().
```

Alternatively, add an explicit drain in `Manager.Close()`:
```go
// In Manager.Close(), after closing sessions:
m.progressReg.m.Range(func(k, v any) bool {
    m.progressReg.m.Delete(k)
    return true
})
```

---

### `pkg/mcp/debug_test.go` — committed debug investigation file should be removed
**Commits:** untracked file added while investigating progress notification behavior  
**Severity:** MINOR BLOCKER — `fmt.Printf` in test file writes directly to stdout (not via `t.Log`), polluting CI output; file is clearly not production-ready

The `TestDebugProgressToken` test is a debugging artifact. It writes:
```go
fmt.Printf("token type: %T, value: %v\n", receivedToken, receivedToken)
```
This raw stdout write appears in test output even when tests pass. The test should either be deleted or converted to use `t.Logf` only.

**Files:** `pkg/mcp/debug_test.go` (untracked, appears in test output)  
**Suggested fix:** Delete `pkg/mcp/debug_test.go` — the debugging investigation is complete and `TestManager_ProgressNotification` covers the same scenario.

---

## Recently Fixed ✅

### `pkg/mcp/client_test.go:384-400` — `TestManager_ProgressNotification` data race ✅ PARTIALLY FIXED `fa21bd2`
The `close(updates) + range` pattern (which closed a channel that a notification goroutine might still be writing to) was replaced with a `select + timeout` pattern in the unstaged diff. However, the test still **fails with timeout** because the underlying `defer registry.unregister` removes the callback before the notification is dispatched. See new URGENT issue above.

---

### `pkg/mcp/client.go:commandTransport` — subprocess env drops `os.Environ()` ✅ FIXED ef4f139

---

## Previously Fixed ✅

- ~~`pkg/tui/components/input.go` — `handleBackspace` pushes undo on every keystroke, `UndoStack.Push` leaks evicted strings~~ — ✅ FIXED 2026-02-23
- ~~`cmd/fir/app.go:33` — `//go:embed CHANGELOG.md` build break~~ — ✅ FIXED (cycle 58 / rebase ce07547)
- ~~`pkg/core/compaction/runner_test.go:110` — `TestDefaultRunner_GetStats_WithMessages` fails: `TokensBefore` always 0~~ — ✅ FIXED (2026-02-19)
- ~~`pkg/core/compaction/runner_test.go:99` — `ai.Message{Role: ...}` unknown struct field~~ — ✅ FIXED
- ~~`pkg/modes/acp/acp_test.go:586,610` — `chunk.Content` used as string (type `ContentBlock`)~~ — ✅ FIXED 2026-02-18
- ~~`pkg/modes/acp/acp.go:929-937` — Extension commands dispatched via `Prompt()` instead of `ExecuteCommand()`~~ — ✅ FIXED 2026-02-18
- ~~`pkg/modes/acp/acp.go:795` — `RunCompaction()` called with wrong signature (no args)~~ — ✅ FIXED 2026-02-18
- ~~`pkg/core/tools/read.go:110` — `filepath.Ext` regression for dotfiles~~ — ✅ FIXED 2026-02-19
- ~~`pkg/ai/oauth/callback_server.go:25` — Reflected XSS in OAuth error page~~ — ✅ FIXED
- ~~`pkg/core/agentsession.go:636` — `WrapToolsWithHooks` never called~~ — ✅ FIXED
- ~~`pkg/extensions/notify/notify.go:30` — Direct `os.Stdout` write~~ — ✅ FIXED
- ~~`pkg/core/authstorage.go:401-446` — Deadlock in `refreshOAuthToken`~~ — ✅ FIXED
- ~~`pkg/modes/acp/acp.go:381` — Non-zero exit code not returned as error~~ — ✅ FIXED
- ~~`pkg/modes/acp/acp.go:82` — No session cleanup on exit~~ — ✅ FIXED
- ~~`pkg/modes/interactive/mode.go:1806` — `proc *exec.Cmd` data race in `performShare`~~ — ✅ FIXED (cycle 96)
- ~~`pkg/core/agentsession.go:686` — `ScopedModelsRef()`/`SetScopedModels()` no mutex guards~~ — ✅ FIXED (ce07547)
- ~~`pkg/modes/acp/terminal.go` — `pendingBashTerminals` leak on abort~~ — ✅ FIXED (ce07547)
