# URGENT — 2026-02-27

## Active Issues

*(none)*

---

## Recently Fixed ✅

### `pkg/mcp/reload_test.go:7` — Unused `"sync"` import breaks build ✅ FIXED (self-resolved)
The import WAS used — `TestManager_Reload_Concurrent` uses `sync.WaitGroup`. Reviewer saw a
stale working-tree snapshot before the concurrent test was written. Build is green.

### `pkg/mcp/client.go:212,238` — Data race on `m.OnToolsChanged` in `ToolListChangedHandler` ✅ FIXED `12734c4`
### `pkg/mcp/config_watch.go` + `pkg/mcp/config.go` — `WatchConfig` redeclared ✅ FIXED (self-resolved)
### `pkg/mcp/server_test.go:93` — arguments base64-encoded ✅ FIXED `5af6d82`
### `pkg/mcp/tool_adapter.go:117` — Audio double MIME prefix + stale test ✅ FIXED `7c90b2d`
### `pkg/mcp/tool_adapter.go:87` — `*sdk.ImageContent` with non-image MIME unconditionally tagged `ContentTypeImage` ✅ FIXED
### `pkg/mcp/tool_adapter.go` — `defer registry.unregister` races with SDK notification goroutine ✅ FIXED `6ddadcd`
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
