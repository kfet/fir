# URGENT — 2026-02-09

## Build Break

### ~~`pkg/modes/interactive/components/`~~ — ✅ FIXED: Import cycle and empty stub resolved

### ~~`pkg/modes/print/print_integration_test.go:52`~~ — ✅ FIXED: Test no longer panics

### ~~`pkg/core/timings.go:62`~~ — ✅ FIXED: Redundant newline removed, vet passes.

### ~~`pkg/modes/interactive/components/`~~ — ✅ FIXED: `shortenPath` now defined, build passes.

---

## Security

<!-- Fixed: API key moved from URL query parameter to x-goog-api-key header -->

<!-- Skipped: needs design discussion — Claude Code impersonation is intentional upstream behavior -->
### `pkg/ai/providers/anthropic.go:22-28` — Claude Code impersonation
```go
const claudeCodeVersion = "2.1.2"
// ...
headers["user-agent"] = fmt.Sprintf("claude-cli/%s (external, cli)", claudeCodeVersion)
headers["x-app"] = "cli"
```
When an OAuth token is detected, the code impersonates Claude Code. This is a ToS risk and could be considered credential misuse. The hardcoded version (`2.1.2`) will also drift from reality, potentially causing issues when Anthropic rolls version checks.

**Fix:** Use a pi-specific user-agent string (e.g., `pi-go/0.1`) and dedicated beta features, or document this as an intentional upstream-matching decision.

<!-- Skipped: needs design discussion — requires adding fields to StreamOptions, cross-cutting change -->
### `pkg/ai/providers/anthropic.go:283-286` — Thinking config smuggled through HTTP headers
```go
base.Headers["x-anthropic-thinking-enabled"] = "true"
base.Headers["x-anthropic-thinking-budget"] = fmt.Sprintf("%d", thinkingBudget)
```
Internal configuration is passed through the `Headers` map, then conditionally stripped in `buildAnthropicHeaders`. This is fragile — if any code path skips the stripping, internal state leaks to the API. The `buildAnthropicParams` function also reads these back.

**Fix:** Add explicit `ThinkingEnabled bool` and `ThinkingBudget int` fields to `StreamOptions` or a separate `AnthropicOptions` struct. Don't repurpose the headers map as an internal bus.

---

## Concurrency

### ~~`pkg/core/session.go:370-392`~~ — ✅ FIXED: Reader methods now protected by `RWMutex`
Changed `sync.Mutex` → `sync.RWMutex`. All 16 read accessors now hold `RLock()`. Write methods (`Append*`, `Branch*`, etc.) use full `Lock()`.
