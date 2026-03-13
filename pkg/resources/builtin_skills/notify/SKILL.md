---
builtin: true
name: notify
description: Send a native OS-level terminal notification to alert the user about long-running task completion or important events.
---

# Notify Skill

Send native OS-level terminal notifications to the user. Notifications appear even when the terminal window is not focused, making them ideal for alerting on long-running tasks.

## How to send a notification

Use the script in this skill's `scripts/` directory:

```bash
bash "$SKILL_DIR/scripts/notify.sh" "TITLE" "BODY"
```

Or use the `bash` tool to write escape sequences directly to `/dev/tty`. You **must** write to `/dev/tty` (not stdout or stderr) because the bash tool runs without a TTY attached.

**Always check `$TMUX` and `$KITTY_WINDOW_ID` first** — tmux requires DCS passthrough wrapping or the notification will be silently swallowed. Kitty handles notifications natively.

### Manual example

```bash
if [ -n "$KITTY_WINDOW_ID" ]; then
  printf '\e]99;i=1:d=0;%s\e\\' 'TITLE' > /dev/tty
  printf '\e]99;i=1:p=body;%s\e\\' 'BODY' > /dev/tty
elif [ -n "$TMUX" ]; then
  printf '\ePtmux;\e\e]777;notify;%s;%s\e\e\\\e\\' 'TITLE' 'BODY' > /dev/tty
else
  printf '\e]777;notify;%s;%s\e\\' 'TITLE' 'BODY' > /dev/tty
fi
```

## Terminal support

- **Ghostty, iTerm2, WezTerm, rxvt-unicode** — OSC 777
- **Kitty** — OSC 99
- **tmux** — DCS passthrough wrapping (handled by the snippets above)

## Notes

- Keep titles short (1–3 words)
- The body should summarize what happened and any key metrics
- Don't send notifications for trivial operations — only for things the user would want to be alerted about
