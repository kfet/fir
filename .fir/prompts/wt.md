---
description: Create a git worktree + branch, open a tmux window with fir running in it, ready to work on a feature
---

Create a new feature worktree for: $1

Additional context (if any): ${@:2}

Steps:

1. Derive a short kebab-case feature name from "$1".

2. Create the worktree and branch:
```bash
PROJECT=$(git rev-parse --show-toplevel)
FEATURE="<kebab-name>"
BRANCH="work/${FEATURE}"
WORKTREE="${PROJECT}-wt-${FEATURE}"
git -C "$PROJECT" worktree add "$WORKTREE" -b "$BRANCH"
```

3. Open a new tmux window and launch fir inside it with the user's task as the initial prompt. Set the fir session name to the feature name so tmux auto-updates:
```bash
tmux new-window -c "$WORKTREE" "fir 'You are working on feature: ${FEATURE}. ${USER_CONTEXT}' '/name ${FEATURE}'"
```

Where `${USER_CONTEXT}` is the additional context from `${@:2}`, if any.

4. Report what you did: worktree path, branch, and the tmux window where fir is running.

Do NOT cd into the worktree yourself. Do NOT start any builds or tests. The new fir instance in the tmux window handles all the work.
