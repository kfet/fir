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

TASK_FILE="$(mktemp -t firtask)"
printf '%s\n' "$TASK_TEXT" > "$TASK_FILE"

tmux new-window -n "$FEATURE" -c "$WORKTREE" \
  "fir --session-name $FEATURE \"\$(cat $TASK_FILE)\"; rm -f $TASK_FILE; exec \$SHELL"

# tmux returns success as soon as the shell launches; verify the window stuck.
sleep 1
if ! tmux list-windows -a 2>/dev/null | grep -q " ${FEATURE} "; then
  echo "WINDOW MISSING — investigate (worktree at $WORKTREE)" >&2
  exit 1
fi

echo "worktree: $WORKTREE"
echo "branch:   $BRANCH"
echo "window:   $FEATURE"
