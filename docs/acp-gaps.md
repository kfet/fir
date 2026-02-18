# ACP Mode Gaps vs TS Source

Discovered 2026-02-17. TS source: `pi-mono-acp/packages/coding-agent/src/modes/acp/acp-mode.ts`.

## Bugs (Incorrect Behavior)

### BUG-1: `BuildModelState` returns all models instead of available ones [FIXED 2026-02-17]
- **File:** `pkg/modes/acp/helpers.go`
- **Fix:** Changed `reg.GetAll()` → `reg.GetAvailable()`. Now only models with auth configured are returned.

## Missing Features — Fixed

### GAP-2: Missing `/login` slash command [FIXED 2026-02-17]
- Added `login` to `builtInCommands()` and `handleLogin()` implementation in `acp.go`.
- Uses `authStorage.Login()` with ACP-message-based progress callbacks.

### GAP-5: Missing `/changelog` slash command [FIXED 2026-02-17]
- Added `changelog` to `builtInCommands()` and handler in `handleSlashCommand`.
- Uses `core.ParseChangelog`.

### GAP-6: Missing session capabilities in `initialize` response [FIXED 2026-02-17]
- The `rawMethodHandler` in `conn.go` now augments the `initialize` response JSON to inject
  `sessionCapabilities: { list: {}, resume: {} }` into `agentCapabilities`.
- This tells Zed we support `session/list` and `session/resume`.

### GAP-7: Missing `session/list` and `session/resume` ACP protocol methods [FIXED 2026-02-17]
- Replaced `acpsdk.NewAgentSideConnection` with `newRawConn` in `RunAcpMode`.
- `newRawConn` uses `acpsdk.NewConnection` directly with `rawMethodHandler` (our own method dispatch).
- `rawMethodHandler` routes ALL stable methods PLUS `session/list` → `pa.ListSessions` and
  `session/resume` → `pa.ResumeSession`.
- Outbound calls still use SDK constants (`acpsdk.ClientMethodSessionUpdate`, etc.).
- `rawConn` struct implements `acpConn` interface using `acpsdk.SendRequest` / `conn.SendNotification`.
- `ListSessions` and `ResumeSession` types defined in `types.go`.

### GAP-8: Missing client FS delegation (readTextFile/writeTextFile) [FIXED 2026-02-17]
- Added `ReadTextFile`/`WriteTextFile` to `acpConn` interface.
- Added `NewReadToolWithReader`, `ReadFileFn` to `pkg/core/tools/read.go`.
- Added `NewWriteToolWithWriter`, `WriteFileFn` to `pkg/core/tools/write.go`.
- Added `NewEditToolWithReadWriter`, `EditReadFn` to `pkg/core/tools/edit.go`.
- `createAcpTools(cwd, sessionID, useClientTerminal, useClientFs, prefix)` takes FS flag.
- When `clientCapabilities.fs.writeTextFile == true`, read/write/edit delegate to ACP client.
- Extracted `applyReadFilters` from `executeRead` to avoid code duplication.

### GAP-9: Missing extension commands in `sendAvailableCommands` [FIXED 2026-02-17]
- `sendAvailableCommands` now calls `entry.extensionRunner.GetCommands()` and adds them.
- `piSession` now stores `extensionRunner *extension.Runner`.

### GAP-10: Missing extension command dispatch in slash handler default case [FIXED 2026-02-17]
- `handleSlashCommand` default case now checks extension runner commands.
- Dispatches via `entry.session.Prompt("/" + command + " " + args)`.

### GAP-11: Missing settings integration in tool creation [FIXED 2026-02-17]
- Added `NewBashToolWithPrefix(cwd, prefix)` to `pkg/core/tools/bash.go`.
- Added `DefaultCodingToolsWithPrefix(cwd, prefix)` to `pkg/core/sdk.go`.
- `createSession` reads `settingsManager.GetShellCommandPrefix()` and passes it to tool creation.
- `createAcpBashTool` accepts and applies `shellCommandPrefix`.
- **Note:** `autoResizeImages` is already handled automatically in the Go read tool (always on).

### GAP-12: Provider ID format validation in `/logout` [FIXED 2026-02-17]
- Added `providerIDRegex = regexp.MustCompile("^[a-zA-Z0-9-]+$")`.
- Applied in both `handleLogout` and `handleLogin` to validate provider ID argument.

## Missing Features — Still Outstanding

### GAP-3: Missing `/export` slash command
- **Severity:** Medium — sessions can't be exported to HTML
- **Problem:** No export case in Go ACP slash handler; `AgentSession` has no `ExportToHTML`.
- **Note:** Requires implementing HTML export in `pkg/core/agentsession.go` first.

### GAP-4: Missing `/share` slash command
- **Severity:** Low — depends on `gh` CLI being installed
- **Note:** Requires export first; then calls `gh gist create`.

## Summary

All P0-P2 gaps fixed (2026-02-17):
- BUG-1: BuildModelState uses GetAvailable() ✅
- GAP-2: /login command ✅
- GAP-5: /changelog command ✅
- GAP-6: sessionCapabilities in initialize ✅
- GAP-7: session/list + session/resume via rawMethodHandler ✅
- GAP-8: Client FS delegation (read/write/edit tools) ✅
- GAP-9+10: Extension commands (available + dispatch) ✅
- GAP-11: Settings integration (shellCommandPrefix) ✅
- GAP-12: Provider ID validation ✅

Remaining (low priority): /export and /share (require HTML export feature first).
