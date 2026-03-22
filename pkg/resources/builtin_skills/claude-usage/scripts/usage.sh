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
#   --no-cache    Bypass the cache and always fetch from the API
#
# Environment:
#   USAGE_CACHE_TTL   Cache lifetime in seconds (default: 60)
#
# The token must be an OAuth Bearer token (not a standard sk-ant-... API key).
# Auto-detection searches ~/.fir/agent/auth.json and ~/.claude/.credentials.json.

set -euo pipefail

RAW=false
VERBOSE=false
NO_CACHE=false
CACHE_TTL="${USAGE_CACHE_TTL:-60}"
BACKOFF_BASE=60
BACKOFF_MAX=900
CACHE_DIR="$HOME/.fir/agent"
CACHE_FILE="$CACHE_DIR/anthropic-usage-cache.json"
LOCK_FILE="$CACHE_DIR/anthropic-usage-cache.lock"

find_token() {
  local entry file filter value
  local search_entries=(
    "$HOME/.fir/agent/auth.json:.anthropic.access // empty"
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
    --raw)      RAW=true; shift ;;
    --verbose)  VERBOSE=true; shift ;;
    --no-cache) NO_CACHE=true; shift ;;
    *)          POSITIONAL+=("$1"); shift ;;
  esac
done

TOKEN="${POSITIONAL[0]:-${TOKEN:-}}"

if [[ -z "$TOKEN" ]]; then
  if token="$(find_token)"; then
    TOKEN="$token"
  fi
fi

if [[ -z "$TOKEN" ]]; then
  echo "Error: no token provided." >&2
  echo "Usage: TOKEN=<bearer-token> $0" >&2
  echo "       $0 <bearer-token>" >&2
  exit 1
fi

# --- Cached fetch with flock ---

cache_is_fresh() {
  [[ -f "$CACHE_FILE" ]] || return 1
  local fetched_at now
  fetched_at=$(jq -r '.fetched_at // 0 | floor' "$CACHE_FILE" 2>/dev/null) || return 1
  now=$(date +%s)
  (( now - fetched_at < CACHE_TTL ))
}

cache_is_backed_off() {
  [[ -f "$CACHE_FILE" ]] || return 1
  local backoff_until now
  backoff_until=$(jq -r '.backoff_until // 0 | floor' "$CACHE_FILE" 2>/dev/null) || return 1
  now=$(date +%s)
  (( now < backoff_until ))
}

read_cache() {
  jq -c '.data' "$CACHE_FILE" 2>/dev/null
}

fetch_and_cache() {
  local response http_code body
  response=$(curl -s -w '\n%{http_code}' \
    -H "Authorization: Bearer $TOKEN" \
    -H "anthropic-beta: oauth-2025-04-20" \
    -H "Accept: application/json" \
    https://api.anthropic.com/api/oauth/usage)

  http_code=$(tail -1 <<< "$response")
  body=$(sed '$ d' <<< "$response")

  if [[ "$http_code" -eq 429 ]]; then
    # Write backoff: double previous duration (reset if expired), capped at BACKOFF_MAX
    local prev_duration backoff_until now duration
    now=$(date +%s)
    backoff_until=$(jq -r '.backoff_until // 0 | floor' "$CACHE_FILE" 2>/dev/null || echo 0)
    if (( now >= backoff_until )); then
      prev_duration=0  # backoff expired — start fresh
    else
      prev_duration=$(jq -r '.backoff_duration // 0 | floor' "$CACHE_FILE" 2>/dev/null || echo 0)
    fi
    duration=$(( prev_duration * 2 ))
    (( duration < BACKOFF_BASE )) && duration=$BACKOFF_BASE
    (( duration > BACKOFF_MAX )) && duration=$BACKOFF_MAX
    jq --argjson until "$(( now + duration ))" --argjson dur "$duration" \
      '. + {backoff_until: $until, backoff_duration: $dur}' "$CACHE_FILE" > "$CACHE_FILE.tmp" 2>/dev/null \
      && mv "$CACHE_FILE.tmp" "$CACHE_FILE"
  elif [[ "$http_code" -ge 200 && "$http_code" -lt 300 ]]; then
    # Cache successful responses, clear backoff
    local now
    now=$(date +%s)
    jq -n --argjson data "$body" --argjson ts "$now" \
      '{fetched_at: $ts, data: $data}' > "$CACHE_FILE.tmp" \
      && mv "$CACHE_FILE.tmp" "$CACHE_FILE"
  fi

  HTTP_CODE="$http_code"
  BODY="$body"
}

mkdir -p "$CACHE_DIR"

if ! $NO_CACHE && cache_is_backed_off; then
  # Backed off — use stale data if available, otherwise report the backoff
  cached=$(read_cache)
  if [[ -n "$cached" && "$cached" != "null" ]]; then
    BODY="$cached"
    HTTP_CODE=200
  else
    echo "Error: rate-limited, backing off" >&2
    exit 1
  fi
elif ! $NO_CACHE && cache_is_fresh; then
  BODY=$(read_cache)
  HTTP_CODE=200
else
  exec 9>"$LOCK_FILE"
  flock 9

  if ! $NO_CACHE && cache_is_backed_off; then
    cached=$(read_cache)
    if [[ -n "$cached" && "$cached" != "null" ]]; then
      BODY="$cached"
      HTTP_CODE=200
    else
      echo "Error: rate-limited, backing off" >&2
      exec 9>&-
      exit 1
    fi
  elif ! $NO_CACHE && cache_is_fresh; then
    BODY=$(read_cache)
    HTTP_CODE=200
  else
    fetch_and_cache
  fi

  exec 9>&-
fi

if [[ "$HTTP_CODE" -lt 200 || "$HTTP_CODE" -ge 300 ]]; then
  echo "Error: API returned HTTP $HTTP_CODE" >&2
  if $VERBOSE; then
    echo "$BODY" >&2
  else
    # Try to extract a short error message
    msg=$(echo "$BODY" | jq -r '.error.message // .error // .message // empty' 2>/dev/null || true)
    if [[ -n "$msg" ]]; then
      echo "$msg" >&2
    else
      echo "(use --verbose to see the full response)" >&2
    fi
  fi
  exit 1
fi

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
