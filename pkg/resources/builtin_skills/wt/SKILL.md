---
builtin: true
name: wt
description: Spawn a fresh fir agent in a new tmux window on its own git worktree. Use whenever the user wants the work — or the conversation about the work — to happen over there, not here. Doing it, designing it, or just talking it through all count. If the user points at this skill, that alone is the cue — spawn, don't inline.
override: true
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

2. Run the spawn script. It creates the worktree + branch, launches fir in a new tmux window, and verifies the window stuck:
   ```bash
   bash "$SKILL_DIR/scripts/spawn.sh" <feature-name> "<full task text including 'Mode: do.' or 'Mode: discuss.'>"
   ```

   In discuss-mode, say so plainly in the task text — the spawned agent will know what that means.

3. Tell the user the worktree path, branch, and window name (the script prints them). Then stop.
