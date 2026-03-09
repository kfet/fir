# Feature: Scheduled Session Resume (`/schedule`)

## Problem

When a provider rate-limit is hit (429 / "resource exhausted" / plan quota), fir's
provider-level retry loop gives up after `maxRetries` (default 3) and surfaces the error
to the user as a `StopReasonError`. The user sees the error, knows the limit resets at
e.g. 2 PM, but has no way to tell fir "resume at that time." They must manually come
back and type something or run `fir -c`.

## Prior Art

- **Claude Code** has `/rate-limit-options` that auto-fires on quota exhaustion. There
  are open requests for "auto-continue once limit resets" but it is not implemented.
  Its auto-fire behaviour has caused a bug where it fires repeatedly, consuming tokens.
- **Zed** has an open issue (zed-industries/zed#31531) requesting wait/retry on
  rate-limit instead of hard error.
- Most tools just error out and leave the user to retry manually.

## Command Name: `/schedule` (not `/wait`)

`/wait` sounds passive (like `sleep`). The agent will actually **execute work** — a
potentially long, expensive turn — when the timer fires. `/schedule` conveys deferred
execution, not mere pause.

---

## Design

This feature is implemented as a **fir extension** (`.fir/extensions/schedule.py`),
not as core code. The only core change needed is exposing `ctx.continue_session()` on
the extension bridge API.

### 1. Core change: `ctx.continue_session()`

Add a single new method to the extension bridge that calls `session.Agent.Continue()`
without injecting any message into the session history.

**Go side:**

```go
// BridgeAPI (api.go) — add:
ContinueSession() error

// SessionBridge (session_bridge.go) — implement:
func (b *SessionBridge) ContinueSession() error {
    return b.session.Agent.Continue()
}

// Bridge (bridge.go) — handle inbound "continue_session" request:
case "continue_session":
    if err := api.ContinueSession(); err != nil {
        rpcErr = &Error{Code: -32000, Message: err.Error()}
    } else {
        result = map[string]any{"ok": true}
    }
```

**Python SDK:**

```python
# Context class in fir_ext.py — add:
def continue_session(self) -> None:
    """Resume the agent session (replay the last turn)."""
    self._call("continue_session")
```

### 2. Extension: `.fir/extensions/schedule.py`

```
/schedule 45m          — execute next turn in 45 minutes
/schedule 2pm          — execute next turn at 2:00 PM local time
/schedule 14:00        — execute next turn at 14:00 local time
/schedule cancel       — cancel a pending scheduled run
/schedule              — (no arg) show status of pending schedule, or show usage
```

**Behavior:**
- Parses relative durations (`45m`, `1h30m`) and absolute times (`2pm`, `14:00`).
- Starts a background `threading.Timer` that calls `ctx.set_status()` every second
  with a live countdown: `⏰ Scheduled — executing in 12m34s (at 2:00 PM)`
- `/schedule cancel` cancels the timer and clears the status.
- When the timer fires, calls `ctx.continue_session()` to replay the last turn.
- Only one schedule can be active at a time. Starting a new one replaces the old.

### 3. Rate-limit detection helper: `pkg/ai/ratelimit.go`

Generalize rate-limit detection and delay extraction out of individual providers
(currently scattered across `google_gemini_cli.go`, `openai_codex_responses.go`, etc.)
into a single shared utility. This is useful beyond the schedule feature and belongs
in core.

```go
package ai

// RateLimitInfo describes a detected rate-limit condition.
type RateLimitInfo struct {
    IsRateLimit bool
    RetryAfter  time.Duration // 0 = unknown / not parseable
    Message     string        // cleaned human-readable error text
}

// DetectRateLimit checks if an AssistantMessage error is a rate-limit and
// extracts any server-indicated retry delay from the error message text.
func DetectRateLimit(msg *AssistantMessage) RateLimitInfo { ... }
```

Patterns to match (already scattered across providers, consolidate here):
- HTTP 429 in error text (`"429 "`)
- `"rate limit"`, `"rate_limit"`, `"resource exhausted"`, `"overloaded"`
- `"usage limit reached"`, `"quota exceeded"`, `"too many requests"`
- Delay extraction: `"reset after Xh Ym Zs"`, `"Please retry in Xs"`,
  `Retry-After` header seconds/timestamp, `x-ratelimit-reset`,
  `x-ratelimit-reset-after` (reuse logic from `extractRetryDelay` in
  `google_gemini_cli.go`).

---

## File-by-file touchpoints

| File | Change |
|------|--------|
| `pkg/extension/api.go` | Add `ContinueSession() error` to `BridgeAPI` |
| `pkg/extension/session_bridge.go` | Implement `ContinueSession()` |
| `pkg/extension/bridge.go` | Handle inbound `"continue_session"` request |
| `pkg/extension/sdk/python/fir_ext.py` | Add `ctx.continue_session()` method |
| `.fir/extensions/schedule.py` | **New file.** The extension itself |
| `pkg/ai/ratelimit.go` | **New file.** `RateLimitInfo`, `DetectRateLimit()` |
| `pkg/ai/providers/google_gemini_cli.go` | Refactor: delegate `extractRetryDelay` to shared util |
| `.fir/skills/self/SKILL.md` | Document `/schedule` command |
| `CHANGELOG.md` | Add entry under `## [Unreleased] ### Added` |

---

## Recommended implementation order

1. **`ctx.continue_session()`** — add to `BridgeAPI`, `SessionBridge`, `Bridge`,
   and `fir_ext.py`. Small, testable change.
2. **`.fir/extensions/schedule.py`** — the extension. Command parsing, timer,
   countdown via `set_status`, fire via `continue_session`.
3. **`pkg/ai/ratelimit.go`** — detection + delay extraction, fully unit-tested.
   Refactor `extractRetryDelay` references. (Independent of the extension; can
   be used later to auto-suggest `/schedule` with a parsed delay.)
4. **`self` skill + CHANGELOG** — update docs last.

---

## What this does NOT do

- **No CLI/print mode scheduling** — use `sleep 45m && fir -p ...` or system cron.
- **No automatic triggering** — `/schedule` is always user-initiated.
- **No automatic model fallback** on rate-limit — separate feature.
- **No cross-session / cron scheduling** — this is within-session only. Use cron +
  `fir -c` for true scheduled jobs.
- **No multi-session orchestration** — the shepherd skill handles that.
- **No infinite retry loop** — a single scheduled retry per `/schedule` invocation.
