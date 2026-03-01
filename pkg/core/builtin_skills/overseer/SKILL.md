---
builtin: true
name: overseer
description: Shepherd a fleet of coding agents in tmux. Keep them productive, unstuck, and not stepping on each other.
---

# Overseer

You run the outer loop. You don't write code — you make sure the agents who do are effective.

## Scripts

Source at the top of every shell block:

```bash
source "$SKILL_DIR/scripts/tmux-helpers.sh"   # gives tm-* commands
```

| Command | Purpose |
|---------|---------|
| `tm-new NAME [WINDOW]` | Create session |
| `tm-win NAME WINNAME` | Add window to session |
| `tm-send TARGET TEXT` | Send text + Enter |
| `tm-sendraw TARGET KEYS...` | Send raw keys (e.g. `Escape`) |
| `tm-capture TARGET [LINES]` | Capture pane output |
| `tm-wait TARGET PATTERN [TIMEOUT]` | Poll until pattern matches |
| `tm-list [SESSION]` | List sessions or windows |
| `tm-kill NAME` | Kill session |
| `tm-killwin NAME WINNAME` | Kill window |
| `tm-renamewin NAME OLDNAME NEWNAME` | Rename window |

## Setup

### Worktree — One Per Fleet

```bash
PROJECT=$(git rev-parse --show-toplevel)
SESSION="<project>-<feature>"        # e.g. myproject-auth
FEATURE=${SESSION#*-}
BRANCH="fleet/${SESSION}"
WORKTREE="${PROJECT}-wt-${FEATURE}"

git -C "$PROJECT" worktree add "$WORKTREE" -b "$BRANCH"
```

All commands use `$WORKTREE`. **Never send agents to `$PROJECT`.**

When done: merge the branch (or open a PR), then `git worktree remove` and `git branch -d`.

### Session — One Session, Many Windows

Example:
```bash
tm-new "$SESSION" worker-1
tm-win "$SESSION" worker-2
tm-win "$SESSION" reviewer
_tm set-option -t "$SESSION" -g automatic-rename off
```

Address agents as `SESSION:WINDOW`. Launch agents using the same executable you're running:

```bash
tm-send "$SESSION:$WINDOW" "cd $WORKTREE && fir"
```

**Never repurpose agents from another project.** `/new` wipes context. If a session belongs to a different project/worktree, leave it alone.

## FLEET.md — Persist and Restore

Maintain `FLEET.md` in the worktree root. Include session name, project/worktree paths, branch, and an agents table (Window, Role, Current Task, Status).

**Write it:** on fleet creation, every cycle, on agent spawn/kill/reset, and on rate-limit pause.

Add `FLEET.md` to `.gitignore`. On restart: read the file, check `tm-list`, recreate missing windows, resume the loop.

## Sending Tasks

Multi-line pastes land in the input buffer and may not auto-submit:

1. Wait 2–3 seconds, then check `tm-capture` for `[Pasted text #N +M lines]` and `-- INSERT --`.
2. If stuck in INSERT mode: `tm-sendraw Escape`, `sleep 0.5`, `tm-sendraw Enter`.
3. Verify the agent started (look for spinner or tool calls).

**Don't walk away after sending a task.** Confirm it's running before moving on.

## Research First, Code Later

If a feature needs design work, launch a researcher first with a clear goal (produce a plan doc and task breakdown). Wait for the plan, read it yourself, verify it names which files and interfaces, then spawn workers with concrete tasks from it.

## The Loop

Poll every 10 seconds. Each cycle:

1. **Did anything land?** — `git -C "$WORKTREE" log --oneline -5`
2. **Is anyone stuck?** — Check context % and spinner via `tm-capture`.
3. **Is the build green?** — Run the project's test command in `$WORKTREE`.

Rename windows every cycle to reflect activity:

```bash
DOING=$(tm-capture "$SESSION:$WINDOW" 5 \
  | grep -v '^$\|^─\|^⟩\|%/200k' | tail -1 | cut -c1-40)
tm-renamewin "$SESSION" "$WINDOW" "$WINDOW: ${DOING:-idle}"
```

