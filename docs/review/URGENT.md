# URGENT — 2026-02-26

## Active Issues

### `pkg/mcp/client_test.go:384-400` — `TestManager_ProgressNotification` is broken + data race
**Commits:** unstaged work-in-progress on top of `3b74aae`
**Severity:** BLOCKER — data race + test broken

The test closes the `updates` channel and then immediately tries to read from it a second time via `select case got = <-updates:`.  A closed empty channel yields zero value immediately, so `got = ""` and the assertion always fails.  Additionally, closing the channel while the SDK's notification-dispatch goroutine may still be sending to it triggers a race detected by `go test -race`.

The comment in the code even says "do not close the channel to avoid a race" — but the channel *is* closed two lines earlier. This is an incomplete edit where the old close+range pattern was not removed when the new select-timeout pattern was added.

**Files:** `pkg/mcp/client_test.go:383-400`

**Suggested fix:**
Remove the `close(updates)` and `for msg := range updates` block entirely, keeping only the select-with-timeout approach:
```go
// Do NOT close updates — SDK notification dispatch goroutine may still
// be sending to it. The notification is delivered before the tool result
// (MCP message ordering), so it is buffered by the time Execute returns.
var got string
select {
case got = <-updates:
case <-time.After(2 * time.Second):
    t.Fatal("timeout waiting for progress notification")
}
assert.Equal(t, "halfway there", got)
```
Also add a brief `time.Sleep` or synchronization after `Execute` if the in-memory transport delivers notifications asynchronously.

---

## Recently Fixed ✅

### `pkg/mcp/client.go:commandTransport` — subprocess env drops `os.Environ()` ✅ FIXED ef4f139
- When cfg.Env was non-empty, `cmd.Env` became a non-nil slice with only the listed vars,
  stripping PATH, HOME, and the entire parent environment from the subprocess.
- Fixed by adding `cmd.Env = os.Environ()` before the loop.
- Tests: `TestCommandTransport_EnvInheritsParent`, `TestCommandTransport_EmptyCommand`

---

## Previously Fixed ✅

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
