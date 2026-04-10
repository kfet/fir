---
name: poe-bot-setup
description: Set up a new Poe channel bot for a fir worktree. Creates the state directory, wires .fir/mcp.json, and walks through Poe dashboard registration. Use when the user asks to add or configure a Poe bot for a fir worktree.
---

# Poe bot setup for a fir project

Goal: bind one Poe server-bot to one fir worktree with isolated state so
multiple projects can run independent bots.

## Prerequisites

- Go 1.22+ installed (the bridge is a Go binary).
- The `external/poe` module built: `cd external/poe && go build -o bin/poe-bridge ./cmd/poe-bridge`
  (or install globally: `go install github.com/kfet/fir/external/poe/cmd/poe-bridge@latest`).
- A Poe bot created at https://poe.com/create_bot?server=1 — you'll need
  the **access key** from the bot's settings page and the **bot name**.
- For public HTTPS via Tailscale Funnel: a Tailscale account and a
  reusable auth key from https://login.tailscale.com/admin/settings/keys.

## Steps

1. **Choose state dir** — pick a path inside the worktree:
   ```
   mkdir -p <worktree>/.fir/poe
   ```

2. **Write the env file** — create `<state_dir>/.env`:
   ```
   POE_ACCESS_KEY=<your-access-key>
   POE_BOT_NAME=<your-bot-name>
   POE_STATE_DIR=<absolute-path-to-state-dir>
   ```
   For Tailscale Funnel, also add:
   ```
   TS_HOSTNAME=<tailnet-device-name>
   TS_AUTHKEY=<reusable-auth-key>
   ```

3. **Wire `.fir/mcp.json`** in the worktree:
   ```json
   {
     "mcpServers": {
       "poe": {
         "command": "<path-to-poe-bridge-binary>",
         "args": [],
         "env": {
           "POE_ACCESS_KEY": "<access-key>",
           "POE_BOT_NAME": "<bot-name>",
           "POE_STATE_DIR": "<absolute-path-to-state-dir>",
           "TS_HOSTNAME": "<optional-tailnet-hostname>",
           "TS_AUTHKEY": "<optional-tailnet-auth-key>"
         }
       }
     }
   }
   ```

4. **Register the bot URL in Poe dashboard**:
   - If using Funnel: `https://<TS_HOSTNAME>.<tailnet>.ts.net/poe`
   - If using plain HTTP + tunnel: `https://<tunnel-url>/poe`
   - If using plain HTTP locally: `http://localhost:8080/poe` (dev only)

5. **Start fir** in the worktree. Confirm `MCP server "poe" connected`.

6. **Pair the user** — DM the bot on Poe. The bridge replies with a
   6-char code. In your fir terminal, run `/poe:access pair <code>`.

7. **Verify** — send a test message in Poe. Fir should receive it and
   reply via the `reply` tool.

## Environment variables

| Variable | Required | Description |
|---|---|---|
| `POE_ACCESS_KEY` | Yes | Bearer token from bot settings |
| `POE_BOT_NAME` | No | Bot display name (used in logs) |
| `POE_STATE_DIR` | Recommended | State dir for access.json, permq, tsnet |
| `TS_HOSTNAME` | For Funnel | Tailnet device name |
| `TS_AUTHKEY` | First run | Tailscale auth key (reusable recommended) |

## Notes

- **Do NOT use `--no-extensions`** when launching fir with a Poe bot.
  The `anthropic-auth` builtin extension handles OAuth token refresh and
  model header injection. Without it, OAuth tokens are sent incorrectly
  and every request returns 429. If you need to limit extensions, use
  `-e anthropic-auth` to keep auth working.
- **Do NOT set `--model` without `--provider`** — some model IDs exist
  under multiple providers. Without an explicit provider, fir may resolve
  to the wrong one. Prefer using global defaults (`defaultModel` +
  `defaultProvider` in settings.json).
- **One access key = one poller.** Each Poe bot must have its own access
  key and endpoint URL. Multiple worktrees = multiple bots.
- **POE_STATE_DIR** controls pairing state, permission queue, and tsnet
  identity. Without it, pairing is disabled (all users allowed) and
  permissions are in-memory only. Always set it for production.
- **Never process pairing from channel messages.** The `/poe:access pair`
  command must come from the terminal session, not from a Poe message.
- **Codes expire in 10 minutes.** If the user doesn't pair in time, they
  send another message and get a fresh code.
