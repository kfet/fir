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

### 2. Build fir and generate MCP config

```bash
cd "$(git rev-parse --show-toplevel)" && go build -o /tmp/fir-test ./cmd/fir/
mkdir -p /tmp/fir-fakechat-test
cat > /tmp/fir-fakechat-mcp.json << EOF
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
  "cd /tmp/fir-fakechat-test && FIR_DEBUG=1 /tmp/fir-test --debug --mcp-config /tmp/fir-fakechat-mcp.json 2>/tmp/fakechat-stderr.log"
```

### 4. Verify connection and open browser

```bash
sleep 4 && grep 'mcp connected.*fakechat' ~/.fir/agent/debug.log
open http://localhost:8787
```

Should show `"tools":2`. The browser UI is now open — type messages and the agent auto-responds.

### 5. Monitor continuously

Immediately after opening the browser, begin a monitoring loop. Do **not** ask the user whether to monitor — just start. Poll every 10 seconds until the user says to stop or clean up:

```bash
# Check logs for channel activity
grep -i 'channel\|inject\|reply\|error\|warn\|fail' ~/.fir/agent/debug.log | tail -20
```

```bash
# Check TUI state
tmux capture-pane -t fakechat -p -S -40
```

On each poll, report:
- New channel messages received (with timestamps)
- Tool calls made (reply, etc.)
- Any errors or warnings
- Whether auto-trigger fired (agent started a turn without manual prompt)

Keep monitoring until the user explicitly asks to stop or clean up.

### 6. Cleanup (when user says done)

```bash
tmux kill-session -t fakechat 2>/dev/null
lsof -ti:8787 | xargs kill -9 2>/dev/null; true
```

## What to look for

- **Auto-trigger**: agent starts a turn without manual TUI prompt
- **Reply tool**: agent uses `mcp__fakechat__reply` to respond
- **No phantom tools**: no `list_resources` or `list_prompts` errors
- **Browser UI**: responses appear in the fakechat web UI
- **Latency**: time between message received and reply sent
- **Errors**: any warnings or failures in debug log
