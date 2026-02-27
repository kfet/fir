# URGENT — 2026-02-27

## Active Issues

### `pkg/mcp/server_test.go:93` — `TestNewToolServer_CallTool` fails: arguments base64-encoded instead of JSON

**Filed:** 2026-02-27 ~02:04 PST

**Error:**
```
--- FAIL: TestNewToolServer_CallTool
    expected: "hello MCP"
    actual  : "invalid arguments: json: cannot unmarshal string into Go value of type map[string]interface {}"
```

**Root cause:** `CallToolParams.Arguments` is typed `any`. When the test passes a `[]byte`
(the result of `json.Marshal(...)`) as `any`, Go's JSON encoder base64-encodes the byte slice
instead of embedding it as raw JSON. The server handler receives a base64 string and fails to
unmarshal it into `map[string]any`.

**Fix:** Wrap the marshaled bytes as `json.RawMessage` before passing, OR pass the map directly:

Option A (minimal fix):
```go
// server_test.go line 92
args, _ := json.Marshal(map[string]any{"input": "hello MCP"})
res, err := session.CallTool(ctx, &sdk.CallToolParams{
    Name:      "echo",
    Arguments: json.RawMessage(args),  // ← wrap as RawMessage
})
```

Option B (cleaner):
```go
res, err := session.CallTool(ctx, &sdk.CallToolParams{
    Name:      "echo",
    Arguments: map[string]any{"input": "hello MCP"},  // ← pass map directly
})
```

**Files:** `pkg/mcp/server_test.go:90-95`

---

## Recently Fixed ✅

### `pkg/mcp/tool_adapter.go:117` — Audio double MIME prefix + stale test ✅ FIXED `7c90b2d`

### `pkg/mcp/elicitation_test.go` — missing `agent` import ✅ FIXED `dee08d8`

### `pkg/mcp/sampling.go` — Build break: 5 compile errors ✅ FIXED
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
