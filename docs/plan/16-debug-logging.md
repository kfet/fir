# 16 — Debug Logging

## Problem

Fir has no debug logging. When something goes wrong — wrong model resolved, API call fails silently, tool output truncated, compaction misfires, MCP server disconnects — the only recourse is adding temporary `fmt.Fprintf(os.Stderr, ...)` calls and rebuilding. This is especially painful because fir runs in multiple modes (TUI, print, JSON-RPC, ACP) where stderr/stdout are protocol channels, not free-form debug dumps.

Current state:
- 9 files use `fmt.Fprintf(os.Stderr, ...)` for user-facing warnings
- 5 files use `log.Printf()` for internal errors (extension panics, session I/O)
- No structured logging, no debug-level output, no request tracing
- `pkg/mcp/client.go` already imports `log/slog` for MCP log-level mapping but doesn't use it for fir's own logging

## Design

### Package: `pkg/log`

A thin wrapper around `log/slog` from stdlib.

```
pkg/log/
├── log.go          # Init, Debug, Info, Warn, Error + global logger
└── log_test.go     # Tests
```

### API

```go
package log

// Init configures the global debug logger. Must be called early in startup.
// When enabled is false, all log calls are no-ops (zero allocation).
// When enabled is true, logs are written to the file at path.
// Returns a cleanup function that flushes and closes the file.
func Init(enabled bool, path string) (cleanup func(), err error)

// Debug logs at debug level. No-op when disabled.
func Debug(msg string, args ...any)

// Info logs at info level. No-op when disabled.
func Info(msg string, args ...any)

// Warn logs at warn level. No-op when disabled.
func Warn(msg string, args ...any)

// Error logs at error level. No-op when disabled.
func Error(msg string, args ...any)

// With returns a logger with the given attributes pre-set.
// Useful for per-component loggers: toolLog := log.With("component", "bash")
func With(args ...any) *slog.Logger
```

### Implementation details

- **Global `slog.Logger`** stored in a package-level `var`. When disabled, set to `slog.New(discardHandler{})` — a handler whose `Enabled()` returns false, so slog short-circuits before allocating args.
- **File output only.** Never writes to stdout or stderr. This is critical: stdout is the protocol channel for JSON-RPC and ACP modes; stderr is used for user-facing warnings in print mode and would corrupt TUI rendering.
- **Default path:** `~/.fir/agent/debug.log`. Mirrors existing `~/.fir/agent/` directory for session storage.
- **File rotation:** Not in scope for v1. The file is truncated on each run (open with `O_CREATE|O_WRONLY|O_TRUNC`). Users can `tail -f` it.
- **slog.JSONHandler** for the file output — structured, grep-friendly, machine-parseable.
- **Timestamps** included automatically by slog (RFC3339 with milliseconds).

### Activation

Two ways to enable, checked in `cmd/fir/app.go` at the top of `run()`:

1. **`--debug` flag** — add to `ParseArgs()` in `cmd/fir/args.go`
2. **`FIR_DEBUG=1` env var** — checked if flag not set

Optional path override:

3. **`--debug-log-file <path>`** flag
4. **`FIR_DEBUG_LOG=<path>`** env var

Pseudocode for `run()`:

```go
debugEnabled := args.Debug || os.Getenv("FIR_DEBUG") == "1"
debugPath := args.DebugLogFile
if debugPath == "" {
    debugPath = os.Getenv("FIR_DEBUG_LOG")
}
if debugPath == "" {
    debugPath = filepath.Join(agentDir, "debug.log")
}
cleanup, err := log.Init(debugEnabled, debugPath)
if err != nil { return err }
defer cleanup()
```

### No-op cost when disabled

When `Init(false, "")` is called (or Init is never called), the global logger uses `discardHandler{}`:

```go
type discardHandler struct{}
func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (discardHandler) WithAttrs([]slog.Attr) slog.Handler        { return discardHandler{} }
func (discardHandler) WithGroup(string) slog.Handler              { return discardHandler{} }
```

Because `slog.Logger.Debug()` calls `handler.Enabled()` first and returns immediately when it's false, no argument formatting or allocation occurs. The inliner should eliminate the call entirely in hot paths.

---

## Where to Add Debug Logs

### 1. `cmd/fir/app.go` — App startup

| Location | What to log |
|---|---|
| `run()` top | `log.Info("fir starting", "version", version, "mode", mode, "pid", os.Getpid())` |
| `run()` after `ParseArgs` | `log.Debug("args parsed", "provider", args.Provider, "model", args.Model, "mode", args.Mode)` |
| `setupSession()` model resolution | `log.Info("model resolved", "provider", model.Provider, "model", model.ID, "source", source)` |
| `setupSession()` session creation | `log.Debug("session created", "sessionID", id, "resumed", isResume)` |
| `setupSession()` thinking level | `log.Debug("thinking level", "requested", requested, "clamped", actual, "modelMax", max)` |

### 2. `pkg/agent/loop.go` — Agent loop

| Location | What to log |
|---|---|
| `runLoop()` outer loop start | `log.Debug("agent loop iteration", "messageCount", len(currentCtx.Messages))` |
| `streamAssistantResponse()` before stream | `log.Debug("streaming request", "model", config.Model.ID, "provider", config.Model.Provider, "messages", len(llmMessages), "tools", len(llmTools))` |
| `streamAssistantResponse()` after stream | `log.Debug("stream complete", "stopReason", finalMsg.StopReason, "contentBlocks", len(finalMsg.Content))` |
| `executeToolCalls()` | `log.Debug("executing tool", "name", toolCall.Name, "id", toolCall.ID)` |

### 3. `pkg/ai/providers/anthropic.go` (and other providers)

| Location | What to log |
|---|---|
| `StreamAnthropic()` request start | `log.Debug("anthropic request", "url", url, "model", model.ID, "messageCount", len(prompt.Messages))` |
| HTTP response received | `log.Debug("anthropic response", "status", resp.StatusCode, "requestID", resp.Header.Get("request-id"))` |
| SSE event processing | `log.Debug("anthropic event", "type", eventType)` (at a reduced frequency or behind a verbose sub-flag to avoid flooding) |
| Error/retry | `log.Warn("anthropic error", "status", status, "retryAfter", retryAfter, "err", err)` |

Apply the same pattern to `openai.go`, `google.go`, `bedrock.go`.

### 4. `pkg/core/tools/bash.go` (and other tools)

| Location | What to log |
|---|---|
| `executeBash()` start | `log.Debug("bash exec", "command", truncate(command, 200), "cwd", cwd, "timeout", timeout)` |
| `executeBash()` complete | `log.Debug("bash done", "exitCode", exitCode, "outputLen", len(output), "truncated", truncated, "elapsed", elapsed)` |

Same pattern for `read.go`, `edit.go`, `write.go`, `grep.go`, `find.go`, `ls.go`.

### 5. `pkg/core/agentsession.go` — Session lifecycle

| Location | What to log |
|---|---|
| `NewAgentSession()` | `log.Debug("agent session created", "sessionID", id, "hasCompaction", runner != nil)` |
| `Prompt()` | `log.Debug("prompt received", "messageLen", len(msg), "isSkill", isSkill)` |
| `checkAutoCompaction()` | `log.Debug("compaction check", "contextTokens", tokens, "window", window, "shouldCompact", should)` |
| `runAutoCompaction()` | `log.Info("auto-compaction triggered", "reason", reason)` |
| Message persistence | `log.Debug("message persisted", "role", role, "entryID", id)` |

### 6. `pkg/extension/runner.go` + `integration.go` — Extensions

| Location | What to log |
|---|---|
| `LoadEnabled()` | `log.Debug("loading extension", "name", name)` |
| `LoadEnabled()` complete | `log.Info("extensions loaded", "count", len(loaded), "tools", len(tools), "commands", len(commands))` |
| `Setup()` | `log.Debug("extension setup", "enabledNames", names)` |
| `Reload()` | `log.Info("extensions reloading", "newNames", names)` |
| Event emission | `log.Debug("extension event", "type", eventType, "handlerCount", len(handlers))` |
| Tool hook (OnToolCall) | `log.Debug("tool hook", "tool", toolName, "blocked", blocked)` |

### 7. `pkg/mcp/client.go` — MCP client

| Location | What to log |
|---|---|
| `Start()` | `log.Info("mcp starting", "servers", len(configs))` |
| Per-server connect | `log.Debug("mcp connecting", "server", name, "command", cmd)` |
| Per-server connected | `log.Info("mcp connected", "server", name, "tools", len(tools))` |
| Per-server error | `log.Warn("mcp connection failed", "server", name, "err", err)` |
| Tool call to MCP server | `log.Debug("mcp tool call", "server", server, "tool", toolName)` |
| `Shutdown()` | `log.Debug("mcp shutting down", "servers", len(sessions))` |

### 8. `pkg/core/compaction/` — Compaction

| Location | What to log |
|---|---|
| `runner.go: RunCompaction()` | `log.Info("compaction starting", "contextTokens", tokens, "keepRecent", keep)` |
| `compaction.go: Compact()` | `log.Debug("compaction request", "messageCount", len(messages), "model", model.ID)` |
| After compaction | `log.Info("compaction complete", "tokensBefore", before, "tokensAfter", after, "ratio", ratio)` |

### 9. `pkg/modes/` — Mode entry points

| Location | What to log |
|---|---|
| `rpc/server.go` start | `log.Info("rpc server starting")` |
| `rpc/server.go` command received | `log.Debug("rpc command", "method", method)` |
| `acp/acp.go` start | `log.Info("acp server starting")` |
| `print/print.go` start | `log.Debug("print mode", "outputMode", mode)` |

---

## Task Breakdown

### Task 1: Create `pkg/log` package

**Files to create:**
- `pkg/log/log.go` — `Init()`, `Debug/Info/Warn/Error()`, `With()`, `discardHandler`
- `pkg/log/log_test.go` — Tests for:
  - Disabled mode produces no output
  - Enabled mode writes JSON lines to file
  - `With()` returns sub-logger with pre-set attrs
  - `Init()` returns working cleanup function
  - `Init()` with bad path returns error
  - Default (no Init call) is safe no-op

**Files to modify:**
- `cmd/fir/args.go` — Add `Debug bool` and `DebugLogFile string` fields to `Args`, parse `--debug` and `--debug-log-file` flags
- `cmd/fir/app.go` — Call `log.Init()` at top of `run()`, defer cleanup. Check `FIR_DEBUG` and `FIR_DEBUG_LOG` env vars.

**Test:** `go test ./pkg/log/...` and `go build ./cmd/fir` (verify it compiles with no-op default).

### Task 2: Add debug logs to core agent loop and session

**Files to modify:**
- `pkg/agent/loop.go` — Log loop iterations, stream start/end, tool execution
- `pkg/core/agentsession.go` — Log session creation, prompt handling, compaction checks, message persistence
- `pkg/core/session.go` — Log session file I/O (new session, append, load)
- `pkg/core/compaction/runner.go` — Log compaction trigger and result

**Test:** `go test ./pkg/agent/... ./pkg/core/...` — existing tests must still pass (logger defaults to no-op).

### Task 3: Add debug logs to providers, tools, and MCP

**Files to modify:**
- `pkg/ai/providers/anthropic.go` — Log request/response/retry
- `pkg/ai/providers/openai.go` — Same pattern
- `pkg/ai/providers/google.go` — Same pattern
- `pkg/ai/providers/bedrock.go` — Same pattern
- `pkg/core/tools/bash.go` — Log command execution and result
- `pkg/core/tools/read.go` — Log file reads
- `pkg/core/tools/edit.go` — Log file edits
- `pkg/core/tools/write.go` — Log file writes
- `pkg/mcp/client.go` — Log server lifecycle, tool calls, errors

**Test:** `go test ./pkg/ai/providers/... ./pkg/core/tools/... ./pkg/mcp/...`

### Task 4: Add debug logs to extensions and modes

**Files to modify:**
- `pkg/extension/runner.go` — Log extension loading, event dispatch, panics
- `pkg/extension/integration.go` — Log setup, tool hooks, reload
- `pkg/modes/rpc/server.go` — Log RPC command handling
- `pkg/modes/acp/acp.go` — Log ACP session lifecycle
- `pkg/modes/print/print.go` — Log print mode entry
- `cmd/fir/app.go` — Log mode dispatch, model resolution, settings load

**Test:** `go test ./pkg/extension/... ./pkg/modes/...`

---

## Non-goals (v1)

- **Log rotation / size limits** — truncate-on-start is sufficient. Add rotation later if needed.
- **Runtime log-level changes** — always debug level when enabled, off when disabled.
- **Per-package enable/disable** — all-or-nothing. Filter with `grep` on the JSON output.
- **Replacing existing `fmt.Fprintf(os.Stderr, ...)` calls** — those are user-facing messages, not debug logs. Leave them as-is.
- **Replacing existing `log.Printf()` calls** — migrate these in a follow-up once `pkg/log` is established and the semantics are validated.
