---
builtin: true
name: claude-usage
description: Check Anthropic Claude API usage — queries 5-hour and 7-day rate-limit windows using a local OAuth token from fir or Claude Code.
---

# Claude Usage Skill

Query the Anthropic usage API to see rate-limit utilization (5-hour and 7-day windows) using an OAuth token.

## Background

The usage stats endpoint (`https://api.anthropic.com/api/oauth/usage`) requires an **OAuth Bearer token** — not a standard `sk-ant-...` API key. Tools like fir and Claude Code store OAuth tokens locally after login.

Standard API keys will **not** work — the endpoint rejects them.

## Step 1 — Run the Script

The script auto-detects your OAuth token from known credential files. The recommended approach is to use `--cached`, which reads from the local cache maintained by the provider-usage extension (refreshed every 5 minutes) — this avoids redundant API calls and rate-limit issues:

```bash
bash "$SKILL_DIR/scripts/usage.sh" --cached
```

If no cache is available, it falls back to a live API call automatically.

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

| Tool | File | Key |
|------|------|-----|
| fir | `~/.fir/agent/auth.json` | `.anthropic.access` |
| Claude Code | `~/.claude/.credentials.json` | `"claudeAiOauthToken"` or `"access_token"` |

### Manual fallback

If auto-detection fails (e.g. non-standard install), extract the token yourself:

```bash
TOKEN=$(jq -r '.anthropic.access' ~/.fir/agent/auth.json 2>/dev/null)
# or
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
