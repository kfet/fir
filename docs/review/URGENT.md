# URGENT — 2026-02-27

## Active Issues

### `pkg/mcp/config_watch.go` + `pkg/mcp/config.go` — Build break: `WatchConfig` redeclared in two files

**Filed:** 2026-02-27 ~02:11 PST

**Error:**
```
pkg/mcp/config_watch.go:22:6: WatchConfig redeclared in this block
    pkg/mcp/config.go:128:6: other declaration of WatchConfig
```

**Root cause:** Two implementations were written simultaneously:
- `config_watch.go` — `func WatchConfig(path string, onChange func(*ConfigFile)) (stop func(), err error)` — non-blocking, directory watch (atomic-rename safe), 200ms debounce.
- `config.go` (unstaged) — `func WatchConfig(ctx context.Context, path string, onReload func(*ConfigFile)) error` — blocking, direct file watch (misses atomic renames), no debounce.

**Recommendation:** Keep `config_watch.go` (better design — directory watch, debounce, stop function).
Remove the `WatchConfig` function and its new imports (`context`, `fsnotify`, `log/slog`) from `config.go`.

**Files to change:** `pkg/mcp/config.go` (remove the new WatchConfig block and unused imports)

---

## Recently Fixed ✅

### `pkg/mcp/server_test.go:93` — `TestNewToolServer_CallTool` fails: arguments base64-encoded ✅ FIXED `5af6d82`

### `pkg/mcp/tool_adapter.go:117` — Audio double MIME prefix + stale test ✅ FIXED `7c90b2d`
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
