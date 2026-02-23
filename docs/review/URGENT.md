# URGENT — 2026-02-21

## Active Issues

_(none — cycle 70: session selector newline-fix + tmuxspinner tests merged; build clean, all 22 packages pass)_

---

## Recently Fixed ✅

- ~~`pkg/modes/interactive/components/session_selector.go` — Visible-window centering wrong when count < maxVisible~~ — ✅ FIXED 2026-02-21 (refactored to min/max builtins; scroll regression tests added)
- ~~`pkg/core/compaction/runner_test.go:110` — `TestDefaultRunner_GetStats_WithMessages` fails: `TokensBefore` always 0~~ — ✅ FIXED (2026-02-19)
- ~~`pkg/core/compaction/runner_test.go:99` — `ai.Message{Role: ...}` unknown struct field~~ — ✅ FIXED (agent corrected to `ai.NewAssistantMsg()`)
- ~~`pkg/modes/acp/acp_test.go:586,610` — `chunk.Content` used as string (type `ContentBlock`)~~ — ✅ FIXED 2026-02-18
- ~~`pkg/modes/acp/acp.go:929-937` — Extension commands dispatched via `Prompt()` instead of `ExecuteCommand()`~~ — ✅ FIXED 2026-02-18
- ~~`pkg/modes/acp/acp.go:795` — `RunCompaction()` called with wrong signature (no args)~~ — ✅ FIXED 2026-02-18
- ~~`pkg/core/tools/read.go:110` — `filepath.Ext` regression for dotfiles~~ — ✅ FIXED 2026-02-19
- ~~`pkg/ai/oauth/callback_server.go:25` — Reflected XSS in OAuth error page~~ — ✅ FIXED
- ~~`pkg/core/agentsession.go:636` — `WrapToolsWithHooks` never called~~ — ✅ FIXED
- ~~`pkg/extensions/notify/notify.go:30` — Direct `os.Stdout` write~~ — ✅ FIXED
- ~~`pkg/core/authstorage.go:401-446` — Deadlock in `refreshOAuthToken`~~ — ✅ FIXED
- ~~`pkg/core/compaction/runner_test.go:101` — Test failure + real auth.json mutation~~ — ✅ FIXED
- ~~`pkg/modes/acp/acp.go:381` — Non-zero exit code not returned as error~~ — ✅ FIXED
- ~~`pkg/modes/acp/acp.go:82` — No session cleanup on exit~~ — ✅ FIXED
