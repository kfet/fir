---
name: spawn-agent
description: Spawn a dedicated fir session for a new Poe conversation. Runs when a channel notification arrives with meta type "spawn".
---

# spawn-agent

A channel notification with `meta.type == "spawn"` means the bridge
claimed a new conversation. Launch a dedicated fir agent for it.

Run the bundled script with the conversation_id and mcp_config from
the notification meta:

```bash
spawn-poe-agent '<conversation_id>' '<mcp_config JSON from meta>'
```

The script creates the agent directory, writes mcp.json, and launches
fir in a tmux session. The new fir receives the user's message
automatically via its bridge.
