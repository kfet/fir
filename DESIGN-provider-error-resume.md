# Design: provider-error → extension event → schedule auto-resume

Branch: `work/provider-error-resume`

## Problem

When an LLM turn ends in a provider/transport error that the agent's own
in-loop retry does **not** cover — notably Anthropic `Overloaded
(overloaded_error)` / HTTP 529, and rate limits / 429 — the turn fully ends,
`a.state.Error` is set, and the session is **stuck waiting for a human**. The
error is captured internally (`agent.go` `EventTurnEnd` → `state.Error`) but is
**never forwarded to extensions**, so no extension can react.

## Boundary vs the agent's own auto-resume (confirmed)

`pkg/agent/loop.go` already auto-resumes, but **only** for
`isResumableStreamError`: transient *network* errors (TCP reset, broken pipe,
unexpected EOF, i/o timeout, HTTP/2 GOAWAY) and stream-truncation guards. It is
bounded by `autoResumeBackoffs` (500ms, 1.5s, 4s — 3 attempts) and happens
**mid-loop, in-process, keeping the partial prefix**.

It explicitly does **NOT** cover:
- Overloaded / 529
- Rate limit / 429
- Transient 5xx (502/503/504)

Those classes are retryable per `ratelimit.IsRetryableError` but are *not*
`isResumableStreamError`, so they end the turn. **This is the gap we fill.** The
two mechanisms do not overlap → no double-retry:

| Layer | Triggers on | Timescale | Mechanism |
|-------|-------------|-----------|-----------|
| agent loop (existing) | transient transport/stream blips | ms–seconds | in-loop replay, keeps prefix |
| schedule ext (new) | overloaded / rate-limit / 5xx that ended the turn | seconds–minutes | re-prompt session ("continue") after backoff |

## New extension event: `provider_error`

Emitted from `pkg/extension/setup.go` in the `EventTurnEnd` branch when the turn
message is an assistant message with `StopReason == error` and a non-empty
`ErrorMessage`. Payload (`ProviderErrorPayload`):

| field | source |
|-------|--------|
| `error_text` | `am.ErrorMessage` |
| `kind` | classified: `overloaded` / `rate_limit` / `transport` / `server` / `terminal` |
| `retryable` | `ratelimit.IsRetryableError(error_text)` |
| `provider` | `am.Provider` |
| `model` | `am.Model` |
| `retry_after_ms` | `ratelimit.ExtractRetryDelayFromText` (0 if unknown) |

`kind` classification order: rate_limit (IsRateLimitText incl. 429/529) →
overloaded (text contains "overload") → server (IsTransientServerError) →
transport (IsTransientNetworkError) → terminal (everything else / not
retryable). Note 529 "overloaded" matches the rate-limit regex; we special-case
"overload" text to report `overloaded` distinctly while still `retryable=true`.

## schedule.py auto-resume policy (user-locked)

State (per session): `consecutive_failures` (int), `first_failure_ts`
(timestamp), an active backoff schedule entry id. Reset to zero on the **first
successful turn** — we subscribe to `turn_end`/`message_end` and clear state
when a turn ends without a provider_error.

### Confirmed defaults (user-locked)

- **Trigger**: only when `retryable == true`. Terminal errors
  (auth/400/context-length) are surfaced to the user, never auto-resumed.
- **Backoff**: **no jitter** (we are small-fish traffic for providers). If the
  provider specifies a `retry_after` (`retry_after_ms > 0`), **honour it**. If
  unknown, use the fixed escalating schedule **30s, 1m, 1.75m, 2m, 2m, 2m, …**
  (steady at 2m after the ramp).
- **Give-up limit**: stop after **20 minutes** of continuous failure
  (`first_failure_ts → now > 20m`). On give-up, post a visible message that the
  provider has been failing for N attempts / M minutes and auto-resume stopped.
- **Always ON** when the schedule extension is loaded. **No toggle** subcommand.
- **Reset semantics**: `consecutive_failures=0`, `first_failure_ts=None` on the
  first non-error `turn_end`. A user manually prompting also implicitly resets
  via the same path.

## Deliverables / sync

- `ProviderErrorPayload` + emit in `setup.go`; Go test first (event emission).
- `provider_error` documented in `docs/extension-protocol.md`, mirrored in
  `fir_ext.py` module docstring, exercised in `demo.py` + `demo_ext_test.py`.
- schedule.py: subscribe + backoff/counter/give-up + reset + toggle.
- CHANGELOG `[Unreleased]` entry.
- `make all` green.
