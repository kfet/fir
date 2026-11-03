#!/usr/bin/env bash
# reap-idle.sh — close tmux sessions whose panes are all idle shells.
#
# Usage: reap-idle.sh [--reap]
#   (default is a dry run: it prints what it would do and changes nothing)
#
# Older spawn.sh versions ended each agent's command with `exec $SHELL`, so a
# finished agent left its session behind as an idle shell; tmux-resurrect
# also brings such sessions back as fresh shells. This closes them.
#
# A session is closed only when EVERY pane in it is idle:
#   - the pane is dead, or
#   - its process is a shell (bash, zsh, sh, fish, ...) with no child
#     processes — nothing is running in it.
# One busy pane keeps the whole session.
#
# NEVER closed: an attached session (someone is looking at it), and the
# session this script runs in.
#
# Output, one line per session:
#   REAP <session>   all panes idle (closed with --reap)
#   KEEP <session>   <reason>

set -euo pipefail

REAP=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --reap) REAP=1; shift ;;
    -h|--help) sed -n '2,22p' "$0"; exit 0 ;;
    *) echo "reap-idle.sh: unknown argument: $1" >&2; exit 2 ;;
  esac
done

if ! tmux list-sessions >/dev/null 2>&1; then
  echo "reap-idle.sh: no tmux server; nothing to do"
  exit 0
fi

SELF=""
if [[ -n "${TMUX_PANE:-}" ]]; then
  SELF="$(tmux display-message -p -t "$TMUX_PANE" '#{session_id}' 2>/dev/null || true)"
fi

is_shell() {
  case "${1#-}" in
    bash|zsh|sh|dash|fish|ksh|mksh|tcsh|csh|nu|elvish) return 0 ;;
  esac
  return 1
}

# pane_idle <dead> <pid> — true if the pane is dead or an idle shell.
pane_idle() {
  local dead="$1" pid="$2" comm
  [[ "$dead" == 1 ]] && return 0
  comm="$(ps -o comm= -p "$pid" 2>/dev/null || true)"
  comm="${comm##*/}"
  is_shell "$comm" || return 1
  [[ -z "$(pgrep -P "$pid" 2>/dev/null || true)" ]]
}

reaped=0
while IFS=$'\t' read -r sid name attached; do
  if [[ "$sid" == "$SELF" ]]; then
    echo "KEEP $name   (this session)"; continue
  fi
  if [[ "$attached" != 0 ]]; then
    echo "KEEP $name   (attached)"; continue
  fi
  busy=""
  while IFS=$'\t' read -r dead pid; do
    if ! pane_idle "$dead" "$pid"; then busy=1; break; fi
  done < <(tmux list-panes -s -t "$sid" -F '#{pane_dead}	#{pane_pid}')
  if [[ -n "$busy" ]]; then
    echo "KEEP $name   (a pane is running something)"; continue
  fi
  echo "REAP $name"
  if [[ $REAP == 1 ]]; then
    # Re-check attachment right before killing: someone may have attached
    # since the listing.
    if [[ "$(tmux display-message -p -t "$sid" '#{session_attached}' 2>/dev/null || echo 1)" == 0 ]]; then
      tmux kill-session -t "$sid" && reaped=$((reaped + 1))
    else
      echo "KEEP $name   (attached just now)"
    fi
  fi
done < <(tmux list-sessions -F '#{session_id}	#{session_name}	#{session_attached}')

if [[ $REAP == 1 ]]; then
  echo "reaped $reaped session(s)"
else
  echo "dry run; pass --reap to close the REAP sessions"
fi
