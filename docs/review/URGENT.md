# URGENT — 2026-02-18

## Active Issues

_(none)_

---

## Previously Fixed

- ~~`pkg/ai/oauth/callback_server.go:25` — Reflected XSS in OAuth error page~~ — ✅ FIXED
- ~~`pkg/core/agentsession.go:636` — `WrapToolsWithHooks` never called~~ — ✅ FIXED
- ~~`pkg/extensions/notify/notify.go:30` — Direct `os.Stdout` write~~ — ✅ FIXED
- ~~`pkg/core/authstorage.go:401-446` — Deadlock in `refreshOAuthToken`~~ — ✅ FIXED
- ~~`pkg/core/compaction/runner_test.go:101` — Test failure + real auth.json mutation~~ — ✅ FIXED
- ~~`RunCompaction` signature change break (runner_test.go, agentsession_test.go, mode.go, rpc/server.go)~~ — ✅ FIXED 2026-02-18
- ~~`pkg/modes/rpc/server.go:193-194` — `CmdSetAutoCompaction` no-op~~ — ✅ FIXED 2026-02-18
- ~~`pkg/core/agentsession.go:488` — Overflow auto-compaction bypassed `Enabled` setting~~ — ✅ FIXED 2026-02-18
