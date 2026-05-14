---
builtin: true
name: poe-usage
description: Check Poe API usage — queries current point balance and usage history using a Poe API key from the environment or fir config.
override: true
---

# Poe Usage Skill

Query the Poe Usage API to check your point balance and see per-bot usage history.

## Prerequisites

The script resolves your Poe API key from (in order):

1. **`POE_API_KEY` environment variable** — set it in your shell
2. **fir models config** — `~/.config/fir/models.json` at `.providers.Poe.apiKey`
3. **fir auth storage** — `~/.config/fir/auth.json` under the `"poe"` provider key.
   Both credential shapes are supported:
   - `{"type": "api_key", "key": "sk-poe-..."}` (manual API key)
   - `{"type": "oauth", "access": "sk-poe-...", "refresh": "...", "expires": 1234}` (written by `fir login poe`)

The OAuth `access` token is itself a usable Poe API key, so either shape works.
If the stored OAuth token is expired, the script prints a warning and still tries
the token; re-run `fir login poe` to refresh it.

To store a manual key in fir's auth storage, add a `poe` entry to `~/.config/fir/auth.json`:

```json
{
  "poe": { "type": "api_key", "key": "YOUR_POE_API_KEY" }
}
```

Get your API key at https://poe.com/api/keys.

## Step 1 — Run the Script

```bash
bash "$SKILL_DIR/scripts/usage.sh"
```

### Options

```bash
# Show balance only
bash "$SKILL_DIR/scripts/usage.sh" --balance

# Show usage history (default: last 100 entries)
bash "$SKILL_DIR/scripts/usage.sh" --history

# Show both (default)
bash "$SKILL_DIR/scripts/usage.sh"

# Limit history entries and paginate
bash "$SKILL_DIR/scripts/usage.sh" --limit 50

# Filter to last N days
bash "$SKILL_DIR/scripts/usage.sh" --days 7
```

| Flag | Description | Default |
|------|-------------|---------|
| `--balance` | Show only current point balance | off (show both) |
| `--history` | Show only usage history | off (show both) |
| `--limit` | Max entries per API page (1-100) | 100 |
| `--days` | Only show entries from last N days | 30 |

## Step 2 — Interpret the Response

Example output:

```
Poe Point Balance
=================
Current balance: 1,500 points

Usage History (last 7 days)
============================

Bot                     Calls   Points    Cost ($)
──────────────────────────────────────────────────────
Claude-3.5-Sonnet          42    3,390      2.53
GPT-4                      18    1,908      1.43
──────────────────────────────────────────────────────
TOTAL                      60    5,298      3.96
```

### API Fields

| Field | Meaning |
|---|---|
| `current_point_balance` | Available points across plan + add-ons |
| `bot_name` | Model/bot used for the query |
| `cost_points` | Points consumed by a single query |
| `cost_usd` | USD cost for that query |
| `creation_time` | Unix timestamp in **microseconds** |
| `query_id` | Unique query ID (used as pagination cursor) |
| `cost_breakdown_in_points` | Per-query breakdown of input/output token costs |
| `usage_type` | Where points were spent: Chat, API, Canvas App |

## Troubleshooting

- **401 error**: `POE_API_KEY` is invalid or expired. Get a new one at https://poe.com/api/keys.
- **Empty results**: No activity in the queried period. Try `--days 30`.
