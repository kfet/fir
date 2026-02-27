# URGENT — 2026-02-27

## Active Issues

### `pkg/mcp/resources.go:85-95` — blob resources unconditionally tagged `ContentTypeImage`
**Commits:** new file in working tree (unstaged), reviewed 2026-02-27
**Severity:** CORRECTNESS — non-image blobs (PDF, audio, etc.) are silently dropped or cause API errors

`convertResourceResult` maps every blob resource to `ai.ContentTypeImage`:
```go
case len(c.Blob) > 0:
    content = append(content, ai.ToolResultContent{
        Type:     ai.ContentTypeImage,   // WRONG for non-image blobs
        Data:     base64.StdEncoding.EncodeToString(c.Blob),
        MimeType: c.MIMEType,
    })
```

Provider consequences:
- **Anthropic**: sends `{"type":"image","source":{"media_type":"application/pdf",...}}` → API rejects unknown image type → tool result call fails with an API error.
- **Models without image support**: `IsImage()` check causes the blob to be **silently dropped** from the tool result; LLM sees an empty result.
- **Image-capable models**: a PDF blob is passed as base64 image data → LLM produces incorrect or garbled output.

**Fix:** Gate on mime type. Only use `ContentTypeImage` for `image/*` mime types; for everything else, return the data as base64 text with a note:
```go
case len(c.Blob) > 0:
    if strings.HasPrefix(c.MIMEType, "image/") {
        content = append(content, ai.ToolResultContent{
            Type:     ai.ContentTypeImage,
            Data:     base64.StdEncoding.EncodeToString(c.Blob),
            MimeType: c.MIMEType,
        })
    } else {
        content = append(content, ai.ToolResultContent{
            Type: ai.ContentTypeText,
            Text: fmt.Sprintf("[binary resource: %s, base64-encoded]\n%s",
                c.MIMEType, base64.StdEncoding.EncodeToString(c.Blob)),
        })
    }
```

**Note:** `pkg/mcp/tool_adapter.go:87` has the same pattern for `*sdk.ImageContent` from tool call results — that case is safer since MCP's `ImageContent` is supposed to be an image, but the same mime-type guard would be more robust there too.

---

## Recently Fixed ✅

### `pkg/mcp/tool_adapter.go` — `defer registry.unregister` races with SDK notification goroutine ✅ FIXED `6ddadcd`
Formally committed as `fix(mcp): avoid progress-callback race by keeping registration alive`.

### `pkg/mcp/debug_test.go` — committed debug investigation file ✅ FIXED
File was never committed; cleaned up before reaching main.

### `pkg/mcp/client_test.go:384-400` — `TestManager_ProgressNotification` data race ✅ FIXED `9708bd2`

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
