#!/usr/bin/env bash
# spawn-poe-agent: create and launch a dedicated fir agent for a Poe conversation.
# Usage: spawn-poe-agent <conversation_id> <mcp_config_json>
#
# All poe windows live in the "poe" tmux session:
#   - "relay"     : poe-bridge --relay
#   - "catch-all" : fir catch-all (claims new convs)
#   - <conv_id>   : dedicated agent per conversation
#
# Windows use remain-on-exit so crashed processes leave output visible.
# Deploy restarts them with: tmux respawn-window -k -t poe:<name>
set -euo pipefail

CONV="$1"
MCP_JSON="$2"
DIR="$HOME/.local/state/fir/agents/$CONV"
SESSION="poe"

mkdir -p "$DIR/.fir"
printf '%s\n' "$MCP_JSON" > "$DIR/.fir/mcp.json"

CMD="cd '$DIR' && exec fir -c --session-name '$CONV'"

if ! tmux has-session -t "$SESSION" 2>/dev/null; then
  tmux new-session -d -s "$SESSION" -n "$CONV" "$CMD"
elif tmux list-windows -t "$SESSION" -F '#{window_name}' | grep -qx "$CONV"; then
  # Window exists (possibly dead from a crash) — respawn it.
  tmux respawn-window -k -t "$SESSION:$CONV" "$CMD"
else
  tmux new-window -t "$SESSION" -n "$CONV" "$CMD"
fi

# Mark window as remain-on-exit so we can see output if it crashes.
tmux set-option -t "$SESSION:$CONV" remain-on-exit on 2>/dev/null || true

echo "spawned agent for $CONV in $DIR"
