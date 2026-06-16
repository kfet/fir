#!/usr/bin/env bash
# usage.sh — fetch and display Anthropic API usage stats
#
# Usage:
#   ./usage.sh                      # auto-detects ALL accounts from known credential files
#   ./usage.sh <bearer-token>       # explicit token as argument (single account)
#   TOKEN=<bearer-token> ./usage.sh # explicit token via env var (single account)
#
# Options:
#   --raw         Print the full JSON response instead of the formatted summary
#   --verbose     On error, print the full response body for debugging
#   --cached      For the default fir account, read from the local cache
#                 (~/.config/fir/anthropic-usage-cache.json) written by the
#                 provider-usage extension instead of hitting the API. Additional
#                 (non-default) accounts always use a live API call, since the
#                 cache only tracks the default account. Falls back to a live
#                 call if the cache is missing.
#
# The token must be an OAuth Bearer token (not a standard sk-ant-... API key).
#
# Multiple accounts:
#   fir stores extra OAuth accounts under composite slot keys in auth.json:
#   the bare `anthropic` key is the default account, and additional accounts
#   use `anthropic#<accountId>` keys. With no explicit token, this script
#   reports usage for EVERY stored Anthropic account, one labelled section each.

set -euo pipefail

RAW=false
VERBOSE=false
CACHED=false
CACHE_FILE="$HOME/.config/fir/anthropic-usage-cache.json"
FIR_AUTH="${FIR_AUTH_FILE:-$HOME/.config/fir/auth.json}"
CLAUDE_CREDS="$HOME/.claude/.credentials.json"

POSITIONAL=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --raw)     RAW=true; shift ;;
    --verbose) VERBOSE=true; shift ;;
    --cached)  CACHED=true; shift ;;
    *)         POSITIONAL+=("$1"); shift ;;
  esac
done

EXPLICIT_TOKEN="${POSITIONAL[0]:-${TOKEN:-}}"

# --- Account enumeration ---
# Each account is emitted as a TSV line: slot <TAB> label <TAB> token
# `slot` is the auth.json key ("anthropic" is the default); used to decide
# whether the cache applies. `label` is a human-readable display name.

emit_accounts() {
  # 1. Explicit token wins — single anonymous account.
  if [[ -n "$EXPLICIT_TOKEN" ]]; then
    printf 'provided\tProvided token\t%s\n' "$EXPLICIT_TOKEN"
    return 0
  fi

  local found=false

  # 2. All Anthropic accounts stored by fir (default + #account slots).
  if [[ -f "$FIR_AUTH" ]]; then
    local lines
    lines=$(jq -r '
      to_entries
      | map(select(.key == "anthropic" or (.key | startswith("anthropic#"))))
      | map(select((.value.type // "") == "oauth" and (.value.access // "") != ""))
      | sort_by(if .key == "anthropic" then "" else .key end)
      | .[]
      | [
          .key,
          (.value.label // .value.extra.label // .value.extra.email // .key),
          .value.access
        ]
      | @tsv
    ' "$FIR_AUTH" 2>/dev/null || true)
    if [[ -n "$lines" ]]; then
      printf '%s\n' "$lines"
      found=true
    fi
  fi

  # 3. Claude Code credentials — only if fir had no Anthropic accounts.
  if ! $found && [[ -f "$CLAUDE_CREDS" ]]; then
    local cc
    cc=$(jq -r '.claudeAiOauthToken // .access_token // empty' "$CLAUDE_CREDS" 2>/dev/null || true)
    if [[ -n "$cc" && "$cc" != "null" ]]; then
      printf 'claude-code\tClaude Code\t%s\n' "$cc"
      found=true
    fi
  fi

  $found
}

# --- Fetch one account's usage JSON into BODY (live API) ---

fetch_live() {
  local token="$1"
  local response http_code body
  response=$(curl -s -w '\n%{http_code}' \
    -H "Authorization: Bearer $token" \
    -H "anthropic-beta: oauth-2025-04-20" \
    -H "Accept: application/json" \
    https://api.anthropic.com/api/oauth/usage)
  http_code=$(tail -1 <<< "$response")
  body=$(sed '$ d' <<< "$response")

  if [[ "$http_code" -lt 200 || "$http_code" -ge 300 ]]; then
    echo "  Error: API returned HTTP $http_code" >&2
    if $VERBOSE; then
      echo "$body" >&2
    else
      local msg
      msg=$(echo "$body" | jq -r '.error.message // .error // .message // empty' 2>/dev/null || true)
      [[ -n "$msg" ]] && echo "  $msg" >&2 || echo "  (use --verbose to see the full response)" >&2
    fi
    return 1
  fi
  printf '%s' "$body"
}

# --- Read cached usage JSON into BODY (default account only) ---

fetch_cached() {
  if [[ -f "$CACHE_FILE" ]]; then
    local cached_data
    cached_data=$(jq -c '.data // empty' "$CACHE_FILE" 2>/dev/null || true)
    if [[ -n "$cached_data" && "$cached_data" != "null" ]]; then
      printf '%s' "$cached_data"
      return 0
    fi
  fi
  return 1
}

# --- Pretty-print one usage body ---

render() {
  echo "$1" | python3 -c "
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

windows = {k: v for k, v in data.items() if k != 'extra_usage' and isinstance(v, dict) and 'utilization' in v}
label_width = max([len(fmt_key(k)) for k in windows] + [11]) if windows else 11
if windows:
    for key, w in windows.items():
        label = fmt_key(key).ljust(label_width)
        resets = fmt_reset(w.get('resets_at'))
        print(f\"  {label}  {w['utilization']:.0f}% — resets {resets}\")
else:
    print('  (no active usage windows reported)')

eu = data.get('extra_usage')
if eu and eu.get('is_enabled'):
    used = eu['used_credits'] / 100
    cap  = eu['monthly_limit'] / 100
    pct  = eu['utilization']
    print(f\"  {'Extra Usage'.ljust(label_width)}  \${used:.2f} / \${cap:.2f} monthly cap ({pct:.0f}%) — overage billing active\")
"
}

# --- Main ---

if ! ACCOUNTS=$(emit_accounts); then
  echo "Error: no Anthropic token found." >&2
  echo "  Looked in: $FIR_AUTH (anthropic / anthropic#* slots), $CLAUDE_CREDS" >&2
  echo "  Or pass one explicitly: $0 <bearer-token>" >&2
  exit 1
fi

# Count accounts to decide whether to print section headers.
N=$(printf '%s\n' "$ACCOUNTS" | grep -c . || true)
EXIT=0
FIRST=true

while IFS=$'\t' read -r slot label token; do
  [[ -z "$slot" ]] && continue

  # Section header when more than one account.
  if [[ "$N" -gt 1 ]]; then
    $FIRST || echo
    echo "▸ $label"
  fi
  FIRST=false

  BODY=""
  # Cache only applies to the default fir account ("anthropic" slot).
  if $CACHED && [[ "$slot" == "anthropic" ]]; then
    BODY=$(fetch_cached || true)
  fi
  if [[ -z "$BODY" ]]; then
    if ! BODY=$(fetch_live "$token"); then
      EXIT=1
      continue
    fi
  fi

  if $RAW; then
    echo "$BODY" | python3 -m json.tool 2>/dev/null || echo "$BODY"
  else
    render "$BODY"
  fi
done <<< "$ACCOUNTS"

exit $EXIT
