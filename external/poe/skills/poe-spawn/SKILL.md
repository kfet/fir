---
name: poe-spawn
description: Spawn a new fir session for a Poe conversation. Use when a pending conversation notification arrives from the relay and you want to create a dedicated fir process for it. Uses bash + tmux.
---

# poe-spawn

Spawn a dedicated fir session for a Poe conversation thread.

## When to use

When the relay sends a `pending` notification for a new conversation_id
that no agent is handling yet. Any connected fir can pick it up.

## Steps

1. **Pre-register** (optional, for low latency): call the `register` MCP
   tool with the conversation_id to claim it immediately while spawning.

2. **Create the conversation directory:**
   ```bash
   mkdir -p ~/fir-poe/convs/<conv_id>/.fir
   ```

3. **Write the MCP config:**
   ```bash
   cat > ~/fir-poe/convs/<conv_id>/.fir/mcp.json << 'EOF'
   {
     "mcpServers": {
       "poe": {
         "command": "poe-bridge",
         "args": ["--agent", "--relay", "ws://krpi2one:9090"],
         "env": { "POE_CONV_ID": "<conv_id>" }
       }
     }
   }
   EOF
   ```

4. **Spawn in tmux:**
   ```bash
   tmux new-window -t poe "fir --session poe-<conv_id> ~/fir-poe/convs/<conv_id>"
   ```

The new fir process starts, its poe-bridge agent connects to the relay
and registers for the conversation_id. If you pre-registered in step 1,
the new agent overrides your provisional registration (since you haven't
written a reply yet).

## Notes

- The relay URL should match your deployment (e.g. `ws://krpi2one:9090`
  for a local tailnet relay).
- Each spawned fir gets its own session, context window, and branch
  history — completely isolated from other conversations.
- Poe provides the full conversation history in each query, so a newly
  spawned fir already has context from previous messages in that chat.
- If the spawned fir dies, its registration expires and the conversation
  returns to the lobby. Any fir can re-spawn it.
