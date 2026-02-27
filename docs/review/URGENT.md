# URGENT — 2026-02-27

## Active Issues

### `pkg/mcp/client.go:212,238` — Data race on `m.OnToolsChanged` in `ToolListChangedHandler`

**Filed:** 2026-02-27 ~02:14 PST

**Race detector output:**
```
WARNING: DATA RACE
Read at ... by goroutine N:
  github.com/kfet/fir/pkg/mcp.(*Manager).startServer.func2()
      client.go:212
```

**Root cause:** `ToolListChangedHandler` (the closure registered during `startServer`) reads
`m.OnToolsChanged` at line 212 (nil check) and calls it at line 238 — BOTH without holding
`m.mu`. The field can be written concurrently by any goroutine that sets `mgr.OnToolsChanged`.

Additionally, `applyConfigDiff` also calls `m.OnToolsChanged(all)` at line ~541 outside the
lock. And the handler goroutine from a closed (old) session can run after the server is replaced,
overwriting `m.tools[serverName]` with stale tools from the old session.

**Test failure:** `TestWatchAndReload_SwapServer` fails because a `ToolListChangedHandler`
goroutine spawned by serverA's session races with `applyConfigDiff`'s tool update, causing
`OnToolsChanged` to be called with `tool-a` instead of `tool-b`.

**Fix options:**
1. Use a mutex-protected accessor for `OnToolsChanged`: read it under `m.mu`, then call outside.
2. In `ToolListChangedHandler`, compare `serverName` against the active session before writing.
   Guard: if `m.sessions[serverName] != req.Session`, the session is stale — skip the update.

Concrete change for (2) in the goroutine:
```go
m.mu.Lock()
if m.sessions[serverName] != req.Session {
    m.mu.Unlock()
    return // stale session — skip
}
m.tools[serverName] = updated
all := m.allTools()
onChanged := m.OnToolsChanged
m.mu.Unlock()
if onChanged != nil {
    onChanged(all)
}
```

**Files:** `pkg/mcp/client.go:211-238`, `pkg/mcp/client.go:~541`

**Severity:** Medium-High — data race is intermittent (not consistently reproducible) but was confirmed by `-race` detector. The incorrect-tools result during hot-reload is a real correctness risk.

---

## Recently Fixed ✅

### `pkg/mcp/config_watch.go` + `pkg/mcp/config.go` — `WatchConfig` redeclared ✅ FIXED (self-resolved)

### `pkg/mcp/server_test.go:93` — arguments base64-encoded ✅ FIXED `5af6d82`

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
