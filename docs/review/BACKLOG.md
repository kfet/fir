# Review Backlog — 2026-02-27

**Last reviewed:** 2026-02-27 ~12:30 PST
**Build status:** ✅ all packages pass. 0 URGENT, 0 open BACKLOG.
**Files reviewed this cycle:** pkg/agent/agent.go, pkg/agent/agent_test.go, pkg/core/agentsession.go, pkg/core/agentsession_test.go, pkg/core/slashcmds.go, pkg/modes/interactive/mode.go, pkg/modes/interactive/mode_test.go (staged changes for /queue and /dequeue slash commands)

---

## Open Issues

*(none)*

---

## Recently Resolved ✅

- **✅ FIXED** `pkg/modes/interactive/mode.go:1241` — Byte-level UTF-8 truncation in `handleQueueCommand` preview. Changed `preview[:77]` to `string([]rune(preview)[:77])`.
- **✅ FIXED** `pkg/core/agentsession.go:452` — `RemoveFollowUp` returned `("", true)` for non-string content (e.g. image blocks), which could silently clear the editor. Now returns `("", false)`. Test `TestAgentSession_RemoveFollowUp_NonStringContent` added.
- **✅ FIXED** `pkg/modes/interactive/mode_test.go` — Missing nil-session test for `handleDequeueCommand` with a numeric arg. `TestHandleDequeueCommand_NilSession` added covering both the no-arg and numeric-arg paths.
- **✅ FIXED (staged)** `pkg/mcp/reload_test.go:215` — truncated doc comment on `TestManager_Reload_Concurrent` restored.
- **✅ FIXED (staged)** `cmd/fir/CHANGELOG.md` — MCP fixes documented under `## [Unreleased] ### Fixed`.

---

## Previously Resolved ✅

- **✅ FIXED (working tree → staged)** `pkg/mcp/reload_test.go` — `TestManager_Reload_Concurrent_ConfigChange` added.
- **✅ FIXED (working tree → staged)** `pkg/mcp/client.go:Reload` — `reloadMu sync.Mutex` added; concurrent Reload calls serialized.
- **✅ FIXED (working tree → staged)** `pkg/mcp/client.go:Reload` — `delete(m.subscribed, name)` added.
- **✅ FIXED (working tree → staged)** `pkg/mcp/client.go:Manager.Status` — `session.Wait()` goroutine detects disconnects.
- **✅ FIXED (working tree → staged)** `pkg/mcp/config_watch.go:WatchConfig` — best-effort stop documented.
- **✅ FIXED `e1c8c2f`** `pkg/mcp/client.go:ToolListChangedHandler` — subscription loop added.
- **✅ FIXED `12734c4`** `pkg/mcp/client.go` — `ToolListChangedHandler` race fixed; stale-session guard added.
- **✅ FIXED `12734c4`** `pkg/mcp/client.go` — `configsEqual` deduplicated.
- **✅ FIXED `12734c4`** `pkg/mcp/completions.go` — doc/error mismatch fixed.
- **✅ FIXED `12734c4`** `pkg/mcp/config_watch.go` + `config_watch_test.go` — `WatchConfig` and `WatchAndReload` with full tests.
- **✅ FIXED b76bd20** `pkg/mcp/client.go` — `OnResourceUpdated` + startup subscription loop.
- **✅ FIXED 92e6174** `pkg/mcp/tool_adapter.go:87` — MIME guard added.
- **✅ FIXED f6c7931** `pkg/core/agentsession.go:runAutoCompaction` — checks `PendingToolCalls`.
- **✅ FIXED 7e102e0** `pkg/mcp/client_test.go` — duplicate transport test removed.
- **✅ FIXED 19f0c28** `pkg/modes/acp/acp_mcp_e2e_test.go` — assertion tightened.
