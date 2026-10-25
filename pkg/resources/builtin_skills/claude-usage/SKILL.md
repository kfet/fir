---
builtin: true
name: claude-usage
description: Check Anthropic Claude API usage — queries 5-hour and 7-day rate-limit windows using a local OAuth token from fir or Claude Code.
override: true
---

# Claude Usage Skill

Query the Anthropic usage API to see rate-limit utilization (5-hour and 7-day windows) using an OAuth token.

## Background

The usage stats endpoint (`https://api.anthropic.com/api/oauth/usage`) requires an **OAuth Bearer token** — not a standard `sk-ant-...` API key. Tools like fir and Claude Code store OAuth tokens locally after login.

Standard API keys will **not** work — the endpoint rejects them.

## Multiple accounts

fir can store more than one Anthropic OAuth login at a time (e.g. a personal and a work/org account). In `auth.json` the bare `anthropic` key is the **default** account, and additional accounts live under composite slot keys `anthropic#<accountId>`.

With no explicit token, the script reports usage for **every** stored Anthropic account, one labelled section each:

```
▸ you@example.com (Personal)
  Five Hour  7% — resets Jun 16, 4:49 PM EEST
  Seven Day  2% — resets Jun 23, 2:59 AM EEST

▸ work@acme.com (Acme Corp)
  Five Hour  41% — resets Jun 16, 5:10 PM EEST
  Seven Day  63% — resets Jun 22, 9:00 PM EEST
```

A failure on one account (e.g. an expired token) is reported under its header and does **not** abort the others; the script exits non-zero if any account failed. When only one account is stored, the section header is omitted (output is identical to the single-account format).

## Step 1 — Run the Script

The script auto-detects your OAuth token(s) from known credential files. Prefer `--cached`: it reads the shared cache kept by the provider-usage extension, which costs nothing and is usually fresher than a poll:

```bash
bash "$SKILL_DIR/scripts/usage.sh" --cached
```

The cache is fed from two sources:

- **Response headers (preferred, live).** Every Claude subscription response carries account-wide `anthropic-ratelimit-unified-*` headers (5h/7d utilization, resets). Each fir session's turns update the cache from them (`headers_at`), so it reflects usage from *every* machine on the account, as of the last turn anywhere.
- **The usage endpoint** (`fetched_at`), polled at most every 5 minutes — only every 30 minutes while header readings are fresh. It adds windows the headers lack (per-model weekly, `extra_usage`).

`--cached` prints the age of each source to stderr, e.g. `(cache: response headers 1m ago, usage endpoint 12m ago)`. Use a live call only when both are old or the user asks for one.

For trends, `anthropic-usage-series.json` (same dir as the cache) holds the recent header readings: `{"entries": [{"t": epoch, "five_hour": pct, "five_hour_reset": iso, "seven_day": pct, ...}]}`. provider-usage raises a burst warning when 5h utilization jumps ≥3 points within 10 minutes (e.g. many sessions resuming at once after a reset).

If no cache is available, it falls back to a live API call automatically. Note: the cache only tracks the **default** account, so under `--cached` any additional accounts are still fetched live.

To force a live API call (bypassing the cache):

```bash
bash "$SKILL_DIR/scripts/usage.sh"
```

You can also pass a token explicitly:

```bash
bash "$SKILL_DIR/scripts/usage.sh" "$TOKEN"
# or
TOKEN="$TOKEN" bash "$SKILL_DIR/scripts/usage.sh"
```

### Where auto-detection looks

| Tool | File | Key(s) |
|------|------|--------|
| fir | `~/.config/fir/auth.json` | `.anthropic.access` (default) **plus** every `anthropic#<account>` slot |
| Claude Code | `~/.claude/.credentials.json` | `"claudeAiOauthToken"` or `"access_token"` |

fir accounts take precedence; the Claude Code credential is only used as a fallback when no fir Anthropic account is stored. Override the fir auth path with `FIR_AUTH_FILE=...` if needed.

### Manual fallback

If auto-detection fails (e.g. non-standard install), extract a token yourself:

```bash
# default fir account
TOKEN=$(jq -r '.anthropic.access' ~/.config/fir/auth.json 2>/dev/null)
# list all stored Anthropic accounts (default + #account slots)
jq -r 'to_entries[] | select(.key=="anthropic" or (.key|startswith("anthropic#"))) | "\(.key)\t\(.value.label // "")"' ~/.config/fir/auth.json
# or Claude Code
TOKEN=$(jq -r '.claudeAiOauthToken // .access_token' ~/.claude/.credentials.json 2>/dev/null)
```

## Step 2 — Interpret the Response

The script prints all non-null usage windows returned by the API, then `extra_usage` if overage billing is enabled.

Example output:

```
Five Hour         36% — resets Feb 25, 6:00 PM PST
Seven Day         71% — resets Feb 27, 10:00 PM PST
Seven Day Sonnet  40% — resets Mar 3, 9:00 PM PST
Extra Usage       $101.60 / $20.00 monthly cap (100%) — overage billing active
```

| Field | Meaning |
|---|---|
| `*.utilization` | % of that window's limit used |
| `*.resets_at` | ISO 8601 timestamp when the window resets (shown in local time) |
| `extra_usage.used_credits` | Overage credits used in **cents** (divide by 100 for dollars) |
| `extra_usage.monthly_limit` | Monthly overage cap in **cents** |
