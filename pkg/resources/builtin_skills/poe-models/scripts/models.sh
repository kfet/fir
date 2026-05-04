#!/usr/bin/env bash
set -euo pipefail

# Poe model catalog query — fetches model metadata from
# https://api.poe.com/v1/models (OpenAI-compatible /models endpoint).
#
# The endpoint is public (no auth required), but if a Poe API key is
# available we send it as a Bearer token — some bots may expose extra
# fields to authenticated callers.

API_URL="https://api.poe.com/v1/models"

find_api_key() {
  if [[ -n "${POE_API_KEY:-}" ]]; then
    printf "%s" "$POE_API_KEY"; return 0
  fi
  local models_file="$HOME/.config/fir/models.json"
  if [[ -f "$models_file" ]]; then
    local key
    key=$(jq -r '.providers.Poe.apiKey // empty' "$models_file" 2>/dev/null)
    if [[ -n "$key" && "$key" != "null" ]]; then printf "%s" "$key"; return 0; fi
  fi
  local auth_file="$HOME/.config/fir/auth.json"
  if [[ -f "$auth_file" ]]; then
    local key
    key=$(jq -r '.poe.access // .poe.key // empty' "$auth_file" 2>/dev/null)
    if [[ -n "$key" && "$key" != "null" ]]; then printf "%s" "$key"; return 0; fi
  fi
  return 1
}

MODE="table"   # table | json | raw
FILTER=""      # case-insensitive substring filter on id/display_name
LIMIT=0        # 0 = no limit
SHOW_ID=""     # exact-id detail mode

while [[ $# -gt 0 ]]; do
  case "$1" in
    --json)   MODE="json"; shift ;;
    --raw)    MODE="raw"; shift ;;
    --filter) FILTER="$2"; shift 2 ;;
    --limit)  LIMIT="$2"; shift 2 ;;
    --id)     SHOW_ID="$2"; shift 2 ;;
    -h|--help)
      cat <<EOF
Usage: models.sh [options]

Options:
  --filter STR   Only show models whose id or display name contains STR (case-insensitive)
  --id ID        Show full JSON details for a single model by exact id
  --json         Emit a normalized JSON array of all matched models
  --raw          Emit the raw upstream JSON response
  --limit N      Cap the number of rows shown (table mode)
EOF
      exit 0 ;;
    *) echo "Unknown option: $1" >&2; exit 1 ;;
  esac
done

AUTH_HEADER=()
if key=$(find_api_key); then
  AUTH_HEADER=(-H "Authorization: Bearer $key")
fi

resp=$(curl -fsS "$API_URL" "${AUTH_HEADER[@]}")

if [[ "$MODE" == "raw" ]]; then
  echo "$resp" | jq .
  exit 0
fi

if [[ -n "$SHOW_ID" ]]; then
  echo "$resp" | jq --arg id "$SHOW_ID" '.data[] | select(.id == $id)'
  exit 0
fi

# Normalize each entry to a stable shape.
normalized=$(echo "$resp" | jq '
  [ .data[] | {
      id,
      display_name: (.metadata.display_name // .id),
      owned_by,
      description,
      input_modalities: (.architecture.input_modalities // []),
      supported_features: (.supported_features // []),
      supported_endpoints: (.supported_endpoints // []),
      context_length: (.context_window.context_length // .context_length // 0),
      max_output_tokens: (.context_window.max_output_tokens // 0),
      pricing: (.pricing // {}),
      reasoning: (.reasoning // {}),
      tools: ((.supported_features // []) | index("tools") != null),
      vision: ((.architecture.input_modalities // []) | index("image") != null)
  } ]')

if [[ -n "$FILTER" ]]; then
  normalized=$(echo "$normalized" | jq --arg f "$FILTER" '
    map(select(((.id // "") + " " + (.display_name // "")) | ascii_downcase | contains($f | ascii_downcase)))')
fi

count=$(echo "$normalized" | jq 'length')

if [[ "$MODE" == "json" ]]; then
  echo "$normalized"
  exit 0
fi

# Table mode
printf "\nPoe Models (%d match%s)\n" "$count" "$([[ "$count" == "1" ]] && echo "" || echo "es")"
printf "=================================\n\n"

if [[ "$count" == "0" ]]; then
  echo "No models matched."
  exit 0
fi

rows=$(echo "$normalized" | jq -r '
  sort_by(.id) | .[] |
  [ .id,
    (.context_length | if . >= 1000 then (./1000 | floor | tostring) + "k" else tostring end),
    (.pricing.prompt // "-"),
    (.pricing.completion // "-"),
    (if .tools then "Y" else "-" end),
    (if .vision then "Y" else "-" end),
    (if .reasoning.supports_reasoning_effort then "Y" else "-" end)
  ] | @tsv')

if [[ "$LIMIT" -gt 0 ]]; then
  rows=$(echo "$rows" | head -n "$LIMIT")
fi

{
  printf "ID\tCTX\tPROMPT\tCOMPLETION\tTOOLS\tVISION\tEFFORT\n"
  printf "%s\n" "$rows"
} | column -t -s $'\t'

echo
echo "Pricing values are USD per token as returned by Poe; multiply by 1e6 for per-1M-token cost."
echo "Use --id ID for full details on a single model, or --json for machine-readable output."
