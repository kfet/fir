# URGENT — 2026-02-13

## Active Issues

(none)

---

## Previously Fixed This Cycle

### ~~`pkg/modes/interactive/mode.go:829-857` — Race on `isBashMode`/`bashComponent`~~ — ✅ FIXED
Protected writes in `handleBashCommand` with `m.mu.Lock()`/`Unlock()`. Protected reads in `OnEscape` and `OnChange`. Added `IsBashMode()` accessor; updated all test assertions to use it. `go test -race` passes.

---

## Previously Fixed This Cycle

### ~~`pkg/tui/components/editor.go:587-607` — `e.mu undefined`~~ — ✅ FIXED
Build break resolved: `mu sync.Mutex` field re-added to Editor struct, `sync` import restored. All fire helpers work correctly.

### ~~`pkg/ai/providers/google_gemini_cli.go:376` — `randomID()` weak entropy~~ — ✅ FIXED
Now uses `crypto/rand.Read(b)` instead of `time.Now().UnixNano()`.

### ~~`pkg/ai/oauth/google_gemini_cli.go:490` — `pollGeminiOperation` infinite loop~~ — ✅ FIXED
Added `maxPollAttempts = 60` (5 min max).

### ~~`pkg/tui/tui.go` — Multiple TUI race conditions~~ — ✅ FIXED
All three race types resolved.

### ~~`pkg/tui/components/editor.go` — Editor internal race~~ — ✅ FIXED
Added `sync.Mutex` to Editor. Render() and HandleInput() acquire lock. Callbacks fire via fireOnChange/fireOnSubmit which unlock before calling to prevent deadlock. All 18 callback sites replaced. Race detector passes.

### ~~`pkg/modes/interactive/mode_test.go` — Session race in test setup~~ — ✅ FIXED
Session now set BEFORE `ui.Start()` via unified `newTestModeInternal()`.

---

## Security (deferred — needs design discussion)

### `pkg/ai/providers/anthropic.go:22-28` — Claude Code impersonation
Matching upstream behavior; product decision.

### `pkg/ai/providers/anthropic.go:283-286` — Thinking config smuggled through HTTP headers
Requires cross-cutting refactor.
