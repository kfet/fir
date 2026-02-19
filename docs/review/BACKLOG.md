# Review Backlog — 2026-02-18

**Last reviewed:** Review cycle 8, 2026-02-18 11:14 PST
**Build status:** ✅ PASSING (`go vet ./...` clean, all tests pass)
**Test status:** ✅ ALL PASSING (22 packages)

**Files reviewed this cycle:**
- `cmd/tau/app_test.go` — `TestCheckModelAvailable_NilModel_ACP` added ✅
- `pkg/core/tools/edit_test.go` — 6 tests for `NewEditToolWithReadWriter` added ✅
- `pkg/core/tools/write_test.go` — 4 tests for `NewWriteToolWithWriter` added ✅
- `pkg/core/authstorage.go` — `WithLockAsync` → `WithLockFallible` (confirmed in working tree)

---

## Correctness

_(no open correctness issues)_

## Simplification

_(none)_

## Security

_(no new issues found)_

## Test Coverage

_(no open test coverage items)_

## Hygiene

_(no new issues found)_

---

## Previously Resolved (all ✅)

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
- `SetActiveTools` no-op — FIXED
- Extension test coverage — FIXED
- Notify/sandbox test coverage — FIXED
- Sandbox command checking docs — FIXED
- `GetSessionList` export scope — FIXED
