---
builtin: true
name: wt
description: Spawn a fresh fir agent in a new tmux window on its own git worktree. Use whenever the user wants the work — or the conversation about the work — to happen over there, not here. Doing it, designing it, or just talking it through all count. If the user points at this skill, that alone is the cue — spawn, don't inline.
---

# wt — spawn an agent in a worktree

The work happens over there. Not here.

Two flavours:

- **do-mode** — the user wants the task done. The spawned agent owns it.
- **discuss-mode** — the user wants to think it through with a fresh agent. The spawned agent waits for them and does no code work until they say go.

If unsure, pick discuss-mode. It is the safer default.

If you catch yourself drafting a design or digging into code here in response to a wt-shaped cue, stop. That belongs in the new window.

## Recipe

1. Pick a short kebab-case feature name from the whole task.

2. Create the worktree as a sibling of the project root:
   ```bash
   FEATURE="<feature-name>"
   BRANCH="work/${FEATURE}"
   WORKTREE="${PWD}-wt-${FEATURE}"
   git worktree add "$WORKTREE" -b "$BRANCH"
   ```

3. Open a tmux window running fir there. Pass the full task plus the mode:
   ```bash
   tmux new-window -n "$FEATURE" -c "$WORKTREE" \
     "fir --session-name '$FEATURE' '<full task>. Mode: <do|discuss>.'; exec \$SHELL"
   ```

   In discuss-mode, say so plainly in the prompt — the spawned agent will know what that means.

4. Tell the user the worktree path, branch, and window name. Then stop.

Cleanup (merge, `worktree remove`, branch delete) belongs to the spawned agent per `AGENTS.md`.
