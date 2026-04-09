---
name: remote-update
description: Self-update fir to the latest version and reexec, triggered remotely over a channel (Telegram, etc). Use when the user asks to update/upgrade fir remotely, self-update over chat, reexec from Telegram, or hot-swap the binary without terminal access.
---

# Remote self-update over a channel

Update the running fir binary and reexec into it — all triggered from a channel message (Telegram, Discord, etc.) without terminal access.

## Prerequisites

- fir session is running inside **tmux** (required for reexec to work — the pane survives the exec).
- The channel MCP (e.g. telegram) is connected and the user is allowlisted.
- `fir update` works on the host (network access to GitHub releases).

## Steps

### 1. Identify the tmux pane

Find the tmux session and window this fir process is running in:

```bash
# Get fir's PID, then find its tmux pane
tmux list-panes -a -F "#{pane_pid} #{session_name}:#{window_name}"
```

Match against fir's PID (parent of the bash tool process — check `ps -o pid,ppid,comm -p $$ $PPID`).

### 2. Run `fir update`

```bash
fir update
```

Confirm the output shows a successful update (e.g. `0.27.0 → 0.28.0`). If already on latest, tell the user and stop.

### 3. Notify the user

Send a channel reply: "Reexec'ing now — back in ~15 seconds."

### 4. Schedule a post-reexec notification

Before reexec, background a script that will tell the new fir session to notify the user once it's back:

```bash
nohup bash -c '
  sleep 20
  tmux send-keys -t "<session>:<window>" \
    "Send a channel reply to chat_id <id> saying: reexec complete, running fir $(fir --version 2>&1)" Enter
' >/tmp/reexec-notify.log 2>&1 &
```

This survives the exec because it's a detached background process. The 20-second delay gives fir time to restart and re-read the session transcript.

### 5. Reexec

Send `/reexec` to the tmux pane:

```bash
tmux send-keys -t "<session>:<window>" "/reexec" Enter
```

The current fir process execs into the new binary. The session transcript persists on disk. The new binary loads it, reconnects MCP servers, and is ready to receive messages.

### 6. Post-reexec (handled automatically)

The background script from step 4 fires after ~20 seconds, prompting the new fir session to send a confirmation message via the channel.

## How it works

- **tmux keeps the pane alive** across exec — the PID stays the same (exec replaces the process image in-place) and the pane persists.
- **Session transcript is on disk** (jsonl) — new binary loads full history, so the model has complete context.
- **MCP servers restart** automatically — the channel poller reconnects within seconds. Telegram queues messages during the gap.
- **The background notifier** is a separate process (nohup + &), so it survives the exec. It types a prompt into the tmux pane, which the new fir session processes normally.

## Gotchas

- **Not in tmux?** Reexec still works (fir's /reexec uses exec()), but you can't send-keys to yourself. Alternative: use `kill -USR1 <fir_pid>` if fir supports signal-triggered reexec, or just restart the process.
- **Multiple fir sessions?** Make sure you target the correct tmux pane. The wrong pane gets a stray `/reexec`.
- **Background script timing:** 20 seconds is conservative. On slow hardware (RPi), increase to 30-40s. On fast machines, 10s is fine.
- **Channel reply after reexec:** The new session doesn't automatically know it just reexec'd. The background script's prompt is what triggers the notification — without it, the user gets silence until they send a new message.
- **fir update fails?** Check network, GitHub rate limits, or permissions. Don't reexec if the update didn't succeed.
