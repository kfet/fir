#!/usr/bin/env bash
# notify.sh — send a native terminal notification
#
# Usage: ./notify.sh TITLE [BODY]
#
# Detects tmux/kitty and uses the appropriate escape sequence.
# Writes to /dev/tty so it works even without a TTY on stdout.

set -euo pipefail

TITLE="${1:?usage: notify.sh TITLE [BODY]}"
BODY="${2:-}"

if [ -n "${KITTY_WINDOW_ID:-}" ]; then
  printf '\e]99;i=1:d=0;%s\e\\' "$TITLE" > /dev/tty
  [ -n "$BODY" ] && printf '\e]99;i=1:p=body;%s\e\\' "$BODY" > /dev/tty
elif [ -n "${TMUX:-}" ]; then
  printf '\ePtmux;\e\e]777;notify;%s;%s\e\e\\\e\\' "$TITLE" "$BODY" > /dev/tty
else
  printf '\e]777;notify;%s;%s\e\\' "$TITLE" "$BODY" > /dev/tty
fi
