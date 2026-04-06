---
description: Start fir in a new git worktree, in a new tmux window, with your prompt
---

Task: $@

Use the `aside` tool to do all the work ephemerally. The aside should use Bash tool calls to run the steps below.

**Before writing the aside**, determine:
- **feature-name**: Summarize the entire task into a short kebab-case name (2-4 words, e.g. "config-dir-cleanup"). Do NOT just use the first word — read the full task and pick a name that captures the intent.
- **project root**: Run `git rev-parse --show-toplevel` to get the absolute project root path.
- **project name**: The basename of the project root (e.g. if root is `/home/user/dev/fir`, project name is `fir`).

Then pass these concrete, absolute paths into the aside instructions — do NOT use placeholders or expect the aside to discover them.

The aside should:

1. Create a worktree and branch. The worktree directory must be `<project-root>/../<project-name>-wt-<feature-name>`:
```bash
cd <project-root> && git worktree add -b <feature-name> ../<project-name>-wt-<feature-name>
```

2. Open a new tmux window running fir with the full task description:
```bash
tmux new-window -n <feature-name> -c <worktree-abs-path> "fir --session-name '<feature-name>' '<full task description>'; exec \$SHELL"
```

3. Return a summary: worktree path, branch, and tmux window name.

Set the aside title to "wt: <feature-name>".

After the aside completes, relay its summary to the user. Done!
