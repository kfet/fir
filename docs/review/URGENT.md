# URGENT — 2026-02-23

## Active Issues

_(none — cycle 126: build clean, all packages pass, no urgent issues)_

---

## Recently Fixed ✅

- ~~`pkg/tui/components/input.go` — `handleBackspace` pushes undo on every keystroke, `UndoStack.Push` leaks evicted strings~~ — ✅ FIXED 2026-02-23
- ~~`cmd/fir/app.go:33` — `//go:embed CHANGELOG.md` build break~~ — ✅ FIXED (cycle 58 / rebase ce07547): embed moved to `cmd/fir/changelog_init.go`; `GetChangelogEntries()` prefers embedded, falls back to file.
- ~~`pkg/core/compaction/runner_test.go:110` — `TestDefaultRunner_GetStats_WithMessages` fails: `TokensBefore` always 0~~ — ✅ FIXED (2026-02-19)
- ~~`pkg/core/compaction/runner_test.go:99` — `ai.Message{Role: ...}` unknown struct field~~ — ✅ FIXED
- ~~`pkg/modes/acp/acp_test.go:586,610` — `chunk.Content` used as string (type `ContentBlock`)~~ — ✅ FIXED 2026-02-18
- ~~`pkg/modes/acp/acp.go:929-937` — Extension commands dispatched via `Prompt()` instead of `ExecuteCommand()`~~ — ✅ FIXED 2026-02-18
- ~~`pkg/modes/acp/acp.go:795` — `RunCompaction()` called with wrong signature (no args)~~ — ✅ FIXED 2026-02-18
- ~~`pkg/core/tools/read.go:110` — `filepath.Ext` regression for dotfiles~~ — ✅ FIXED 2026-02-19
- ~~`pkg/ai/oauth/callback_server.go:25` — Reflected XSS in OAuth error page~~ — ✅ FIXED
- ~~`pkg/core/agentsession.go:636` — `WrapToolsWithHooks` never called~~ — ✅ FIXED
- ~~`pkg/extensions/notify/notify.go:30` — Direct `os.Stdout` write~~ — ✅ FIXED
- ~~`pkg/core/authstorage.go:401-446` — Deadlock in `refreshOAuthToken`~~ — ✅ FIXED
- ~~`pkg/modes/acp/acp.go:381` — Non-zero exit code not returned as error~~ — ✅ FIXED
- ~~`pkg/modes/acp/acp.go:82` — No session cleanup on exit~~ — ✅ FIXED
- ~~`pkg/modes/interactive/mode.go:1806` — `proc *exec.Cmd` data race in `performShare`~~ — ✅ FIXED (cycle 96): `atomic.Pointer[exec.Cmd]`
- ~~`pkg/core/agentsession.go:686` — `ScopedModelsRef()`/`SetScopedModels()` no mutex guards~~ — ✅ FIXED (ce07547): RLock/Lock added
- ~~`pkg/modes/acp/terminal.go` — `pendingBashTerminals` leak on abort~~ — ✅ FIXED (ce07547): `CleanupPendingBashTerminals` + per-path deletes
