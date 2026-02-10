# Review Backlog — 2026-02-09

**Last reviewed:** Cycle 13, 2026-02-09 20:50 PST — 96 staged + 23 unstaged `.go` files.
**Build status:** ✅ PASSING — `go vet` clean, all 14 testable packages pass, `-race` clean.
**Recent fixes:** Compaction tests (10 new), loop edge case tests (4 new), session race tests, `shortenPath` TUI fix.

---

## Simplification

<!-- Skipped: needs design discussion — agent.ThinkingLevel vs ai.ThinkingLevel touches 9+ files across cmd/ and pkg/ -->
### `pkg/agent/types.go` — Duplicate `ThinkingLevel` type
`agent.ThinkingLevel` duplicates `ai.ThinkingLevel` with an added `"off"` value. Consider using `ai.ThinkingLevel` directly with an empty string representing "off", or a single shared type. The `ToAIThinkingLevel()` method is a code smell indicating the duplication.

### `pkg/ai/providers/openai.go` + `anthropic.go` + `bedrock.go` + `google.go` — Providers ignore the passed `context.Context`
All streaming providers start goroutines that create a fresh `context.Background()`:
```go
// anthropic.go:168
reqCtx := context.Background()
// openai.go:89
runCtx := context.Background()
```
The outer context from the agent loop (which carries cancellation for `Abort()`) is never threaded through. This means **aborting a stream doesn't cancel the HTTP request**. The goroutine continues reading from the provider until it naturally completes.

**Fix:** Accept `context.Context` in the stream function signature or thread the cancellation context into the goroutine. This is both a simplification (remove the detached context) and a correctness fix.

### `pkg/core/session.go:297-305` — UUID collision retry loop
```go
func (sm *SessionManager) generateID() string {
    for i := 0; i < 100; i++ {
        id := uuid.New().String()[:8]
        if _, ok := sm.byID[id]; !ok {
            return id
        }
    }
    return uuid.New().String()
}
```
Truncating UUIDs to 8 chars (32 bits) creates meaningful collision risk at ~65K entries (birthday bound). The 100-retry loop is fine for safety, but the full UUID fallback has a different length format than the truncated ones, which could cause subtle issues. Consider using a longer prefix (12+ chars) to reduce collision probability.

### `pkg/core/settings.go:123-155` — `deepMergeSettings` is over-complex
Round-tripping through JSON to merge two typed structs is expensive and fragile. A direct field-by-field merge (or using `mergo` library) would be clearer and avoid silent data loss from marshal errors.

---

## Security

<!-- Removed: google.go API key leak — fixed, error is now wrapped without URL -->

<!-- Skipped: needs design discussion — Claude Code impersonation is a product decision, not a code bug -->
### `pkg/ai/providers/anthropic.go:22-28` — Claude Code impersonation (see URGENT.md)

<!-- Skipped: by design, matches upstream TS behavior -->
### `pkg/core/tools/bash.go` — No command sanitization or sandboxing

<!-- Skipped: by design, matches upstream TS behavior -->
### `pkg/core/tools/write.go:64` — Path traversal: write accepts absolute paths

---

## Test Coverage

---

## Correctness (TS→Go Port)

<!-- Removed: read.go FormatSize — not a bug, reviewer noted this -->
### `pkg/core/tools/read.go:159` — `FormatSize` called with character count, not byte count
```go
firstLineSize := FormatSize(len(allLines[startLine]))
```
`len(string)` returns bytes in Go, which is correct for `FormatSize`. But the message says "exceeds limit" comparing to `DefaultMaxBytes` — this is fine. No bug, just noting the subtle Go `len` semantics are correct here.

### `pkg/agent/agent.go:368-369` — `AgentLoop` and `AgentLoopContinue` return values ignored
```go
go func() {
    if messages != nil {
        AgentLoop(ctx, messages, agentCtx, config, streamFn, events)
    } else {
        AgentLoopContinue(ctx, agentCtx, config, streamFn, events)
    }
    close(events)
}()
```
Both functions return `[]AgentMessage` but the return values are discarded. The results are communicated via the events channel, so this is probably fine, but the unused return values suggest the API could be simplified.
