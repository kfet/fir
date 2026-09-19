#!/usr/bin/env bash
# gc.sh — reap tmux windows left behind by finished wt agents.
#
# Usage: gc.sh [--reap] [--idle-days N]
#   (default is a dry run: it prints what it would do and changes nothing)
#
# A wt agent lives in a tmux window on its own worktree. When it finishes —
# ship-it merges and removes the worktree — the window stays behind holding a
# dead shell in a directory that no longer exists. Those accumulate.
#
# Everything needed to classify a window is already in tmux. No bookkeeping.
#
#   REAP    no agent, worktree gone     -> the agent finished, kill the window
#   REAP    no agent, idle > N days     -> leftover prompt, kill the window
#   REPORT  agent alive, worktree gone  -> a bug; a human must look
#   REPORT  agent alive, idle > N days  -> maybe stuck; never auto-kill
#
# A live agent is NEVER killed automatically. The caller's own window is
# always skipped.
#
# "Is an agent running here" is answered from the pane's process tree, NOT
# from #{pane_current_command}. tmux reports the pane's immediate command,
# and spawn.sh starts the agent as `fir ...; exec $SHELL` — so a pane with a
# perfectly live agent reports `zsh`. Trusting that field would let this
# script kill running agents. A window is judged by ALL of its panes: one
# live agent in any pane protects the whole window.

set -euo pipefail

REAP=0
IDLE_DAYS=3

while [[ $# -gt 0 ]]; do
  case "$1" in
    --reap) REAP=1; shift ;;
    --idle-days) IDLE_DAYS="${2:-}"; shift 2 ;;
    -h|--help) sed -n '2,26p' "$0"; exit 0 ;;
    *) echo "gc.sh: unknown argument: $1" >&2; exit 2 ;;
  esac
done

if ! [[ "$IDLE_DAYS" =~ ^[0-9]+$ ]]; then
  echo "gc.sh: --idle-days needs a whole number of days" >&2
  exit 2
fi

# agent_running <pid> — true if pid or any descendant looks like an agent.
agent_running() {
  local pid="$1" kid
  case "$(ps -o comm= -p "$pid" 2>/dev/null)" in
    fir|claude|codex|*-acp) return 0 ;;
  esac
  for kid in $(pgrep -P "$pid" 2>/dev/null); do
    agent_running "$kid" && return 0
  done
  return 1
}

if ! tmux has-session 2>/dev/null; then
  echo "gc: no tmux server; nothing to do"
  exit 0
fi

# The window this script runs in. Never touch it.
SELF_WIN=""
if [[ -n "${TMUX_PANE:-}" ]]; then
  SELF_WIN="$(tmux display-message -p -t "$TMUX_PANE" '#{window_id}' 2>/dev/null || true)"
fi

NOW="$(date +%s)"
IDLE_SECS=$(( IDLE_DAYS * 86400 ))

declare -A W_NAME W_ACT W_PATH W_LIVE W_GONE

# One row per pane. The path is last: it is the only field that can hold tabs
# or spaces safely at the end of the line.
while IFS=$'\t' read -r win_id name pane_pid activity path; do
  [[ -n "$win_id" ]] || continue
  [[ "$win_id" == "$SELF_WIN" ]] && continue

  # tmux appends " (deleted)" when the pane's cwd no longer exists.
  gone=0
  case "$path" in
    *" (deleted)") gone=1; path="${path% (deleted)}" ;;
  esac
  [[ -d "$path" ]] || gone=1

  live=0
  agent_running "$pane_pid" && live=1

  # First pane seen defines the window's name, activity and path.
  if [[ -z "${W_NAME[$win_id]:-}" ]]; then
    W_NAME[$win_id]="$name"
    W_ACT[$win_id]="$activity"
    W_PATH[$win_id]="$path"
    W_LIVE[$win_id]=0
    W_GONE[$win_id]=1
  fi
  (( live )) && W_LIVE[$win_id]=1   # any live pane protects the window
  (( gone )) || W_GONE[$win_id]=0   # any surviving cwd keeps it
done < <(tmux list-panes -a -F \
  '#{window_id}	#{window_name}	#{pane_pid}	#{window_activity}	#{pane_current_path}' 2>/dev/null)

reaped=0
kept=0

for win_id in "${!W_NAME[@]}"; do
  name="${W_NAME[$win_id]}"
  path="${W_PATH[$win_id]}"
  idle=$(( NOW - W_ACT[$win_id] ))
  age="$(( idle / 86400 ))d"
  live="${W_LIVE[$win_id]}"
  gone="${W_GONE[$win_id]}"

  if (( ! live )); then
    if (( gone )); then
      reason="worktree gone"
    elif (( idle > IDLE_SECS )); then
      reason="idle ${age}"
    else
      kept=$(( kept + 1 )); continue
    fi
    if (( REAP )); then
      tmux kill-window -t "$win_id" 2>/dev/null || true
      echo "REAPED  $name ($reason)"
    else
      echo "WOULD REAP  $name ($reason)  $path"
    fi
    reaped=$(( reaped + 1 ))
    continue
  fi

  # A live agent. Report only.
  if (( gone )); then
    echo "REPORT  $name — an agent is alive but its worktree is gone: $path"
  elif (( idle > IDLE_SECS )); then
    echo "REPORT  $name — an agent is alive, no activity for ${age}: $path"
  fi
  kept=$(( kept + 1 ))
done

if (( REAP )); then
  echo "gc: reaped ${reaped} window(s), kept ${kept}"
else
  echo "gc: ${reaped} window(s) to reap, ${kept} kept — rerun with --reap to act"
fi
