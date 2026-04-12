---
name: spawn-agent
description: Spawn a new fir session for a channel conversation. Triggered by channel notifications with meta type "spawn".
---

# spawn-agent

When you receive a channel message with `type: "spawn"` in the meta,
spawn a new fir session by running bash commands. Do NOT reply to the
user yourself.

## Steps

Extract `conversation_id` and `mcp_config` from the notification meta,
then run:

```bash
CONV_ID="<conversation_id>"
AGENT_DIR="$HOME/.local/state/fir/agents/$CONV_ID"
mkdir -p "$AGENT_DIR/.fir"

cat > "$AGENT_DIR/.fir/mcp.json" << 'MCPEOF'
<mcp_config JSON from meta>
MCPEOF

tmux new-window -t agents \
  "fir -c --session-name '$CONV_ID' '$AGENT_DIR'" 2>/dev/null || \
tmux new-session -d -s agents \
  "fir -c --session-name '$CONV_ID' '$AGENT_DIR'"
```

That's it. Do NOT pass a system prompt or first message to fir.
The new fir will receive the user's message automatically via its
channel bridge and reply on its own.
