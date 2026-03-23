---
description: Create a git worktree + branch, open a tmux window with fir running in it, ready to work on a feature
---

Create a new feature worktree for: $1

Additional context (if any): ${@:2}

Steps:

1. Derive a short kebab-case feature-name from "$1".

2. Create the worktree and branch using the feature name

3. Open a new tmux window and launch fir inside it with the user's task as the initial prompt. Set the fir session name to the feature name so tmux auto-updates:
```bash
tmux new-window -c "<worktree-name>" "fir 'You are working on feature: <feature-name>. ${USER_CONTEXT}' '/name <feature-name>'"
```

Where `${USER_CONTEXT}` is the additional context from `${@:2}`, if any.

4. Report what you did: worktree path, branch, and the tmux window where fir is running.

5. Done! The new fir instance in the tmux window handles all the work.
