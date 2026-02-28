---
name: claude-usage
description: Check Anthropic Claude API usage stats using a Claude Code OAuth token
---

# Claude Usage Skill

Query the Anthropic usage API to see rate-limit utilization (5-hour and 7-day windows) using an OAuth token.

## Background

The usage stats endpoint (`https://api.anthropic.com/api/oauth/usage`) requires an **OAuth Bearer token** — not a standard `sk-ant-...` API key. Tools like fir and Claude Code store OAuth tokens locally after login.

Standard API keys will **not** work — the endpoint rejects them.

## Step 1 — Extract the Token

Look for an OAuth token in common locations:

```bash
# Try common credential files
for f in ~/.fir/agent/auth.json ~/.claude/.credentials.json; do
  [ -f "$f" ] && echo "Found: $f"
done
```

### Common locations

| Tool | File | Key |
|------|------|-----|
| fir | `~/.fir/agent/auth.json` | `.anthropic.access` |
| Claude Code | `~/.claude/.credentials.json` | `"access_token"` or `"claudeAiOauthToken"` |

Extract with jq:
```bash
TOKEN=$(jq -r '.anthropic.access' ~/.fir/agent/auth.json 2>/dev/null)
# or
TOKEN=$(jq -r '.claudeAiOauthToken // .access_token' ~/.claude/.credentials.json 2>/dev/null)
```

## Step 2 — Run the Script

The script lives at `scripts/usage.sh` (relative to this skill). Pass the token via the `TOKEN` env var or as the first argument:

```bash
TOKEN="$TOKEN" bash "$SKILL_DIR/scripts/usage.sh"
# or
bash "$SKILL_DIR/scripts/usage.sh" "$TOKEN"
```

## Step 3 — Interpret the Response

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
