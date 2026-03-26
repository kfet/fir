---
description: Start fir in a new git worktree, in a new tmux window, with your prompt
---

Task: $@

Steps:

1. Summarize the entire task into a short kebab-case feature-name (2-4 words, e.g. "config-dir-cleanup"). Do NOT just use the first word — read the full task and pick a name that captures the intent.

2. Create the worktree and branch using the feature name.

3. Open a new tmux window and launch fir inside it with the user's task as the initial prompt. Make sure to pass the FULL task description (from "Task:" above) into the fir command, properly escaped for the shell:
```bash
tmux new-window -c "<worktree-path>" "fir --session-name '<feature-name>' '<full task description>'"
```

4. Report what you did: worktree path, branch, and the tmux window where fir is running.

5. Done! The new fir instance in the tmux window handles all the work.
