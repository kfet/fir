---
name: fakechat-test
description: Set up fakechat MCP plugin for interactive browser-based channel testing
---

Set up the official fakechat MCP plugin so you can chat with fir through the browser UI.

# Build fir and generate MCP config

```bash
cd "$(git rev-parse --show-toplevel)" && make
MCP_CONFIG=$(mktemp /tmp/fir-fakechat-mcp.XXXXXX.json)
DEBUG_LOG=$(mktemp /tmp/fir-fakechat-debug.XXXXXX.log)
cat > "$MCP_CONFIG" << EOF
{
  "mcpServers": {
    "fakechat": {
      "command": "bunx",
      "args": ["--bun", "github:anthropics/claude-plugins-official/external_plugins/fakechat"]
    }
  }
}
EOF
echo "MCP config: $MCP_CONFIG"
echo "Debug log: $DEBUG_LOG"
```

# Kill stale processes and launch

```bash
lsof -ti:8787 | xargs kill -9 2>/dev/null; true
tmux kill-session -t fakechat 2>/dev/null; true
tmux new-session -d -s fakechat -x 140 -y 40 \
  "cd /tmp && FIR_DEBUG=1 FIR_DEBUG_FILE=$DEBUG_LOG $(git rev-parse --show-toplevel)/bin/fir --debug --mcp-config $MCP_CONFIG 2>/tmp/fakechat-stderr.log"
```

# Verify connection and open browser

```bash
sleep 4 && grep 'mcp connected.*fakechat' "$DEBUG_LOG"
open http://localhost:8787
```

Should show `"tools":2`. The browser UI is now open — type messages and the agent auto-responds.

# Monitor continuously

1. Check logs for channel activity
```bash
grep -i 'channel\|inject\|reply\|error\|warn\|fail' "$DEBUG_LOG" | tail -20
```

2. Check TUI state
```bash
tmux capture-pane -t fakechat -p -S -40
```

3. Report:
- New channel messages received (with timestamps)
- Tool calls made (reply, etc.)
- Any errors or warnings
- Whether auto-trigger fired (agent started a turn without manual prompt)

4. Sleep for 10s, then go back to step 1

Keep monitoring until the user explicitly asks to stop or clean up.

# Cleanup after user says you're done

```bash
tmux kill-session -t fakechat 2>/dev/null
lsof -ti:8787 | xargs kill -9 2>/dev/null; true
```
