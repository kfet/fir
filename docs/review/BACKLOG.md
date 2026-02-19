# Review Backlog — 2026-02-19

**Last reviewed:** Review cycle 46, 2026-02-19 10:28 PST → updated 2026-02-19 (fixer)
**Build status:** ✅ BUILD PASSING (`go vet ./...` clean)
**Test status:** ✅ ALL PASSING (22 packages)
**Work tracker:** All phases complete (0→13). No active porting work.

**Files reviewed this cycle:**
- `pkg/extension/integration.go` — double-wrap, new findings
- `pkg/extension/integration_test.go` — SendMessage/SendUserMessage delivery routing tests added ✅
- `pkg/extension/runner.go` — no new issues
- `pkg/core/compaction/compaction.go` — no new issues
- `pkg/core/compaction/runner.go` — no new issues
- `pkg/core/compaction/compaction_test.go` — thorough coverage, no gaps
- `pkg/core/compaction/runner_test.go` — good coverage
- `pkg/core/agentsession.go` — IsError never applied, new finding
- `pkg/modes/interactive/mode.go` — no new issues

---

## Simplification

_(no open items)_

## Security

_(no open items)_

## Test Coverage

_(no open items)_

## Correctness

_(no open items)_

## Hygiene

_(no open items)_

---

## Previously Resolved (all ✅)

- `resolveTools` unknown tool warning — FIXED
- `UnknownFlags` never populated — FIXED
- `SendMessage`/`SendUserMessage` stubbed — FIXED
- `SendMessage opts.DeliverAs/TriggerTurn ignored` — FIXED (2026-02-19) via runner.go routing
- `SendUserMessage opts.DeliverAs ignored` — FIXED (2026-02-19)
- `SendMessage/SendUserMessage delivery routing untested` — FIXED (2026-02-19) via integration_test.go
- `SetActiveTools` no-op — FIXED
- `TurnStartEvent.TurnIndex/Timestamp always zero` — FIXED (2026-02-19) via atomic counter
- `TurnEndEvent.TurnIndex always zero` — FIXED (2026-02-19) via currentTurnIdx tracking
- `ShouldCompact contextWindow==0 triggers compaction` — FIXED (2026-02-19) via early return
- Extension test coverage — FIXED
- Notify/sandbox test coverage — FIXED
- Sandbox command checking docs — FIXED
- `GetSessionList` export scope — FIXED
- Google OAuth User-Agent impersonation (antigravity + gemini-cli + github_copilot) — NOTE comments added ✅
- `bash.go` cmd.Cancel process-group kill — correct implementation, no issues ✅
- `tmuxspinner_test.go` ClearRegistry without cleanup — `defer ClearRegistry()` added ✅
- `settings.go` write-error suppression — `SettingsStorage.WithLock` now returns error; recorded via `recordError` ✅
- `settings_test.go` DrainErrors error path untested — `failingSettingsStorage` mock + test added ✅
- `models_generated.go:1` — Missing `// Generated at:` timestamp — ✅ FIXED (2026-02-18)
- `models_generated.go` — 161 noisy float literals rounded to 8 decimal places; `formatFloat` generator fixed ✅
- `pkg/modes/rpc/server.go:168` — `AutoCompactionEnabled: true` hardcoded — ✅ FIXED (2026-02-18)
- `pkg/core/compaction/runner_test.go` — Missing `IsEnabled()` test — ✅ FIXED (2026-02-18)
- `pkg/modes/rpc/server.go` — `Run()` exits before prompt goroutine completes — ✅ FIXED (2026-02-18) via `sync.WaitGroup`
- `pkg/ai/oauth/github_copilot.go` — `pollForGitHubAccessToken` etc. untested — ✅ FIXED (2026-02-18)
- `pkg/modes/rpc/server_html_export.go` — HTML export untested — ✅ FIXED (2026-02-18)
- `pkg/modes/interactive/components/tree_selector.go:716-720` — `math.Max`/`math.Min` — ✅ FIXED (2026-02-18)
- `pkg/core/tools/imageresize.go:148` — last-resort fallback path untested — ✅ FIXED (2026-02-18)
- `pkg/core/tools/imageresize_test.go:145-155` — Custom helpers duplicating stdlib — ✅ FIXED (2026-02-18)
- `pkg/modes/rpc/server.go:154` — `CmdAbort` called `Close()` instead of `Abort()` — ✅ FIXED (2026-02-18)
- `pkg/extension/runner.go:EmitInput` — Panics silently discarded — ✅ FIXED (2026-02-19)
- `pkg/core/compaction/runner.go:GetStats` — No unit tests — ✅ FIXED (2026-02-19)
- `pkg/modes/interactive/mode.go:compactionFormatTokens/compactionLoaderLabel` — No unit tests — ✅ FIXED (2026-02-19)
- `pkg/core/compaction/runner_test.go:99` — `ai.Message{Role:...}` unknown field — ✅ FIXED (2026-02-19)
- `pkg/core/compaction/runner_test.go:110` — TokensBefore always 0 — ✅ FIXED (2026-02-19)
- `pkg/extension/integration.go:173-189` — Double-wrapping of tools (hooks firing twice) — ✅ FIXED (2026-02-19): `addExtensionTools` now only wraps new extension tools, not pre-existing tools
- `pkg/extension/integration_test.go` — No hook-fires-exactly-once test — ✅ FIXED (2026-02-19): `TestHookFiresExactlyOnceWithPreExistingTools` added
- `pkg/core/agentsession.go:wrapTool` — `ToolResultModification.IsError` dead code — ✅ FIXED (2026-02-19): added `IsError bool` to `AgentToolResult`, loop uses it, hook receives correct isError, mod.IsError applied
- `pkg/extension/integration.go:bridgeSessionEvents` — `turnCounter int64` needlessly wide — ✅ FIXED (2026-02-19): changed to `int`, dropped `int()` cast
