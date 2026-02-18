# Review Backlog — 2026-02-17

**Last reviewed:** Review cycle 11, 2026-02-17 18:40 PST
**Build status:** ✅ BUILD PASSING (`go build ./...`, `go vet ./...` clean)
**Test status:** ✅ ALL PASSING (21 packages)

**Files reviewed this cycle:**

Unstaged (in-progress):
- `pkg/core/settings_test.go` — `failingSettingsStorage` mock + DrainErrors error path tests added

---

## Security

_(no open items)_

## Simplification

_(no new issues)_

## Test Coverage

_(no open items — DrainErrors write-error path now tested with mock storage)_

## Correctness

_(no open items)_

## Hygiene

_(no new issues)_

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
- Google OAuth User-Agent impersonation (antigravity + gemini-cli + github_copilot) — NOTE comments added ✅
- `bash.go` cmd.Cancel process-group kill — correct implementation, no issues ✅
- `tmuxspinner_test.go` ClearRegistry without cleanup — `defer ClearRegistry()` added ✅
- `settings.go` write-error suppression — `SettingsStorage.WithLock` now returns error; recorded via `recordError` ✅
- `settings_test.go` DrainErrors error path untested — `failingSettingsStorage` mock + test added ✅
