#!/usr/bin/env bash
# usage.sh — fetch and display OpenAI Codex / ChatGPT subscription usage stats
#
# Usage:
#   ./usage.sh                      # auto-detects ALL accounts from known credential files
#   ./usage.sh <bearer-token>       # explicit token as argument (single account)
#   TOKEN=<bearer-token> ./usage.sh # explicit token via env var (single account)
#
# Options:
#   --raw         Print the full JSON response instead of the formatted summary
#   --verbose     On error, print the full response body for debugging
#
# The token must be a ChatGPT OAuth Bearer token (the one fir stores after
# `fir auth login openai-codex`), not an `sk-...` platform API key.
#
# Multiple accounts:
#   fir stores extra OAuth accounts under composite slot keys in auth.json:
#   the bare `openai-codex` key is the default account, and additional accounts
#   use `openai-codex#<accountId>` keys. With no explicit token, this script
#   reports usage for EVERY stored Codex account, one labelled section each.

set -euo pipefail

RAW=false
VERBOSE=false
FIR_AUTH="${FIR_AUTH_FILE:-$HOME/.config/fir/auth.json}"
CODEX_CREDS="${CODEX_AUTH_FILE:-$HOME/.codex/auth.json}"
USAGE_URL="https://chatgpt.com/backend-api/wham/usage"
# Any codex_cli_rs/<version> works; without a codex UA Cloudflare serves a 403.
CODEX_UA="codex_cli_rs/0.56.0"

POSITIONAL=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --raw)     RAW=true; shift ;;
    --verbose) VERBOSE=true; shift ;;
    *)         POSITIONAL+=("$1"); shift ;;
  esac
done

EXPLICIT_TOKEN="${POSITIONAL[0]:-${TOKEN:-}}"

# --- Account id from the access token ---
# The ChatGPT access token is a JWT whose payload carries the account id at
# `https://api.openai.com/auth`.chatgpt_account_id. Used when a stored account
# has no recorded accountId, and for explicitly provided tokens.

