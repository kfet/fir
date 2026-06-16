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
▸ kalin.f@gmail.com (Personal)
  Five Hour  7% — resets Jun 16, 4:49 PM EEST
  Seven Day  2% — resets Jun 23, 2:59 AM EEST

▸ work@acme.com (Acme Corp)
  Five Hour  41% — resets Jun 16, 5:10 PM EEST
  Seven Day  63% — resets Jun 22, 9:00 PM EEST
```

A failure on one account (e.g. an expired token) is reported under its header and does **not** abort the others; the script exits non-zero if any account failed. When only one account is stored, the section header is omitted (output is identical to the single-account format).

## Step 1 — Run the Script

The script auto-detects your OAuth token(s) from known credential files. The recommended approach is to use `--cached`, which reads from the local cache maintained by the provider-usage extension (refreshed every 5 minutes) — this avoids redundant API calls and rate-limit issues:

```bash
bash "$SKILL_DIR/scripts/usage.sh" --cached
```

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
