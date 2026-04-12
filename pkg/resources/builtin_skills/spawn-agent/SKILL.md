---
name: spawn-agent
description: Spawn a new fir session for a channel conversation. Triggered by channel notifications with meta type "spawn". Creates a dedicated fir process via tmux.
---

# spawn-agent

When you receive a channel message with `type: "spawn"` in the meta,
you MUST spawn a new dedicated fir session for that conversation.
Do NOT try to handle the conversation yourself — you are the catch-all
agent and must delegate to a new process.

## What you receive

A channel notification like:

```
[Channel message … type="spawn" conversation_id="c-abc123" …]
New conversation needs an agent.
User: u-xyz
First message: Hello!
```

The meta contains:
- `conversation_id` — the conversation to handle
- `mcp_config` — JSON object to write as the new agent's `.fir/mcp.json`
- `user_id` — who started the conversation

## What you MUST do

Run these bash commands exactly (substitute the conversation_id and mcp_config):

```bash
CONV_ID="<conversation_id from meta>"
AGENT_DIR="$HOME/.local/state/fir/agents/$CONV_ID"

mkdir -p "$AGENT_DIR/.fir"

cat > "$AGENT_DIR/.fir/mcp.json" << 'MCPEOF'
<mcp_config JSON from meta>
MCPEOF

tmux new-window -t agents "fir -c --session-name '$CONV_ID' '$AGENT_DIR'" 2>/dev/null || \
tmux new-session -d -s agents "fir -c --session-name '$CONV_ID' '$AGENT_DIR'"
```

## Important

- Always use the `mcp_config` from the notification meta — do not hardcode relay URLs.
- Each conversation gets its own directory and fir session.
- If the tmux session "agents" doesn't exist, create it.
- After spawning, do NOT reply to the conversation yourself.
- The new fir agent will connect to the relay and handle the conversation.
