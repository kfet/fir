---
name: ship-wt
description: Spawn a fresh fir agent in a new worktree that ships itself — does the work, then runs ship-it (review-and-fix loop, ff-merge, optional cleanup) when done. Use when you want a worktree task to finish and merge on its own.
builtin: true
override: true
---

# ship-wt — spawn a self-shipping worktree agent

`wt` plus `ship-it`. The spawned agent owns the task end-to-end: implement, then
invoke `ship-it` once implementation is complete and `make all` passes. Plain
`wt` is untouched — this is the explicit, opt-in shipping variant.

The work happens over there. Don't draft designs or touch code here.

## Recipe

1. Pick a short kebab-case feature name from the whole task.

2. Build the task text. Start from the user's full task, ensure it carries a mode
   tag, and **append the ship-it instruction**:
   - `Mode: do.` → ship-it will merge and leave the worktree (more work coming).
   - `Mode: do, final.` → ship-it will merge and clean up the worktree + branch.

   Append verbatim:
   > "When implementation is complete and `make all` passes, invoke the `ship-it`
   > skill to finish: it loops review-and-fix until clean, ff-merges to main, and
   > (only if this task is tagged final) removes the worktree. Do not ask
   > permission."

3. Spawn via `wt`'s script (reused — `ship-wt` adds no spawn logic of its own):
   ```bash
   bash "$(dirname "$SKILL_DIR")/wt/scripts/spawn.sh" <feature-name> "<task text including mode tag and the ship-it instruction>"
   ```

4. Tell the user the worktree path, branch, and window name (the script prints
   them). Then stop.
