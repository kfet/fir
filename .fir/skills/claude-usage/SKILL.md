---
name: claude-usage
description: Check Anthropic Claude API usage stats using a Claude Code OAuth token
---

# Claude Usage Skill

Query the Anthropic usage API to see rate-limit utilization (5-hour and 7-day windows) using the OAuth token stored locally by fir or Claude Code.

## Background

The usage stats endpoint (`https://api.anthropic.com/api/oauth/usage`) requires an **OAuth Bearer token** — not a standard `sk-ant-...` API key. Both fir and Claude Code store OAuth tokens locally after login.

Standard API keys will **not** work — the endpoint rejects them.

## Step 1 — Extract the Token

### Primary: fir's auth storage (preferred)

fir stores credentials in `~/.fir/agent/auth.json`. Extract the Anthropic access token in one step:

```bash
TOKEN=$(jq -r '.anthropic.access' ~/.fir/agent/auth.json)
```

### Fallback: Claude Code credentials

If not logged in via fir, Claude Code stores credentials in `~/.claude/.credentials.json`:

```bash
cat ~/.claude/.credentials.json
```

Look for a field like `"access_token"` or `"claudeAiOauthToken"`.

If that file doesn't exist, search:

```bash
find ~/.claude -name "*.json" | xargs grep -l "token" 2>/dev/null
```

## Step 2 — Run the Script

The script lives at `scripts/usage.sh` (relative to this skill). Pass the token
from Step 1 via the `TOKEN` env var or as the first argument:

```bash
TOKEN=<bearer-token> /path/to/skill/scripts/usage.sh
# or
/path/to/skill/scripts/usage.sh <bearer-token>
```

The script accepts the token externally so the AI can resolve the correct source
(fir, Claude Code, etc.) using Step 1 above and inject it at call time.

## Step 3 — Interpret the Response

The script prints all non-null usage windows returned by the API (field names vary
by plan), then `extra_usage` if overage billing is enabled. Labels are derived
from the API field names (e.g. `seven_day_sonnet` → `Seven Day Sonnet`).

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
