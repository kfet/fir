---
builtin: true
name: codex-usage
description: Check OpenAI Codex / ChatGPT subscription usage — queries rate-limit windows, plan type and credits using a local OAuth token from fir or the Codex CLI.
override: true
---

# Codex Usage Skill

Query the ChatGPT backend usage endpoint to see Codex rate-limit utilization (the 5-hour / weekly / monthly windows a plan happens to have), plan type, and credit balance, using a ChatGPT **OAuth Bearer token**.

## Background

The endpoint is `GET https://chatgpt.com/backend-api/wham/usage`. It needs an OAuth token issued by a ChatGPT login (what `fir auth login openai-codex` stores) — a platform `sk-...` API key will **not** work.

It is also fussy about headers: `Authorization`, `chatgpt-account-id`, `originator: codex_cli_rs` and a `codex_cli_rs/<version>` `User-Agent`. Omitting the User-Agent gets you a Cloudflare 403 HTML page rather than JSON.

The same numbers also ride back as `x-codex-*` response headers (`x-codex-plan-type`, `x-codex-primary-used-percent`, `x-codex-secondary-used-percent`, `x-codex-primary-window-minutes`, `x-codex-primary-reset-at`, `x-codex-credits-*`) on any real `POST /backend-api/codex/responses` call — but reading usage that way costs a model call, so this skill uses the GET endpoint.

### Dead ends — do not retry these

`/backend-api/codex/usage`, `/backend-api/codex/rate_limits`, `/backend-api/my/rate_limits` and `/backend-api/accounts/check/...` all return Cloudflare 403. `api.openai.com/v1/usage` rejects the OAuth JWT outright. `wham/usage` is the one that works.

## Multiple accounts

fir can store more than one Codex OAuth login at a time. In `auth.json` the bare `openai-codex` key is the **default** account, and additional accounts live under composite slot keys `openai-codex#<accountId>`.

With no explicit token, the script reports usage for **every** stored Codex account, one labelled section each:

```
▸ you@example.com
  Plan      plus
  Weekly    34% — resets Sep 10, 4:49 PM EEST
  5-Hour     7% — resets Sep 3, 6:00 PM EEST
  Credits   $12.40 balance

▸ work@acme.com
  Plan      free
  Monthly    2% — resets Sep 30, 11:39 AM EEST
```

A failure on one account (e.g. an expired token) is reported under its header and does **not** abort the others; the script exits non-zero if any account failed. When only one account is stored, the section header is omitted.

## Step 1 — Run the Script

The script auto-detects your OAuth token(s) from known credential files:

```bash
bash "$SKILL_DIR/scripts/usage.sh"
```

You can also pass a token explicitly:

```bash
bash "$SKILL_DIR/scripts/usage.sh" "$TOKEN"
# or
TOKEN="$TOKEN" bash "$SKILL_DIR/scripts/usage.sh"
```

`--raw` prints the full JSON response; `--verbose` prints the whole response body when a request fails.

There is no cached mode — unlike Anthropic usage, no fir extension maintains a Codex usage cache, so every run is a live call.

### Where auto-detection looks

| Tool | File | Key(s) |
|------|------|--------|
| fir | `~/.config/fir/auth.json` | `.["openai-codex"].access` + `.extra.accountId` (default) **plus** every `openai-codex#<account>` slot |
| Codex CLI | `~/.codex/auth.json` | `.tokens.access_token` / `.tokens.account_id` |

fir accounts take precedence; the Codex CLI credential is only used as a fallback when no fir Codex account is stored. Override the paths with `FIR_AUTH_FILE=...` / `CODEX_AUTH_FILE=...`.

When a stored account has no `accountId` recorded (and for an explicitly supplied token), the script recovers it from the access token itself — it is a JWT carrying `chatgpt_account_id` under the `https://api.openai.com/auth` claim.

### Manual fallback

```bash
# default fir account
TOKEN=$(jq -r '.["openai-codex"].access' ~/.config/fir/auth.json 2>/dev/null)
ACCOUNT=$(jq -r '.["openai-codex"].extra.accountId' ~/.config/fir/auth.json 2>/dev/null)
# list all stored Codex accounts (default + #account slots)
jq -r 'to_entries[] | select(.key=="openai-codex" or (.key|startswith("openai-codex#"))) | "\(.key)\t\(.value.label // "")"' ~/.config/fir/auth.json
# or Codex CLI
TOKEN=$(jq -r '.tokens.access_token' ~/.codex/auth.json 2>/dev/null)

curl -s https://chatgpt.com/backend-api/wham/usage \
  -H "Authorization: Bearer $TOKEN" \
  -H "chatgpt-account-id: $ACCOUNT" \
  -H "originator: codex_cli_rs" \
  -H "User-Agent: codex_cli_rs/0.56.0"
```

## Step 2 — Interpret the Response

Windows are **named from their declared length** (`limit_window_seconds`), not from which slot they arrive in: 18000s → `5-Hour`, 604800s → `Weekly`, 2592000s → `Monthly`. Which windows exist depends on the plan — a free plan typically reports only `primary_window` (monthly), while paid plans populate both `primary_window` (weekly) and `secondary_window` (5-hour) and carry a non-null credit balance. Any `additional_rate_limits` (prefixed `Extra`) and `code_review_rate_limit` (prefixed `Review`) are printed the same way.

Example output:

```
Plan      plus
Weekly    34% — resets Sep 10, 4:49 PM EEST
5-Hour     7% — resets Sep 3, 6:00 PM EEST
Credits   $12.40 balance
Status    rate limit reached (secondary)
```

| Field | Meaning |
|---|---|
| `plan_type` | `free`, `plus`, `pro`, `team`, … |
| `rate_limit.*_window.used_percent` | % of that window's limit used |
| `rate_limit.*_window.limit_window_seconds` | Window length — the source of its label |
| `rate_limit.*_window.reset_at` | Unix seconds when the window resets (shown in local time; falls back to `reset_after_seconds`) |
| `rate_limit.limit_reached` / `rate_limit_reached_type` | Whether a limit is currently hit, and which window |
| `credits.balance` | Credit balance in dollars (`null` when the plan has none; `unlimited` is a separate flag) |
| `credits.overage_limit_reached`, `spend_control.reached` | Surfaced on the `Status` line |

Most fields are nullable — a plan simply omits what it doesn't have, and the script skips anything absent.
