---
name: spawn-agent
description: Spawn a dedicated fir session for a new Poe conversation. Runs when a channel notification arrives with meta type "spawn".
---

# spawn-agent

A channel notification with `meta.type == "spawn"` means the bridge
claimed a new conversation. Create a dedicated fir process for it.

## Steps

1. Read `conversation_id` and `mcp_config` from the notification meta.

2. Run this bash block (substitute values):

```bash
CONV="<conversation_id>"
DIR="$HOME/.local/state/fir/agents/$CONV"
mkdir -p "$DIR/.fir"
cat > "$DIR/.fir/mcp.json" << 'MCPEOF'
<mcp_config from meta, as JSON>
MCPEOF
tmux new-window -t agents "fir -c --session-name '$CONV' '$DIR'" 2>/dev/null || \
  tmux new-session -d -s agents "fir -c --session-name '$CONV' '$DIR'"
```

3. Done. The new fir receives the user's message automatically via its
   bridge and replies on its own. Move on and wait for the next spawn.
