#!/usr/bin/env bash
# spawn-poe-agent: create and launch a dedicated fir agent for a Poe conversation.
# Usage: spawn-poe-agent <conversation_id> <mcp_config_json>
set -euo pipefail

CONV="$1"
MCP_JSON="$2"
DIR="$HOME/.local/state/fir/agents/$CONV"

mkdir -p "$DIR/.fir"
printf '%s\n' "$MCP_JSON" > "$DIR/.fir/mcp.json"

# Launch in tmux "agents" session. Create session if it doesn't exist.
if tmux has-session -t agents 2>/dev/null; then
  tmux new-window -t agents -n "$CONV" "cd '$DIR' && fir -c --session-name '$CONV'"
else
  tmux new-session -d -s agents -n "$CONV" "cd '$DIR' && fir -c --session-name '$CONV'"
fi

echo "spawned agent for $CONV in $DIR"
