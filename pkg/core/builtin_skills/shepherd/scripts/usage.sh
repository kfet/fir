#!/usr/bin/env bash
# usage.sh — fetch and display Anthropic API usage stats
#
# Usage:
#   TOKEN=<bearer-token> ./usage.sh
#   ./usage.sh <bearer-token>
#
# The token must be an OAuth Bearer token (not a standard sk-ant-... API key).
# See the skill's SKILL.md for how to obtain it.

set -euo pipefail

TOKEN="${1:-${TOKEN:-}}"

if [[ -z "$TOKEN" ]]; then
  echo "Error: no token provided." >&2
  echo "Usage: TOKEN=<bearer-token> $0" >&2
  echo "       $0 <bearer-token>" >&2
  exit 1
fi

curl -s \
  -H "Authorization: Bearer $TOKEN" \
  -H "anthropic-beta: oauth-2025-04-20" \
  -H "Accept: application/json" \
  https://api.anthropic.com/api/oauth/usage | python3 -c "
import sys, json, datetime

data = json.load(sys.stdin)
local_tz = datetime.datetime.now().astimezone().tzinfo

def fmt_reset(iso):
    if not iso:
        return '—'
    t = datetime.datetime.fromisoformat(iso).astimezone(local_tz)
    return t.strftime('%b %-d, %-I:%M %p %Z')

def fmt_key(k):
    return k.replace('_', ' ').title()

# Print all non-null window fields (anything with 'utilization'), except extra_usage.
windows = {k: v for k, v in data.items() if k != 'extra_usage' and isinstance(v, dict) and 'utilization' in v}
if windows:
    label_width = max(len(fmt_key(k)) for k in windows)
    for key, w in windows.items():
        label = fmt_key(key).ljust(label_width)
        resets = fmt_reset(w.get('resets_at'))
        print(f\"{label}  {w['utilization']:.0f}% — resets {resets}\")

# Print extra_usage if present and enabled.
eu = data.get('extra_usage')
if eu and eu.get('is_enabled'):
    used = eu['used_credits'] / 100
    cap  = eu['monthly_limit'] / 100
    pct  = eu['utilization']
    print(f\"Extra Usage{''.ljust(label_width - 11)}  \${used:.2f} / \${cap:.2f} monthly cap ({pct:.0f}%) — overage billing active\")
"
