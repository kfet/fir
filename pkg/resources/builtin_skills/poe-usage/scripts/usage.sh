#!/usr/bin/env bash
set -euo pipefail

# Poe Usage API poller
# Resolves API key from: POE_API_KEY env → ~/.fir/agent/models.json → ~/.fir/agent/auth.json

API_BASE="https://api.poe.com/usage"

find_api_key() {
  # 1. Explicit env var
  if [[ -n "${POE_API_KEY:-}" ]]; then
    printf "%s" "$POE_API_KEY"
    return 0
  fi

  # 2. fir models config (~/.fir/agent/models.json) — custom provider "Poe"
  local models_file="$HOME/.fir/agent/models.json"
  if [[ -f "$models_file" ]]; then
    local key
    key=$(jq -r '.providers.Poe.apiKey // empty' "$models_file" 2>/dev/null)
    if [[ -n "$key" && "$key" != "null" ]]; then
      printf "%s" "$key"
      return 0
    fi
  fi

  # 3. fir auth storage (~/.fir/agent/auth.json) — "poe" provider with api_key type
  local auth_file="$HOME/.fir/agent/auth.json"
  if [[ -f "$auth_file" ]]; then
    local key
    key=$(jq -r '.poe.key // empty' "$auth_file" 2>/dev/null)
    if [[ -n "$key" && "$key" != "null" ]]; then
      printf "%s" "$key"
      return 0
    fi
  fi

  return 1
}

POE_API_KEY=$(find_api_key) || {
  echo "ERROR: No Poe API key found." >&2
  echo "" >&2
  echo "Set it via one of:" >&2
  echo "  1. POE_API_KEY environment variable" >&2
  echo "  2. fir models config: .providers.Poe.apiKey in ~/.fir/agent/models.json" >&2
  echo "  3. fir auth storage: add a 'poe' entry to ~/.fir/agent/auth.json" >&2
  echo "" >&2
  echo "Get your API key at https://poe.com/api/keys" >&2
  exit 1
}

# Defaults
MODE="both"  # balance, history, both
LIMIT=100
DAYS=30

# Parse arguments
while [[ $# -gt 0 ]]; do
  case "$1" in
    --balance)  MODE="balance"; shift ;;
    --history)  MODE="history"; shift ;;
    --limit)    LIMIT="$2"; shift 2 ;;
    --days)     DAYS="$2"; shift 2 ;;
    *)          echo "Unknown option: $1" >&2; exit 1 ;;
  esac
done

# Cutoff timestamp in microseconds
CUTOFF_US=$(( ($(date +%s) - DAYS * 86400) * 1000000 ))

fetch_balance() {
  local resp
  resp=$(curl -s -w "\n%{http_code}" "$API_BASE/current_balance" \
    -H "Authorization: Bearer $POE_API_KEY")

  local http_code body
  http_code=$(echo "$resp" | tail -1)
  body=$(echo "$resp" | sed '$d')

  if [[ "$http_code" != "200" ]]; then
    echo "ERROR: Balance API returned HTTP $http_code" >&2
    echo "$body" >&2
    return 1
  fi

  local balance
  balance=$(echo "$body" | jq -r '.current_point_balance // "unknown"')
  printf "\nPoe Point Balance\n=================\n"
  printf "Current balance: %s points\n" "$(printf '%s' "$balance" | sed ':a;s/\B[0-9]\{3\}\>/,&/;ta' 2>/dev/null || echo "$balance")"
}

fetch_history() {
  printf "\nUsage History (last %d days)\n" "$DAYS"
  printf "============================\n\n"

  local starting_after=""
  local all_entries="[]"
  local page=0

  while true; do
    local url="$API_BASE/points_history?limit=$LIMIT"
    if [[ -n "$starting_after" ]]; then
      url="$url&starting_after=$starting_after"
    fi

    local resp
    resp=$(curl -s -w "\n%{http_code}" "$url" \
      -H "Authorization: Bearer $POE_API_KEY")

    local http_code body
    http_code=$(echo "$resp" | tail -1)
    body=$(echo "$resp" | sed '$d')

    if [[ "$http_code" != "200" ]]; then
      echo "ERROR: Usage history API returned HTTP $http_code" >&2
      echo "$body" >&2
      return 1
    fi

    local entries has_more
    entries=$(echo "$body" | jq -c '.data // []')
    has_more=$(echo "$body" | jq -r '.has_more // false')

    local entry_count
    entry_count=$(echo "$entries" | jq 'length')
    if [[ "$entry_count" == "0" ]]; then
      break
    fi

    # Filter entries by cutoff (creation_time is in microseconds)
    local filtered
    filtered=$(echo "$entries" | jq -c --argjson cutoff "$CUTOFF_US" \
      '[.[] | select(.creation_time >= $cutoff)]')

    all_entries=$(echo "$all_entries $filtered" | jq -s 'add')

    local filtered_count
    filtered_count=$(echo "$filtered" | jq 'length')

    # Stop if we've gone past our date range or no more pages
    if [[ "$filtered_count" -lt "$entry_count" ]] || [[ "$has_more" != "true" ]]; then
      break
    fi

    # Get last query_id for pagination cursor
    starting_after=$(echo "$entries" | jq -r '.[-1].query_id // empty')
    if [[ -z "$starting_after" ]]; then
      break
    fi

    page=$((page + 1))

    # Safety: max 20 pages
    if [[ $page -ge 20 ]]; then
      echo "(Stopped after 20 pages of results)" >&2
      break
    fi
  done

  local count
  count=$(echo "$all_entries" | jq 'length')

  if [[ "$count" == "0" ]]; then
    echo "No usage entries found in the last $DAYS days."
    return 0
  fi

  # Aggregate by bot
  echo "$all_entries" | jq -r '
    group_by(.bot_name) |
    map({
      bot: .[0].bot_name,
      calls: length,
      points: (map(.cost_points // 0) | add),
      cost: (map((.cost_usd // "0") | tonumber) | add)
    }) |
    sort_by(-.points) |
    (["Bot", "Calls", "Points", "Cost ($)"] | @tsv),
    "────────────────────────────────────────────────────────",
    (.[] | [.bot, .calls, .points, (.cost * 100 | round / 100)] | @tsv),
    "────────────────────────────────────────────────────────",
    (. as $all |
      ["TOTAL",
       ($all | map(.calls) | add),
       ($all | map(.points) | add),
       ($all | map(.cost) | add * 100 | round / 100)
      ] | @tsv)
  ' | column -t -s $'\t' 2>/dev/null || echo "$all_entries" | jq -r '
    group_by(.bot_name) |
    map({bot: .[0].bot_name, calls: length, points: (map(.cost_points // 0) | add), cost: (map((.cost_usd // "0") | tonumber) | add)}) |
    sort_by(-.points) | .[] |
    "\(.bot)\t\(.calls)\t\(.points)\t$\(.cost)"
  '

  # Show usage type breakdown
  local types
  types=$(echo "$all_entries" | jq -r '[.[].usage_type] | group_by(.) | map({type: .[0], count: length}) | sort_by(-.count) | .[] | "  \(.type): \(.count)"')
  if [[ -n "$types" ]]; then
    printf "\nBy usage type:\n%s\n" "$types"
  fi
}

# Execute
case "$MODE" in
  balance) fetch_balance ;;
  history) fetch_history ;;
  both)    fetch_balance; echo; fetch_history ;;
esac