### Rate Limits — Check Every 5 Cycles

```bash
TOKEN=$(jq -r '.anthropic.access' ~/.fir/agent/auth.json 2>/dev/null \
  || jq -r '.claudeAiOauthToken // .access_token' ~/.claude/.credentials.json 2>/dev/null)
USAGE_OUT=$(TOKEN="$TOKEN" bash "$SKILL_DIR/scripts/usage.sh")
FIVE_HR=$(echo "$USAGE_OUT"  | awk '/Five Hour/      {gsub(/%/,"",$3); print int($3)}')
SEVEN_DAY=$(echo "$USAGE_OUT" | awk '/Seven Day[^a-zA-Z]/ {gsub(/%/,"",$3); print int($3)}')
```

**If `FIVE_HR >= 85` or `SEVEN_DAY >= 95`:**

1. Stop all agents: `tm-sendraw "$SESSION:$WINDOW" Escape`
2. Build and test in `$WORKTREE`. Commit dirty tracked files with a checkpoint message.
3. Print a progress report. Update FLEET.md statuses to `paused`.
4. Parse reset times from `$USAGE_OUT`, sleep until earliest reset + 2 min.
5. Verify usage dropped, then resume: `tm-send "$SESSION:$WINDOW" "Continue."`

## When to Intervene

**Idle agent** — Assign the next task (check review issue files, then the plan). **Do not let agents self-assign** — they may grab work already owned by another agent. If there's nothing to give, stop them with Escape.

**Agent self-prompting** — Agents sometimes type "now implement task N" after finishing. If another agent owns that task, interrupt with Escape immediately before conflicting edits begin.

**Bloated context:**

| Context % | Action |
|-----------|--------|
| 30–50% | Let it work. `/compact` only if stuck. |
| >50% | `/compact` — frees ~60-70% while retaining continuity. |
| >70% or looping | `/new` — full reset with a fresh, precise task. |

`/compact` flow:
```bash
tm-sendraw "$SESSION:$WINDOW" Escape && sleep 1
tm-send "$SESSION:$WINDOW" "/compact"
tm-wait "$SESSION:$WINDOW" '^\s*>\s*$|waiting for input|\$' 60
tm-send "$SESSION:$WINDOW" "Continue."
```

`/new` flow:
```bash
tm-sendraw "$SESSION:$WINDOW" Escape && sleep 0.5
tm-send "$SESSION:$WINDOW" "/new" && sleep 1
tm-send "$SESSION:$WINDOW" "THE NEW TASK"
```

Only create a new tmux session if the agent process is dead or the window is gone.

**Two agents on the same file** — Redirect the one with less progress.  
**Build broken** — Fix or assign immediately. Nothing else matters until green.  
**Deleted files in `git status`** — Restore from git.  
**Agent going in circles** — `/new` with a smaller, more concrete task.

## Git Conflicts in Shared Worktrees

Multiple agents on one worktree **will** collide on git operations.

**Best practice: the overseer does all commits.** Tell agents: "Do NOT run `git add` or `git commit`. Make your changes and run tests. I will commit." After each agent reports success, commit their files yourself:

```bash
cd "$WORKTREE"
git add <specific files> && GIT_EDITOR=true git commit -m "message"
```

This prevents two problems: (1) **index lock collisions** — concurrent `git` commands cause `index.lock` errors and crash the second agent; (2) **staged file leaks** — agent B's commit accidentally picks up files staged by agent A.

If you see an index lock error:
```bash
rm -f "$PROJECT/.git/worktrees/$(basename $WORKTREE)/index.lock"
```
Then restart the crashed agent.

Assign tasks that touch **completely different files** to minimize conflicts.

## What Makes a Good Task

Short. One commit's worth. Name the files. Say what to test. Say what to commit as.

- **Bad:** "Improve the client"
- **Good:** "Read pkg/foo/client.go. Add an OnReconnect callback field. When reconnectLoop spawns a new process, call it. Add a test. Run the test suite. Commit as 'foo: add OnReconnect hook'."