account_id_from_jwt() {
  local token="$1" payload
  payload=$(cut -d. -f2 <<< "$token" 2>/dev/null || true)
  [[ -z "$payload" || "$payload" == "$token" ]] && return 0
  # base64url → base64, then pad to a multiple of 4.
  payload=${payload//-/+}
  payload=${payload//_//}
  while (( ${#payload} % 4 )); do payload+="="; done
  printf '%s' "$payload" | base64 -d 2>/dev/null |
    jq -r '."https://api.openai.com/auth".chatgpt_account_id // empty' 2>/dev/null || true
}

# --- Account enumeration ---
# Each account is emitted as a TSV line: slot <TAB> label <TAB> token <TAB> accountId

emit_accounts() {
  # 1. Explicit token wins — single anonymous account.
  if [[ -n "$EXPLICIT_TOKEN" ]]; then
    printf 'provided\tProvided token\t%s\t%s\n' \
      "$EXPLICIT_TOKEN" "$(account_id_from_jwt "$EXPLICIT_TOKEN")"
    return 0
  fi

  local found=false

  # 2. All Codex accounts stored by fir (default + #account slots).
  if [[ -f "$FIR_AUTH" ]]; then
    local lines
    lines=$(jq -r '
      to_entries
      | map(select(.key == "openai-codex" or (.key | startswith("openai-codex#"))))
      | map(select((.value.type // "") == "oauth" and (.value.access // "") != ""))
      | sort_by(if .key == "openai-codex" then "" else .key end)
      | .[]
      | [
          .key,
          (.value.label // .value.extra.label // .value.extra.email // .key),
          .value.access,
          (.value.extra.accountId // "")
        ]
      | @tsv
    ' "$FIR_AUTH" 2>/dev/null || true)
    if [[ -n "$lines" ]]; then
      printf '%s\n' "$lines"
      found=true
    fi
  fi

  # 3. Codex CLI credentials — only if fir had no Codex accounts.
  if ! $found && [[ -f "$CODEX_CREDS" ]]; then
    local line
    line=$(jq -r '
      .tokens
      | select(type == "object")
      | select((.access_token // "") != "")
      | ["codex-cli", "Codex CLI", .access_token, (.account_id // "")]
      | @tsv
    ' "$CODEX_CREDS" 2>/dev/null || true)
    if [[ -n "$line" ]]; then
      printf '%s\n' "$line"
      found=true
    fi
  fi

  $found
}

# --- Fetch one account's usage JSON ---

fetch_live() {
  local token="$1" account="$2"
  local response http_code body
  response=$(curl -s -w '\n%{http_code}' \
    -H "Authorization: Bearer $token" \
    -H "chatgpt-account-id: $account" \
    -H "originator: codex_cli_rs" \
    -H "User-Agent: $CODEX_UA" \
    -H "Accept: application/json" \
    "$USAGE_URL")
  http_code=$(tail -1 <<< "$response")
  body=$(sed '$ d' <<< "$response")

  if [[ "$http_code" -lt 200 || "$http_code" -ge 300 ]]; then
    echo "  Error: API returned HTTP $http_code" >&2
    if $VERBOSE; then
      echo "$body" >&2
    else
      local msg
      msg=$(echo "$body" | jq -r '.error.message // .detail // .error // .message // empty' 2>/dev/null || true)
      [[ -n "$msg" ]] && echo "  $msg" >&2 || echo "  (use --verbose to see the full response)" >&2
    fi
    return 1
  fi
  printf '%s' "$body"
}

# --- Pretty-print one usage body ---

render() {
  echo "$1" | python3 -c "
import sys, json, datetime

data = json.load(sys.stdin)
local_tz = datetime.datetime.now().astimezone().tzinfo

def fmt_reset(ts, fallback_seconds=None):
    if ts is None and fallback_seconds is not None:
        ts = datetime.datetime.now(datetime.timezone.utc).timestamp() + fallback_seconds
    if ts is None:
        return '—'
    t = datetime.datetime.fromtimestamp(ts, local_tz)
    return t.strftime('%b %-d, %-I:%M %p %Z')

def window_label(seconds):
    # Name the window from its declared length rather than hardcoding which
    # slot ('primary'/'secondary') carries which period — that varies by plan.
    if not seconds:
        return 'Window'
    named = {3600: 'Hourly', 86400: 'Daily', 604800: 'Weekly', 2592000: 'Monthly'}
    if seconds in named:
        return named[seconds]
    if seconds % 86400 == 0:
        return '%d-Day' % (seconds // 86400)
    if seconds % 3600 == 0:
        return '%d-Hour' % (seconds // 3600)
    if seconds % 60 == 0:
        return '%d-Minute' % (seconds // 60)
    return '%d-Second' % seconds

def collect(obj, out, prefix=''):
    '''Flatten anything window-shaped into (label, window) pairs.'''
    if isinstance(obj, dict):
        if 'used_percent' in obj:
            out.append((prefix + window_label(obj.get('limit_window_seconds')), obj))
            return
        for v in obj.values():
            collect(v, out, prefix)
    elif isinstance(obj, list):
        for v in obj:
            collect(v, out, prefix)

rl = data.get('rate_limit') or {}
windows = []
for key in ('primary_window', 'secondary_window'):
    collect(rl.get(key), windows)
collect(data.get('additional_rate_limits'), windows, prefix='Extra ')
collect(data.get('code_review_rate_limit'), windows, prefix='Review ')

rows = []
plan = data.get('plan_type')
if plan:
    rows.append(('Plan', str(plan)))

for label, w in windows:
    pct = w.get('used_percent')
    pct = '—' if pct is None else '%.0f%%' % pct
    resets = fmt_reset(w.get('reset_at'), w.get('reset_after_seconds'))
    rows.append((label, '%s — resets %s' % (pct, resets)))

if not windows:
    rows.append(('Windows', '(none reported)'))

credits = data.get('credits') or {}
if credits.get('unlimited'):
    rows.append(('Credits', 'unlimited'))
elif credits.get('balance') is not None:
    rows.append(('Credits', '\$%.2f balance' % credits['balance']))
elif credits.get('has_credits'):
    rows.append(('Credits', 'available'))

flags = []
if rl.get('limit_reached'):
    kind = data.get('rate_limit_reached_type')
    flags.append('rate limit reached' + (' (%s)' % kind if kind else ''))
elif rl.get('allowed') is False:
    flags.append('requests not currently allowed')
if credits.get('overage_limit_reached'):
    flags.append('overage limit reached')
if (data.get('spend_control') or {}).get('reached'):
    flags.append('spend control reached')
if flags:
    rows.append(('Status', '; '.join(flags)))

width = max(len(r[0]) for r in rows)
for label, value in rows:
    print('  %s  %s' % (label.ljust(width), value))
"
}

# --- Main ---

if ! ACCOUNTS=$(emit_accounts); then
  echo "Error: no OpenAI Codex token found." >&2
  echo "  Looked in: $FIR_AUTH (openai-codex / openai-codex#* slots), $CODEX_CREDS" >&2
  echo "  Or pass one explicitly: $0 <bearer-token>" >&2
  exit 1
fi

# Count accounts to decide whether to print section headers.
N=$(printf '%s\n' "$ACCOUNTS" | grep -c . || true)
EXIT=0
FIRST=true

while IFS=$'\t' read -r slot label token account; do
  [[ -z "$slot" ]] && continue

  # Section header when more than one account.
  if [[ "$N" -gt 1 ]]; then
    $FIRST || echo
    echo "▸ $label"
  fi
  FIRST=false

  # The account id header is required; recover it from the token if the
  # credential file didn't record one.
  [[ -z "$account" ]] && account=$(account_id_from_jwt "$token")
  if [[ -z "$account" ]]; then
    echo "  Error: no account id for this token (not a ChatGPT OAuth JWT?)" >&2
    EXIT=1
    continue
  fi

  if ! BODY=$(fetch_live "$token" "$account"); then
    EXIT=1
    continue
  fi

  if $RAW; then
    echo "$BODY" | python3 -m json.tool 2>/dev/null || echo "$BODY"
  else
    render "$BODY"
  fi
done <<< "$ACCOUNTS"

exit $EXIT
