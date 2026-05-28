---
builtin: true
name: wt
description: Spawn a fresh fir agent in a new tmux window on its own git worktree. Use whenever the user wants the work — or the conversation about the work — to happen over there, not here. If the user points at this skill, or has directly loaded it in the context, that alone is the cue — spawn, don't inline.
override: true
---

Delegate all to a new agent!

1. Pick a short kebab-case feature name from the task description.

2. Construct the task text. The spawned agent will land in
   `<cwd>-wt-<feature>` on branch `work/<feature>` — that is its
   workspace. It must do *all* work and commits there, never in the
   source repo's main worktree. Agents routinely misread a phrase
   like "Repo: ~/dev/ai/foo" as an instruction to `cd` and commit on
   main; pre-empt that.

   Prepend the task text with an explicit pin, e.g.:

   ```
   You are in a dedicated git worktree on branch work/<feature>.
   Do all work and commits in this CWD. Do NOT cd into the source
   repo or any sibling worktree, and do NOT commit to main.

   <original task here>
   ```

   If the original task names a repo path, clarify that the path
   identifies the *project*, not the working directory.

3. Run the spawn script:
   ```bash
   bash "$SKILL_DIR/scripts/spawn.sh" <feature-name> "<task text with pin>"
   ```

4. Report back what the script returned.

The task description usually follows below, after the skill body:
