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

### 1. Slash command: `/schedule` (interactive mode)

```
/schedule 45m          — execute next turn in 45 minutes
/schedule 2pm          — execute next turn at 2:00 PM local time
/schedule 14:00        — execute next turn at 14:00 local time
/schedule cancel       — cancel a pending scheduled run
/schedule              — (no arg) show status of pending schedule, or show usage
```

**Behavior:**
- Shows a live countdown in the activity area using the existing `CountdownTimer`
  component: `⏰ Scheduled — executing in 12m34s (at 2:00 PM)`
- User can press **Escape** or type `/schedule cancel` to cancel without resuming.
- When the timer fires:
  1. Strip the trailing `StopReasonError` assistant message from the agent context
     (keep it in session history, remove from LLM context). This is identical to
     what `runAutoCompaction` does today at `agentsession.go` ~line 835.
  2. Call `session.Agent.Continue()`. The agent replays the last user turn (or
     pending tool results) against the LLM.
- If the last message is NOT an error (user scheduled proactively), no stripping
  occurs — just `Continue()`.

**Why explicit, not automatic?**
Auto-sleeping is dangerous: Claude Code's auto `/rate-limit-options` has a bug where
it fires repeatedly after a session reset, consuming tokens. `/schedule` is always
user-initiated. The `scheduleOnRateLimit: "auto"` setting (§3) is opt-in.

### 2. CLI flags: `--schedule` (print mode / headless)

For scripted / headless / CI / shepherd-fleet usage:

```bash
fir -p --schedule 45m    "fix the tests"
fir -p --schedule 14:00  "fix the tests"
fir -c --schedule 30m                      # continue previous session, wait on rate-limit
```

**Behavior:**
- On a rate-limit error, instead of exiting non-zero, sleep until the scheduled time,
  then retry the full turn.
- On non-rate-limit errors, exit immediately as today (don't block for unrelated
  failures).
- Writes a status line to stderr during the wait:
  `Scheduled — executing in 12m34s (at 14:00)...`
- Respects SIGINT/SIGTERM to cancel the wait and exit cleanly.

### 3. Settings: `retry.scheduleOnRateLimit`

```jsonc
{
  "retry": {
    "enabled": true,
    "maxRetries": 3,
    "baseDelayMs": 2000,
    "maxDelayMs": 60000,
    "scheduleOnRateLimit": "off"   // NEW — "off" | "auto" | duration string e.g. "1h"
  }
}
```

- `"off"` **(default)** — current behavior: error after maxRetries.
- `"auto"` — parse `Retry-After` / `x-ratelimit-reset` headers and error body via
  the new `DetectRateLimit` helper (§4). Wait up to 1 hour automatically. Beyond
  1 hour, prompt the user (interactive) or error (print).
- `"30m"` / `"2h"` — willing to wait up to this duration. If the server-indicated
  delay exceeds this cap, surface the error normally.

### 4. New helper: `pkg/ai/ratelimit.go`

Generalize rate-limit detection and delay extraction out of individual providers
(currently scattered across `google_gemini_cli.go`, `openai_codex_responses.go`, etc.)
into a single shared utility.

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

### 5. New event types in `AgentSession`

Following the existing `auto_compaction_start` / `auto_compaction_end` pattern,
add `schedule_wait_start` and `schedule_wait_end` events so modes get notified
without `agentsession.go` knowing anything about the UI or CLI.

```go
// In AgentSessionEvent (agentsession.go):
// Type == "schedule_wait_start":
//   RateLimitInfo *ai.RateLimitInfo  — info about why we're waiting
//   ScheduledFor  time.Time          — absolute time of planned execution
//
// Type == "schedule_wait_end":
//   Cancelled bool — true if user cancelled, false if timer fired
```

A new method `(s *AgentSession) StartSchedule(at time.Time)` lets the interactive
mode initiate the wait (from `/schedule`) and also lets the session initiate it
automatically (from `scheduleOnRateLimit: "auto"`).

`CancelSchedule()` stops a running wait, emits `schedule_wait_end{Cancelled: true}`.

### 6. Interactive mode wiring

In `mode.go`:

```go
// Handle new session event
case "schedule_wait_start":
    m.showScheduleCountdown(event.ScheduledFor, event.RateLimitInfo)

case "schedule_wait_end":
    m.clearScheduleCountdown()
    if !event.Cancelled {
        // timer fired — agent.Continue() was already called by agentsession
        m.showStatus("Scheduled execution started")
    } else {
        m.showStatus("Scheduled execution cancelled")
    }
```

```go
// Handle new slash command
case "/schedule":
    m.handleScheduleCommand(arg)  // parses arg, calls m.session.StartSchedule(at)
```

Escape key: if `m.activeSchedule != nil`, pressing Escape cancels the schedule
instead of (or in addition to) clearing the editor.

### 7. Print mode wiring (`cmd/fir/app.go`)

```go
if args.Schedule != "" {
    // Parse --schedule value once, store as scheduledAt time.Time
    // After each turn that ends in a rate-limit error:
    //   - print status to stderr
    //   - sleep until scheduledAt
    //   - retry (loop back to run the agent turn again)
}
```

---

## File-by-file touchpoints

| File | Change |
|------|--------|
| `pkg/ai/ratelimit.go` | **New file.** `RateLimitInfo`, `DetectRateLimit()` |
| `pkg/ai/providers/google_gemini_cli.go` | Refactor: delegate `extractRetryDelay` to shared util |
| `pkg/config/settings.go` | Add `ScheduleOnRateLimit string` to `RetrySettings` |
| `pkg/core/agentsession.go` | Add `StartSchedule()`, `CancelSchedule()`, `schedule_wait_start/end` events; call `checkScheduleOnRateLimit()` after error turns when setting is `"auto"` |
| `pkg/modes/interactive/mode.go` | Handle `schedule_wait_start/end` events; `/schedule` slash command; Escape cancels active schedule |
| `pkg/resources/slashcmds.go` | Add `/schedule` to `BuiltinSlashCommands` |
| `cmd/fir/args.go` | Add `--schedule` flag; update `--help` text and examples |
| `cmd/fir/app.go` | Wire `--schedule` into print mode run loop |
| `.fir/skills/self/SKILL.md` | Document `/schedule` slash command and `--schedule` flag |
| `CHANGELOG.md` | Add entry under `## [Unreleased] ### Added` |

---

## Recommended implementation order

1. **`pkg/ai/ratelimit.go`** — detection + delay extraction, fully unit-tested in
   isolation. Refactor `extractRetryDelay` references to use it.
2. **`/schedule` slash command** — interactive mode only, manual trigger. Shows
   countdown, Escape cancels, timer fires `session.Agent.Continue()`. Hardest UI
   piece, good to do early.
3. **`AgentSession` event wiring** — `StartSchedule` / `CancelSchedule` /
   `schedule_wait_start` / `schedule_wait_end`. Wire interactive mode to use them
   so the slash command goes through the session rather than doing its own timer.
4. **`--schedule` CLI flag** — print mode. Simpler: just sleep + retry loop.
5. **`retry.scheduleOnRateLimit` setting** — auto-detect in `AgentSession`, emit
   `schedule_wait_start` automatically when rate-limit detected and setting != "off".
6. **`self` skill + CHANGELOG** — update docs last.

---

## What this does NOT do

- **No automatic model fallback** on rate-limit — separate feature.
- **No cross-session / cron scheduling** — this is within-session only. Use cron +
  `fir -c` for true scheduled jobs.
- **No multi-session orchestration** — the shepherd skill handles that.
- **No infinite retry loop** — max 3 scheduled retries (same `maxRetries` config),
  then surface the error.
