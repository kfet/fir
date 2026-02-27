# Review Backlog — 2026-02-27

**Last reviewed:** MCP advanced work, cycle 49, 2026-02-27 ~09:10 PST
**Build status:** ✅ all 23 tested packages pass with -race. 0 URGENT, 0 open BACKLOG.
**Files reviewed this cycle (staged):** `pkg/mcp/reload_test.go`, `cmd/fir/CHANGELOG.md`, `.fir/skills/overseer/SKILL.md`

---

## Open Issues

*(none)*

---

## Resolved This Cycle ✅

- **✅ FIXED (staged)** `pkg/mcp/reload_test.go:215` — truncated doc comment on
  `TestManager_Reload_Concurrent` is now restored. The full first line
  `// TestManager_Reload_Concurrent verifies that concurrent Reload calls do not` is present.
  A blank line is also now present between the two test functions.

- **✅ FIXED (staged)** `cmd/fir/CHANGELOG.md` — MCP fixes documented under `## [Unreleased] ### Fixed`:
  concurrent Reload serialisation, post-startup disconnect detection, `m.subscribed` memory leak,
  and WatchConfig stop semantics.

---

## Previously Resolved ✅

- **✅ FIXED (working tree → staged)** `pkg/mcp/reload_test.go` — `TestManager_Reload_Concurrent_ConfigChange` added; alternates between `cmd-a` and `cmd-b` configs to exercise concurrent stop+restart path.
- **✅ FIXED (working tree → staged)** `pkg/mcp/client.go:Reload` — `reloadMu sync.Mutex` added; concurrent Reload calls serialized.
- **✅ FIXED (working tree → staged)** `pkg/mcp/client.go:Reload` — `delete(m.subscribed, name)` added.
- **✅ FIXED (working tree → staged)** `pkg/mcp/client.go:Manager.Status` — `session.Wait()` goroutine detects disconnects; `TestManager_Status_AfterServerDisconnect` added.
- **✅ FIXED (working tree → staged)** `pkg/mcp/config_watch.go:WatchConfig` — best-effort stop documented.
- **✅ FIXED `e1c8c2f`** `pkg/mcp/client.go:ToolListChangedHandler` — subscription loop added; new post-startup resources subscribed to.
- **✅ FIXED `12734c4`** `pkg/mcp/client.go` — `ToolListChangedHandler` race fixed; stale-session guard added.
- **✅ FIXED `12734c4`** `pkg/mcp/client.go` — `configsEqual` deduplicated.
- **✅ FIXED `12734c4`** `pkg/mcp/completions.go` — doc/error mismatch fixed.
- **✅ FIXED `12734c4`** `pkg/mcp/config_watch.go` + `config_watch_test.go` — `WatchConfig` and `WatchAndReload` with full tests.
- **✅ FIXED b76bd20** `pkg/mcp/client.go` — `OnResourceUpdated` + startup subscription loop.
- **✅ FIXED 92e6174** `pkg/mcp/tool_adapter.go:87` — MIME guard added.
- **✅ FIXED f6c7931** `pkg/core/agentsession.go:runAutoCompaction` — checks `PendingToolCalls`.
- **✅ FIXED 7e102e0** `pkg/mcp/client_test.go` — duplicate transport test removed.
- **✅ FIXED 19f0c28** `pkg/modes/acp/acp_mcp_e2e_test.go` — assertion tightened.
