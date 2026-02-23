# Review Backlog — 2026-02-22

**Last reviewed:** Review cycle 72, 2026-02-22 18:55 PST
**Race detector:** ✅ CLEAN (`go test -race ./...` — all 22 packages pass, no races)
**Build status:** ✅ BUILD PASSING (`go vet ./...` clean)
**Test status:** ✅ ALL PASSING (22 packages)
**Work tracker:** All phases 0–14 complete.
**Files reviewed this cycle:** No new files (cycle 70 fix for `SortRelevance`/`FilterAndSortSessions` confirmed fixed by another agent)

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

- `pkg/modes/interactive/components/session_selector.go:applyFilter` — `SortRelevance` case missing; `FilterAndSortSessions` never called; all modes now use the rich search engine (fuzzy/phrase/regex) — FIXED 2026-02-22
- `pkg/extensions/tmuxspinner/tmuxspinner_test.go:87-92` — `TestNoopWhenNotInTmux` removed unnecessary `ClearRegistry()` + re-`Register()` calls; factory evaluates `isTmux()` at load time so `testSetup` override suffices — FIXED 2026-02-21
- `pkg/modes/interactive/components/session_selector_search.go:161` — Dead `if inQuote { flush(TokenPhrase) }` branch (inQuote can only be false at that point) — FIXED 2026-02-21: simplified to `flush(TokenFuzzy)` unconditionally
- `pkg/core/agentsession.go:255-261` — `emit` RLock held during callbacks (deadlock risk) — FIXED 2026-02-20
- `pkg/modes/print/print.go:87-91` — `os.Exit(1)` inside library function — FIXED 2026-02-20
- `pkg/core/session.go:687` — `panic` in `CreateBranchedSession` — FIXED 2026-02-20
- `pkg/core/modelregistry.go:779-815` — Four `panic` calls in `applyProviderConfig` — FIXED 2026-02-20
- `pkg/modes/print/print_test.go` — `Run` function had zero coverage — FIXED 2026-02-20
- `pkg/ai/providers/codex_websocket.go:138-169` — `wsInflight` coalescing path untested — FIXED 2026-02-20
- `pkg/core/sdk.go:81-87` — Unnecessary auth-path complexity — FIXED 2026-02-20
- `pkg/modes/interactive/components/session_selector.go` — Visible-window centering wrong when count < maxVisible; refactored to min/max builtins; scroll regression tests added — FIXED 2026-02-21
- `pkg/extensions/claudeusage/client.go:80-84` — `progressBar` manual clamping → `max`/`min` builtins — ✅ FIXED 2026-02-18
- `pkg/extensions/claudeusage/client.go:44` — `http.DefaultClient` → `httpClient` with 10s timeout — ✅ FIXED 2026-02-18
- `pkg/modes/acp/acp.go:ResumeSession` — duplicate session/resume leaked resources — ✅ FIXED (closes old session before creating new one; test added)
- `pkg/core/tools/read.go:148` — `getExtension` reimplements `filepath.Ext` — ✅ FIXED (replaced with `filepath.Ext`, function removed)
- `EditReadFn` / `ReadFileFn` duplicate type — ✅ FIXED (dropped `EditReadFn`, `NewEditToolWithReadWriter` now uses `ReadFileFn`)
- `NewEditToolWithReadWriter` duplicated edit algorithm — ✅ FIXED (extracted `applyEditLogic` helper; 6 tests added)
- `WithLockAsync` misleading name — ✅ FIXED (renamed to `WithLockFallible`)
- `NewBashToolWithPrefix` no test — ✅ FIXED (3 tests: prefix prepended, empty passthrough, non-string fallthrough)
- `NewReadToolWithReader` no test — ✅ FIXED (5 tests: delegate, image fallback, offset/limit, empty path, readFn error)
- `NewWriteToolWithWriter` no test — ✅ FIXED (4 tests: absolute path, empty path, ctx cancel, bytes message)
- `NewEditToolWithReadWriter` no test — ✅ FIXED (6 tests: round-trip, not-found, multiple occurrences, empty path, cancel, readFn error)
- `ModeACP` not tested in `checkModelAvailable` — ✅ FIXED (`TestCheckModelAvailable_NilModel_ACP` added)
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
- `pkg/ai/oauth/github_copilot.go` — `pollForGitHubAccessToken`, `refreshGitHubCopilotToken`, `enableAllCopilotModels`/`enableCopilotModel` untested — ✅ FIXED (2026-02-18) via `github_copilot_test.go`
- `pkg/modes/rpc/server_html_export.go` — `writeConversationHTML`/`extractHTMLMessageText` no unit tests — ✅ FIXED (2026-02-18) via `server_html_export_test.go`
- `pkg/modes/interactive/components/tree_selector.go:716-720` — `math.Max`/`math.Min` float64 for integer clamping — ✅ FIXED (2026-02-18) via `max`/`min` builtins
- `pkg/core/tools/imageresize.go:148` — last-resort fallback path untested — ✅ FIXED (2026-02-18) via `TestResizeImage_LastResortFallback`
- `pkg/core/tools/imageresize_test.go:145-155` — Custom `contains`/`containsAt` helpers duplicating `strings.Contains` — ✅ FIXED (2026-02-18)
- `pkg/modes/rpc/server.go:154` — `CmdAbort` called `Close()` instead of `Abort()` — ✅ FIXED (2026-02-18)
- `pkg/extension/runner.go:EmitInput` — Panics silently discarded — ✅ FIXED (2026-02-19)
- `pkg/core/compaction/runner.go:GetStats` — No unit tests — ✅ FIXED (2026-02-19)
- `pkg/modes/interactive/mode.go:compactionFormatTokens/compactionLoaderLabel` — No unit tests — ✅ FIXED (2026-02-19)
- `pkg/core/compaction/runner_test.go:99` — `ai.Message{Role:...}` unknown field — ✅ FIXED (2026-02-19)
- `pkg/core/compaction/runner_test.go:110` — TokensBefore always 0 — ✅ FIXED (2026-02-19)
- `pkg/extension/integration.go:173-189` — Double-wrapping of tools (hooks firing twice) — ✅ FIXED (2026-02-19)
- `pkg/extension/integration_test.go` — No hook-fires-exactly-once test — ✅ FIXED (2026-02-19)
- `pkg/core/agentsession.go:wrapTool` — `ToolResultModification.IsError` dead code — ✅ FIXED (2026-02-19)
- `pkg/extension/integration.go:bridgeSessionEvents` — `turnCounter int64` needlessly wide — ✅ FIXED (2026-02-19)
- `pkg/modes/acp/acp.go:929-937` — Extension commands dispatched via `Prompt()` instead of `ExecuteCommand()` — ✅ FIXED 2026-02-18
- `pkg/modes/acp/acp_test.go:586,610` — `chunk.Content` used as string (type `ContentBlock`) — ✅ FIXED 2026-02-18
- `pkg/tui/components/editor.go:713,740` — `math.Max` used instead of builtin `max()` — ✅ FIXED 2026-02-18
- `pkg/modes/acp/acp.go:1162` — `parseInt` reimplemented instead of using `strconv.Atoi` — ✅ FIXED 2026-02-18
- ACP slash commands `/session`, `/name`, `handleResumeArg` untested — ✅ FIXED 2026-02-18
- `pkg/ai/providers/codex_websocket.go` — mapCodexEvent duplication, TOCTOU race — FIXED 2026-02-20
- `pkg/ai/providers/google_gemini_cli.go` — mergeHeaders mutated in-place — FIXED 2026-02-20
- `pkg/ai/models_test.go` — Missing gemini-3.1-pro test cases — FIXED 2026-02-20
