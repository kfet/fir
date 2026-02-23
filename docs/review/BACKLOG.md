# Review Backlog — 2026-02-22

**Last reviewed:** Review cycle 110, 2026-02-22 23:10 PST
**Build status:** ✅ BUILD PASSING (`go vet ./...` clean, `go test ./...` all 22 packages pass)
**Files reviewed this cycle:**
- `pkg/core/modelresolver_test.go` — 8 new `TestResolveCliModel_*` tests + `TestParseModelPatternStrict_InvalidThinkingLevel_NoFallback` added. Backlog items from cycle 109 resolved.
- `pkg/modes/interactive/components/tool_execution.go` — new `strArgChecked` helper distinguishes absent vs wrong-type args; all display functions updated.
- `pkg/ai/models_generated.go` — regenerated with corrected MaxTokens and pricing. Generated file, no action needed.
- `cmd/fir/app.go` — upstream hash bump only (no behavior changes from what was reviewed in cycle 109).
- `pkg/tui/terminal.go`, `pkg/modes/interactive/mode.go` — upstream hash bumps only, no code changes.
**Open items: 0** (all backlog resolved by current agent work)

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

- `pkg/core/modelresolver.go:223` — `ResolveCliModel` has no unit tests — ✅ FIXED cycle 110 (8 tests added: Empty, ExactID, ProviderSlashModel, ExplicitProvider, UnknownProvider, ModelNotFound, OpenRouterSlashID, ThinkingLevel)
- `pkg/core/modelresolver.go:175` — `parseModelPatternStrict` (allowFallback=false) untested — ✅ FIXED cycle 110 (`TestParseModelPatternStrict_InvalidThinkingLevel_NoFallback`)
- `pkg/modes/acp/conn.go:67-75` — marshal/unmarshal errors silently swallowed in `initialize` → null ACP handshake response — ✅ FIXED 2026-02-22
- `pkg/modes/interactive/mode.go` — `cycleModel`/`handleFork`/`handleForkByNumber` no tests — ✅ FIXED 2026-02-22
- `pkg/core/export.go` — `ExportToHTML` duplicate create/write/close branches — ✅ FIXED (cycle 97)
- `pkg/modes/interactive/mode.go:1156` — `append(queued, current)` slice capacity footgun — ✅ FIXED (cycle 97): `make+copy`
- `pkg/modes/interactive/mode.go:1806` — `proc` data race in `performShare` — ✅ FIXED (cycle 96): `atomic.Pointer[exec.Cmd]`
- `pkg/core/changelog.go:cmpInt` — reimplements `cmp.Compare` — ✅ FIXED (ce07547)
- `pkg/modes/interactive/mode.go` — `handleDequeue`/`handleExternalEditor`/`handleClipboardImagePaste`/`performShare` untested — ✅ FIXED (ce07547)
- `pkg/modes/interactive/mode.go` — `showScopedModelsSelector` stub — ✅ FIXED (ce07547)
- `pkg/modes/acp/terminal.go` — `pendingBashTerminals` leak on cancel/resume/shutdown — ✅ FIXED (ce07547)
- `pkg/core/agentsession.go` — `SetScopedModels`/`ScopedModelsRef` no mutex — ✅ FIXED (ce07547)
- `pkg/core/export.go` — `ExportToHTML` no unit test — ✅ FIXED (ce07547)
- `pkg/modes/interactive/components/tree_selector.go` — `SetOnLabelEdit`/`SetInitialSelection` no tests — ✅ FIXED (ce07547)
- `pkg/modes/interactive/components/session_selector.go` — `SortRelevance` case missing; `FilterAndSortSessions` never called — ✅ FIXED 2026-02-22
- `pkg/extensions/tmuxspinner/tmuxspinner_test.go:87-92` — unnecessary `ClearRegistry` calls — ✅ FIXED 2026-02-21
- `pkg/modes/interactive/components/session_selector_search.go:161` — Dead `if inQuote { flush(TokenPhrase) }` branch — ✅ FIXED 2026-02-21
- `pkg/core/agentsession.go:255-261` — `emit` RLock held during callbacks (deadlock risk) — ✅ FIXED 2026-02-20
- `pkg/modes/print/print.go:87-91` — `os.Exit(1)` inside library function — ✅ FIXED 2026-02-20
- `pkg/core/session.go:687` — `panic` in `CreateBranchedSession` — ✅ FIXED 2026-02-20
- `pkg/core/modelregistry.go:779-815` — Four `panic` calls in `applyProviderConfig` — ✅ FIXED 2026-02-20
- `pkg/modes/print/print_test.go` — `Run` function had zero coverage — ✅ FIXED 2026-02-20
- `pkg/ai/providers/codex_websocket.go:138-169` — `wsInflight` coalescing path untested — ✅ FIXED 2026-02-20
- `pkg/core/sdk.go:81-87` — Unnecessary auth-path complexity — ✅ FIXED 2026-02-20
- `pkg/extensions/claudeusage/client.go:80-84` — `progressBar` manual clamping → `max`/`min` builtins — ✅ FIXED 2026-02-18
- `pkg/extensions/claudeusage/client.go:44` — `http.DefaultClient` → `httpClient` with 10s timeout — ✅ FIXED 2026-02-18
- `pkg/modes/acp/acp.go:ResumeSession` — duplicate session/resume leaked resources — ✅ FIXED
- `pkg/core/tools/read.go:148` — `getExtension` reimplements `filepath.Ext` — ✅ FIXED
- `EditReadFn` / `ReadFileFn` duplicate type — ✅ FIXED
- `NewEditToolWithReadWriter` duplicated edit algorithm — ✅ FIXED
- `WithLockAsync` misleading name — ✅ FIXED
- `NewBashToolWithPrefix` no test — ✅ FIXED
- `NewReadToolWithReader` no test — ✅ FIXED
- `NewWriteToolWithWriter` no test — ✅ FIXED
- `NewEditToolWithReadWriter` no test — ✅ FIXED
- `ModeACP` not tested in `checkModelAvailable` — ✅ FIXED
- `resolveTools` unknown tool warning — ✅ FIXED
- `UnknownFlags` never populated — ✅ FIXED
- `SendMessage`/`SendUserMessage` stubbed — ✅ FIXED
- `TurnStartEvent.TurnIndex/Timestamp always zero` — ✅ FIXED (2026-02-19)
- `TurnEndEvent.TurnIndex always zero` — ✅ FIXED (2026-02-19)
- `ShouldCompact contextWindow==0 triggers compaction` — ✅ FIXED (2026-02-19)
- `pkg/modes/interactive/components/session_selector.go` — Visible-window centering wrong — ✅ FIXED 2026-02-21
- `pkg/extension/runner.go:EmitInput` — Panics silently discarded — ✅ FIXED 2026-02-19
- `pkg/core/compaction/runner.go:GetStats` — No unit tests — ✅ FIXED 2026-02-19
- `pkg/extension/integration.go:173-189` — Double-wrapping of tools — ✅ FIXED 2026-02-19
- `pkg/modes/interactive/mode.go:compactionFormatTokens/compactionLoaderLabel` — No unit tests — ✅ FIXED 2026-02-19
- `pkg/modes/rpc/server.go:168` — `AutoCompactionEnabled: true` hardcoded — ✅ FIXED 2026-02-18
- `pkg/modes/rpc/server.go` — `Run()` exits before prompt goroutine completes — ✅ FIXED 2026-02-18
- `pkg/ai/oauth/github_copilot.go` — Various functions untested — ✅ FIXED 2026-02-18
- `pkg/modes/rpc/server_html_export.go` — No unit tests — ✅ FIXED 2026-02-18 (moved to `pkg/core/export.go`)
- `pkg/modes/interactive/components/tree_selector.go:716-720` — `math.Max`/`math.Min` — ✅ FIXED 2026-02-18
- `pkg/modes/rpc/server.go:154` — `CmdAbort` called `Close()` instead of `Abort()` — ✅ FIXED 2026-02-18
- `pkg/core/tools/imageresize.go:148` — last-resort fallback path untested — ✅ FIXED 2026-02-18
- `pkg/tui/components/editor.go:713,740` — `math.Max` used instead of builtin `max()` — ✅ FIXED 2026-02-18
- `pkg/modes/acp/acp.go:1162` — `parseInt` reimplemented instead of `strconv.Atoi` — ✅ FIXED 2026-02-18
- `pkg/ai/providers/codex_websocket.go` — mapCodexEvent duplication, TOCTOU race — ✅ FIXED 2026-02-20
- `pkg/ai/providers/google_gemini_cli.go` — mergeHeaders mutated in-place — ✅ FIXED 2026-02-20
