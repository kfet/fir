#!/usr/bin/env bash
# spawn.sh — create a worktree + branch and launch fir in a new tmux window.
#
# Usage: spawn.sh <feature-name> <task-text>
#   feature-name : short kebab-case identifier (used for branch, worktree, window)
#   task-text    : full prompt for the spawned fir agent, including "Mode: do." or "Mode: discuss."
#
# Must be run from inside the source repo (current dir = project root).
# On success prints the worktree path, branch, and tmux window name.

set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: spawn.sh <feature-name> <task-text>" >&2
  exit 2
fi

FEATURE="$1"
TASK_TEXT="$2"
BRANCH="work/${FEATURE}"
WORKTREE="${PWD}-wt-${FEATURE}"

git worktree add "$WORKTREE" -b "$BRANCH" >/dev/null

TASK_FILE="$(mktemp -t firtask.XXXXXX)"
printf '%s\n' "$TASK_TEXT" > "$TASK_FILE"

# Capture the new window's id (tmux's stable `@N` handle) so verification is
# immune to spacing in the default list-windows output AND to cross-session
# name collisions (e.g. a stale window with the same FEATURE name in another
# session would otherwise produce a false-positive).
WIN_ID="$(tmux new-window -P -F '#{window_id}' -n "$FEATURE" -c "$WORKTREE" \
  "fir --session-name $FEATURE \"\$(cat $TASK_FILE)\"; rm -f $TASK_FILE; exec \$SHELL")"

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
  } >&2
  exit 1
fi

echo "worktree: $WORKTREE"
echo "branch:   $BRANCH"
echo "window:   $FEATURE"
