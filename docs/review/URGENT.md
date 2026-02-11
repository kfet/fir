# URGENT — 2026-02-10

### ~~E2E: RPC mode stdin consumed by readPipedStdin()~~ — ✅ FIXED
Guarded `readPipedStdin()` with `args.OutputMode != ModeRPC` so RPC server can read stdin directly.

---

### ~~E2E: RPC mode not wired up in app.go~~ — ✅ FIXED
Added `isRPCMode` check in `run()`. When `args.OutputMode == ModeRPC`, dispatches to `rpcmode.NewServer(result.Session).Run()`. Import added for `pkg/modes/rpc`.

---

### ~~E2E: `--list-models` flag not handled~~ — ✅ FIXED
Added `runListModels()` function and early-return in `run()` after `--version` check. Lists all models as `provider/id`, supports pattern filtering via `--list-models <pattern>`. Tests added.

---

# URGENT — 2026-02-09 (previous)

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

## Committed Binaries

### ~~`pi`, `rpc.test`~~ — ✅ FIXED: `git rm --cached`, `.gitignore` updated (`./pi` → `pi`, added `*.test`)

---

## Correctness

### ~~`pkg/core/agentsession.go:488-489`~~ — ✅ INVALID: `ResumeSession` doesn't exist in agentsession.go. The session resume logic is in `session.go:BuildSessionContext()` which already uses `e.RawMessage` correctly.

### ~~`pkg/modes/rpc/server.go:124-145`~~ — ✅ FIXED: `CmdSteer` now calls `Agent.Steer()`, `CmdFollowUp` now calls `Agent.FollowUp()` with proper `AgentMessage` construction.

---

## Concurrency

### ~~`pkg/core/session.go:370-392`~~ — ✅ FIXED: Reader methods now protected by `RWMutex`
Changed `sync.Mutex` → `sync.RWMutex`. All 16 read accessors now hold `RLock()`. Write methods (`Append*`, `Branch*`, etc.) use full `Lock()`.
