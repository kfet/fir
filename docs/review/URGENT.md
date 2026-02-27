# URGENT — 2026-02-27

## Active Issues

*(none — all previously filed issues have been resolved)*

---

## Recently Fixed ✅

### `pkg/mcp/tool_adapter.go` — `defer registry.unregister` races with SDK notification goroutine ✅ FIXED `7f336a9`
The `defer registry.unregister(toolCallID)` was removed. The code now includes an explicit comment
explaining why: the SDK's `readIncoming` goroutine unblocks `CallTool` (via the response path)
concurrently with `handleAsync` still dispatching an earlier progress notification, so removing
the callback immediately after `CallTool` returns would silently drop in-flight notifications.
`TestManager_ProgressNotification` now passes reliably with `-race -count=20`.

### `pkg/mcp/debug_test.go` — debug investigation file with `fmt.Printf` ✅ FIXED
File was never committed; cleaned up from working tree before commit.

---

## Previously Fixed (all ✅)

### `pkg/mcp/client_test.go:384-400` — `TestManager_ProgressNotification` data race ✅ FIXED `9708bd2`
- `close(updates) + range` replaced with `select + timeout` pattern.

### `pkg/mcp/client.go:commandTransport` — subprocess env drops `os.Environ()` ✅ FIXED `ef4f139`

### `pkg/tui/components/input.go` — `handleBackspace` undo leak ✅ FIXED 2026-02-23

### `cmd/fir/app.go:33` — `//go:embed CHANGELOG.md` build break ✅ FIXED `ce07547`

### `pkg/core/compaction/runner_test.go:110` — `TokensBefore` always 0 ✅ FIXED 2026-02-19

### `pkg/ai/oauth/callback_server.go:25` — Reflected XSS in OAuth error page ✅ FIXED

### `pkg/core/agentsession.go:636` — `WrapToolsWithHooks` never called ✅ FIXED

### `pkg/extensions/notify/notify.go:30` — Direct `os.Stdout` write ✅ FIXED

### `pkg/core/authstorage.go:401-446` — Deadlock in `refreshOAuthToken` ✅ FIXED

### `pkg/modes/acp/acp.go:381` — Non-zero exit code not returned as error ✅ FIXED

### `pkg/modes/acp/acp.go:82` — No session cleanup on exit ✅ FIXED

### `pkg/modes/interactive/mode.go:1806` — `proc *exec.Cmd` data race in `performShare` ✅ FIXED `ce07547`

### `pkg/core/agentsession.go:686` — `ScopedModelsRef()`/`SetScopedModels()` no mutex guards ✅ FIXED `ce07547`

### `pkg/modes/acp/terminal.go` — `pendingBashTerminals` leak on abort ✅ FIXED `ce07547`
