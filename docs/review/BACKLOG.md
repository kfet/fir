# Review Backlog — 2026-02-14

**Last reviewed:** Review cycle 3, 2026-02-14 11:40 PST
**Build status:** ✅ PASSING (`go vet ./...` clean, all tests pass)
**Test status:** ✅ ALL PASSING (21 packages, including new tmuxspinner + usage)

**Files reviewed this cycle:** All unstaged `.go` diffs (~160 files), focus on recently modified:
- `pkg/extensions/usage/{usage,auth,client}.go` + tests (new extension)
- `pkg/extension/integration.go` + test (reload mechanism)
- `pkg/extension/runner.go` (Reset method)
- `pkg/core/keybindings.go` + test (ActionSelectThinking, constants)
- `pkg/core/prompttemplates.go` + test (inferPromptSource, ConfigDirName → `.tau`)
- `pkg/modes/interactive/mode.go` + test (extension reload, prompt autocomplete, session info)
- `pkg/modes/rpc/server.go` + test (GetCommands returns prompts + skills)
- `pkg/ai/providers/google_gemini_cli.go` + test (formatting, user-agent rename)
- `pkg/modes/interactive/components/custom_editor.go` (keybinding constants)
- `cmd/tau/app.go` (module rename)

---

## Simplification

_(no new issues found)_

## Security

_(no new issues found)_

## Test Coverage

_(no new gaps found — all new code has corresponding tests)_

## Correctness

_(no new issues found)_

## Hygiene

_(no new issues found)_

---

## Previously Resolved (all ✅)

- `resolveTools` unknown tool warning — FIXED
- `UnknownFlags` never populated — FIXED
- `SendMessage`/`SendUserMessage` stubbed — FIXED
- `SetActiveTools` no-op — FIXED
- Extension test coverage — FIXED
- Notify/sandbox test coverage — FIXED
- Sandbox command checking docs — FIXED
- `GetSessionList` export scope — FIXED
