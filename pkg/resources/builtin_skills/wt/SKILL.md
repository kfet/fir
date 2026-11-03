---
builtin: true
name: wt
description: Spawn a fresh fir agent in its own tmux session on its own git worktree. Use whenever the user wants the work — or the conversation about the work — to happen over there, not here. If the user points at this skill, or has directly loaded it in the context, that alone is the cue — spawn, don't inline.
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
   Before exploring, run the `recall` skill for this repo (it runs
   `orient` if no repo map exists yet).

   <original task here>
   ```

   If the original task names a repo path, clarify that the path
   identifies the *project*, not the working directory.

3. Run the spawn script:
   ```bash
   bash "$SKILL_DIR/scripts/spawn.sh" <feature-name> "<task text with pin>"
   ```

4. Sweep up after earlier agents. A wt agent's tmux session closes when
   the agent exits, but windows can still be left behind: from agents
   spawned by older versions of this skill, or shells someone opened by
   hand. Always run the sweep once the spawn succeeded:

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

   Each agent runs in its own detached tmux session (named after the
   feature, with `.`/`:` replaced by `_`), so killing some other tmux
   session never takes it down. The script prints `session:`, `window:`
   and an `attach:` command (`tmux switch-client -t …` inside tmux,
   `tmux attach -t …` outside). It fails if that session already exists.

5. Report back what the spawn script returned, then every `REPORT` line.

Closing idle sessions: `scripts/reap-idle.sh` closes every tmux session
whose panes are all idle shells. These are sessions left by agents from
before spawn.sh stopped chaining `exec $SHELL`, and sessions that
tmux-resurrect restored as empty shells. It covers every session on the
tmux server, not only wt ones, so do not run it as part of a spawn. Run it
when the user asks to clean up idle or restored sessions:

```bash
bash "$SKILL_DIR/scripts/reap-idle.sh"          # dry run: REAP / KEEP per session
bash "$SKILL_DIR/scripts/reap-idle.sh" --reap   # close the REAP sessions
```

A session is closed only if every pane is dead or is a shell with no
child process. It never closes an attached session or the session it runs
in. Tell the user which sessions were closed.

Checking in later: the agent's session is named `<feature-name>`, so talk
to it through fir, not its tmux screen:

```bash
fir observe <feature-name> --status     # idle | running | error | no-model, last error
fir observe <feature-name> -n 40        # snapshot of what it did; exits
fir send <feature-name> --wait --timeout 5m 'status?'  # ask, block for the reply
```

The same commands work for an agent on another host — run them there
over ssh (`rexec`). `observe_session` / `send_session` only see this machine.

The task description usually follows below, after the skill body:
