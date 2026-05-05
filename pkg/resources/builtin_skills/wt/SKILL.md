---
builtin: true
name: wt
description: Delegate a task to a fresh fir agent in a new tmux window on its own git worktree. Use when the user asks to "kick off", "spin up", "delegate", or "start a worktree for" a task — i.e. wants another agent to do it, not you. If the user wants you to do the work, follow the worktree discipline in `AGENTS.md` instead.
---

# wt — spawn a delegated agent in a worktree

Spawn the agent in a new tmux window and continue the task over there. Don't do any of the work — investigation, design, edits — in the current session.

## Recipe

1. Pick a **feature name**: short kebab-case (2–4 words) summarising the whole task. Read the full task — don't latch onto the first word.

2. Create the worktree + branch as a sibling of the project root:
   ```bash
   FEATURE="<feature-name>"
   BRANCH="work/${FEATURE}"
   WORKTREE="${PWD}-wt-${FEATURE}"
   git worktree add "$WORKTREE" -b "$BRANCH"
   ```

3. Open a new tmux window running fir in the worktree, passing the full task:
   ```bash
   tmux new-window -n "$FEATURE" -c "$WORKTREE" \
     "fir --session-name '$FEATURE' '<full task description>'; exec \$SHELL"
   ```

4. Report back to the user: worktree path, branch name, tmux window name.

Then stop — the spawned agent owns the task from here. Cleanup (merge, `worktree remove`, branch delete) is its responsibility per `AGENTS.md`.
