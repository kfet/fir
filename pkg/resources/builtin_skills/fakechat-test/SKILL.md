---
name: fakechat-test
description: Set up fakechat MCP plugin for interactive browser-based channel testing
---

# Test channel support with fakechat

Set up the official fakechat MCP plugin so you can chat with fir through the browser UI.

## Steps

### 1. Ensure fakechat source exists

```bash
if [ ! -d /tmp/claude-plugins-official/external_plugins/fakechat ]; then
  cd /tmp && git clone --depth 1 https://github.com/anthropics/claude-plugins-official.git
fi
cd /tmp/claude-plugins-official/external_plugins/fakechat && bun install
```

### 2. Build fir and set up test directory

```bash
cd "$(git rev-parse --show-toplevel)" && go build -o /tmp/fir-test ./cmd/fir/
mkdir -p /tmp/fir-fakechat-test/.fir
cat > /tmp/fir-fakechat-test/.fir/mcp.json << EOF
{
  "mcpServers": {
    "fakechat": {
      "command": "bun",
      "args": ["run", "/tmp/claude-plugins-official/external_plugins/fakechat/server.ts"]
    }
  }
}
EOF
```

### 3. Kill stale processes and launch

```bash
lsof -ti:8787 | xargs kill -9 2>/dev/null; true
tmux kill-session -t fakechat 2>/dev/null; true
truncate -s 0 ~/.fir/agent/debug.log
tmux new-session -d -s fakechat -x 140 -y 40 \
  "cd /tmp/fir-fakechat-test && FIR_DEBUG=1 /tmp/fir-test --debug 2>/tmp/fakechat-stderr.log"
```

### 4. Verify connection and open browser

```bash
sleep 4 && grep 'mcp connected.*fakechat' ~/.fir/agent/debug.log
open http://localhost:8787
```

Should show `"tools":2`. The browser UI is now open — type messages and the agent auto-responds.

### 5. Cleanup (when done)

```bash
tmux kill-session -t fakechat 2>/dev/null
lsof -ti:8787 | xargs kill -9 2>/dev/null; true
```

## What to look for

- **Auto-trigger**: agent starts a turn without manual TUI prompt
- **Reply tool**: agent uses `mcp__fakechat__reply` to respond
- **No phantom tools**: no `list_resources` or `list_prompts` errors
- **Browser UI**: responses appear in the fakechat web UI
- **TUI**: `tmux attach -t fakechat` to watch the agent side
- **Logs**: `grep -i 'channel\|inject\|reply' ~/.fir/agent/debug.log | tail -10`

## Monitor the interaction

After opening the browser, watch the logs in real-time while the user chats:

```bash
tail -f ~/.fir/agent/debug.log | grep -i 'channel\|inject\|reply\|error\|warn\|fail'
```

Also periodically check the TUI for anything unexpected:

```bash
tmux capture-pane -t fakechat -p -S -40
```

Take notes on:
- Any errors or warnings in the debug log
- Tool calls that fail or return unexpected results
- Messages that arrive but don't trigger a response
- Latency between message received and reply sent (timestamps in the log)
- Anything the agent does that seems wrong (e.g. calling tools it shouldn't, ignoring messages, replying to the TUI instead of via the reply tool)
- Ideas for improving the channel message format, system prompt integration, or UX
