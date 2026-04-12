# Task: Add `replace` and `error` support to poe-bridge reply tool

## Worktree
`~/dev/ai/fir-wt-poe-ux` — branch `poe-ux`

## Goal
Two features, both additive and backward-compatible:

1. **`replace` flag** — emits `replace_response` SSE event instead of `text`, letting fir show progress then overwrite it
2. **`error` flag** — emits `error` SSE event with optional `error_type`, giving Poe a clean error UI with retry button

## Files to Change

### 1. `external/poe/internal/router/router.go`

Add two fields to the `Chunk` struct:

```go
type Chunk struct {
    Text      string
    Final     bool
    Replace   bool   // emit replace_response instead of text
    IsError   bool   // emit error event instead of text
    ErrorType string // optional: "user_caused_error", "user_message_too_long"
}
```

No other changes in this file.

### 2. `external/poe/cmd/poe-bridge/main.go`

**a) Expand `replyArgs`:**

```go
type replyArgs struct {
    MessageID string `json:"message_id" jsonschema:"the Poe message_id from the inbound channel notification"`
    Text      string `json:"text" jsonschema:"text chunk to append to the reply; may be empty on a final=true call"`
    Final     bool   `json:"final,omitempty" jsonschema:"set true to mark the last chunk; closes the SSE stream on the Poe side"`
    Replace   bool   `json:"replace,omitempty" jsonschema:"if true, replace all prior text instead of appending"`
    Error     bool   `json:"error,omitempty" jsonschema:"if true, emit as error event instead of text"`
    ErrorType string `json:"error_type,omitempty" jsonschema:"error type: user_caused_error or user_message_too_long"`
}
```

**b) Update both reply tool handlers** (local router ~line 216, and relay ~line 565) to pass new fields through to the Chunk:

```go
rt.Push(args.MessageID, router.Chunk{
    Text:      args.Text,
    Final:     args.Final,
    Replace:   args.Replace,
    IsError:   args.Error,
    ErrorType: args.ErrorType,
})
```

Same pattern for the relay reply tool.

**c) Update the SSE loop in `newOnQuery`** (~line 173):

Replace this:
```go
if chunk.Text != "" {
    if err := sse.WriteEvent("text", map[string]any{"text": chunk.Text}); err != nil {
        return err
    }
}
```

With this:
```go
if chunk.IsError {
    data := map[string]any{"allow_retry": true, "text": chunk.Text}
    if chunk.ErrorType != "" {
        data["error_type"] = chunk.ErrorType
    }
    if err := sse.WriteEvent("error", data); err != nil {
        return err
    }
} else if chunk.Replace {
    if err := sse.WriteEvent("replace_response", map[string]any{"text": chunk.Text}); err != nil {
        return err
    }
} else if chunk.Text != "" {
    if err := sse.WriteEvent("text", map[string]any{"text": chunk.Text}); err != nil {
        return err
    }
}
```

### 3. Tests

**`external/poe/internal/router/router_test.go`** — update any test that constructs `Chunk{}` to still compile (existing tests should pass as-is since new fields are zero-valued).

**`external/poe/cmd/poe-bridge/main_test.go`** — add test cases for:
- Reply with `replace=true` emits `replace_response` event
- Reply with `error=true` emits `error` event with `allow_retry: true`
- Reply with `error=true, error_type="user_caused_error"` includes the error_type
- Normal reply still emits `text` event (regression check)

### 4. Relay agent (`external/poe/internal/agent/`)

If there's a relay agent Reply method, extend its wire format to carry `replace`, `error`, `error_type`. Check `agent.go` for the message struct.

## Do NOT change
- No new tools — everything goes through the existing `reply` tool
- No changes to `internal/poe/poe.go` (SSEWriter already handles arbitrary event names)
- No changes to the meta event or settings response
- No changes to the channel notification format

## Validation
```bash
cd ~/dev/ai/fir-wt-poe-ux/external/poe
go test ./...
go vet ./...
```
