#!/usr/bin/env bash
# spawn.sh — create a worktree + branch and launch fir in its own detached tmux session.
#
# Usage: spawn.sh <feature-name> <task-text>
#   feature-name : short kebab-case identifier (used for branch, worktree, window)
#   task-text    : full prompt for the spawned fir agent, including "Mode: do." or "Mode: discuss."
#
# Must be run from inside the source repo (current dir = project root).
# On success prints the worktree path, branch, tmux session/window, and an
# attach hint.
#
# Each agent gets its OWN tmux session. `tmux new-window` with no -t (and an
# empty $TMUX, as in relay/ACP mode) lands in the most recently used session,
# which may belong to an unrelated tool — and dies with it when that tool runs
# `tmux kill-session`.
#
# WT_FIR_CMD overrides the agent command (default: fir); used by tests.

set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: spawn.sh <feature-name> <task-text>" >&2
  exit 2
fi

FEATURE="$1"
TASK_TEXT="$2"
BRANCH="work/${FEATURE}"
WORKTREE="${PWD}-wt-${FEATURE}"
FIR_CMD="${WT_FIR_CMD:-fir}"
# tmux rejects '.' and ':' in session names.
NAME="$(printf %s "$FEATURE" | tr '.:' '__')"

# '=' forces an exact match; without it has-session matches by prefix.
if tmux has-session -t "=$NAME" 2>/dev/null; then
  echo "spawn.sh: tmux session $NAME already exists; pick another feature name or kill it" >&2
  exit 1
fi

git worktree add "$WORKTREE" -b "$BRANCH" >/dev/null

TASK_FILE="$(mktemp -t firtask.XXXXXX)"
printf '%s\n' "$TASK_TEXT" > "$TASK_FILE"

# Capture the new window's id (tmux's stable `@N` handle) so verification is
# immune to spacing in the default list-windows output AND to cross-session
# name collisions (e.g. a stale window with the same FEATURE name in another
# session would otherwise produce a false-positive).
WIN_ID="$(tmux new-session -d -s "$NAME" -n "$FEATURE" -c "$WORKTREE" -P -F '#{window_id}' \
  "$FIR_CMD --session-name $FEATURE \"\$(cat $TASK_FILE)\"; rm -f $TASK_FILE; exec \$SHELL")"

# tmux returns success as soon as the shell launches; verify the window is
# still alive (fir could fail to start and the window die before our check).
# Short retry handles fresh-server edge cases.
found=
deadline=$((SECONDS + 5))
while (( SECONDS <= deadline )); do
  if tmux display-message -p -t "$WIN_ID" '' >/dev/null 2>&1; then
    found=1
    break
  fi
  sleep 0.1
done

if [[ -z "$found" ]]; then
  {
    echo "WINDOW MISSING — investigate (worktree at $WORKTREE)"
    echo "tmux list-windows -a output:"
    tmux list-windows -a 2>&1 || true
    echo "tmux list-sessions output:"
    tmux list-sessions 2>&1 || true
  } >&2
  exit 1
fi

echo "worktree: $WORKTREE"
echo "branch:   $BRANCH"
echo "session:  $NAME"
echo "window:   $FEATURE"
if [[ -n "${TMUX:-}" ]]; then
  echo "attach:   tmux switch-client -t $NAME"
else
  echo "attach:   tmux attach -t $NAME"
fi
