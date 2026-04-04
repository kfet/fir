#!/usr/bin/env bash
# usage.sh — fetch and display Anthropic API usage stats
#
# Usage:
#   ./usage.sh                      # auto-detects token from known credential files
#   ./usage.sh <bearer-token>       # explicit token as argument
#   TOKEN=<bearer-token> ./usage.sh # explicit token via env var
#
# Options:
#   --raw         Print the full JSON response instead of the formatted summary
#   --verbose     On error, print the full response body for debugging
#   --cached      Read from the local cache (~/.config/fir/anthropic-usage-cache.json)
#                 written by the provider-usage extension, instead of hitting the API.
#                 Falls back to a live API call if the cache is missing.
#
# The token must be an OAuth Bearer token (not a standard sk-ant-... API key).
# Auto-detection searches ~/.config/fir/auth.json and ~/.claude/.credentials.json.

set -euo pipefail

RAW=false
VERBOSE=false
CACHED=false
CACHE_FILE="$HOME/.config/fir/anthropic-usage-cache.json"

find_token() {
  local entry file filter value
  local search_entries=(
    "$HOME/.config/fir/auth.json:.anthropic.access // empty"
    "$HOME/.claude/.credentials.json:.claudeAiOauthToken // .access_token // empty"
  )

  for entry in "${search_entries[@]}"; do
    file="${entry%%:*}"
    filter="${entry#*:}"
    if [[ -f "$file" ]]; then
      if value=$(jq -r "$filter" "$file" 2>/dev/null); then
        if [[ -n "$value" && "$value" != "null" ]]; then
          printf "%s" "$value"
          return 0
        fi
      fi
    fi
  done

  return 1
}

POSITIONAL=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --raw)     RAW=true; shift ;;
    --verbose) VERBOSE=true; shift ;;
    --cached)  CACHED=true; shift ;;
    *)         POSITIONAL+=("$1"); shift ;;
  esac
done

TOKEN="${POSITIONAL[0]:-${TOKEN:-}}"

if [[ -z "$TOKEN" ]]; then
  if token="$(find_token)"; then
    TOKEN="$token"
  fi
fi

# --- Obtain BODY: either from cache or live API ---

BODY=""

if $CACHED && [[ -f "$CACHE_FILE" ]]; then
  cached_data=$(jq -c '.data // empty' "$CACHE_FILE" 2>/dev/null || true)
  if [[ -n "$cached_data" && "$cached_data" != "null" ]]; then
    BODY="$cached_data"
  fi
fi

if [[ -z "$BODY" ]]; then
  # Need a token for the live API call
  if [[ -z "$TOKEN" ]]; then
    echo "Error: no token provided and no cached data available." >&2
    echo "Usage: TOKEN=<bearer-token> $0" >&2
    echo "       $0 <bearer-token>" >&2
    exit 1
  fi

  RESPONSE=$(curl -s -w '\n%{http_code}' \
    -H "Authorization: Bearer $TOKEN" \
    -H "anthropic-beta: oauth-2025-04-20" \
    -H "Accept: application/json" \
    https://api.anthropic.com/api/oauth/usage)

  HTTP_CODE=$(tail -1 <<< "$RESPONSE")
  BODY=$(sed '$ d' <<< "$RESPONSE")

  if [[ "$HTTP_CODE" -lt 200 || "$HTTP_CODE" -ge 300 ]]; then
    echo "Error: API returned HTTP $HTTP_CODE" >&2
    if $VERBOSE; then
      echo "$BODY" >&2
    else
      msg=$(echo "$BODY" | jq -r '.error.message // .error // .message // empty' 2>/dev/null || true)
      if [[ -n "$msg" ]]; then
        echo "$msg" >&2
      else
        echo "(use --verbose to see the full response)" >&2
      fi
    fi
    exit 1
  fi
fi

# --- Output ---

if $RAW; then
  echo "$BODY" | python3 -m json.tool 2>/dev/null || echo "$BODY"
  exit 0
fi

echo "$BODY" | python3 -c "
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
