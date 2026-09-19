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
   workspace. It must do *all* work and commits there.

   Prepend the task text with an explicit pin, e.g.:

   ```
   You are in a dedicated git worktree on branch work/<feature>.
   Do all work and commits in this CWD.

   <original task here>
   ```

   If the original task names a repo path, clarify that the path
   identifies the *project*, not the working directory.

3. Run the spawn script:
   ```bash
   bash "$SKILL_DIR/scripts/spawn.sh" <feature-name> "<task text with pin>"
   ```

4. Sweep up after earlier agents. A finished wt agent leaves its tmux
   window behind, holding a dead shell in a worktree that ship-it has
   already removed. Always run the sweep once the spawn succeeded:

   ```bash
   bash "$SKILL_DIR/scripts/gc.sh" --reap
   ```

   It kills only windows with no agent in them. It never kills a live
   agent, and it never kills your own window.

   Lines that start with `REPORT` are live agents that look wrong — an
   agent whose worktree is gone, or one idle for days. You MUST copy
   every `REPORT` line into your answer, word for word. Do not act on
   them and do not summarise them away. A count of reaped windows is
   not a substitute: the `REPORT` lines are the part a human needs.
   If there are none, say so in one short sentence.

5. Report back what the spawn script returned, then every `REPORT` line.

The task description usually follows below, after the skill body:
