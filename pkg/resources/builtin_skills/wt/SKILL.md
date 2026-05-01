---
name: wt
description: Delegate a task to a fresh fir agent running in a new tmux window on its own git worktree. Use when the user wants to fire-and-forget a task, or spin up a parallel agent, instead of doing the work in the current session.
---

# wt — spawn a delegated agent in a worktree

Use this skill when the user asks to "kick off", "spin up", "delegate", or "start a worktree for" a task and have **another** agent do it. If the user wants *you* to do the work, follow the worktree discipline in `AGENTS.md` instead — don't spawn a new session.

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
