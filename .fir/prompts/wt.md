---
description: Start fir in a new git worktree, in a new tmux window, with your prompt
---

Task: $@

1. Pick a **feature-name**: summarize the entire task into a short kebab-case name (2-4 words, e.g. "config-dir-cleanup"). Read the full task — don't just use the first word.

2. Discover the project root and name:
```bash
git rev-parse --show-toplevel
```
The project name is the basename of the root (e.g. `/home/user/dev/fir` → `fir`).

3. Create the worktree and branch (worktree goes as a sibling of the project root):
```bash
cd <project-root> && git worktree add -b <feature-name> ../<project-name>-wt-<feature-name>
```

4. Open a tmux window with fir, passing the full task description:
```bash
tmux new-window -n <feature-name> -c <worktree-abs-path> "fir --session-name '<feature-name>' '<full task description>'; exec \$SHELL"
```

5. Tell the user: worktree path, branch, and tmux window name. Done!
