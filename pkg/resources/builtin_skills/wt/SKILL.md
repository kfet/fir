---
builtin: true
name: wt
description: Spawn a fresh fir agent in a new tmux window on its own git worktree. Use whenever the user wants the work — or the conversation about the work — to happen over there, not here. If the user points at this skill, or has directly loaded it in the context, that alone is the cue — spawn, don't inline.
override: true
---

Delegate all to a new agent!

1. Pick a short kebab-case feature name from the task description.

2. Run the spawn script:
   ```bash
   bash "$SKILL_DIR/scripts/spawn.sh" <feature-name> "<full task text>"
   ```

3. Report back what the script returned.

The task description usually follows below, after the skill body:
