---
name: overseer
description: Shepherd a fleet of coding agents in tmux. Keep them productive, unstuck, and not stepping on each other.
---

# Overseer

You run the outer loop. You don't write code — you make sure the agents who do are effective.

## Scripts

This skill ships helper scripts in its `scripts/` subdirectory. Resolve the path relative to this SKILL.md file and source at the top of every shell block:

```bash
source "$SKILL_DIR/scripts/tmux-helpers.sh"   # gives tm-* commands
# usage.sh is run standalone — see "Rate Limits" below
```

The `tmux-helpers.sh` script handles the tmux socket automatically. Key helpers:

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

Every fleet works in a dedicated git worktree on its own branch. Derive paths from the project root (use `git rev-parse --show-toplevel`) and the session name:

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

```bash
tm-new "$SESSION" worker-1
tm-win "$SESSION" worker-2
tm-win "$SESSION" reviewer
_tm set-option -t "$SESSION" -g automatic-rename off
```

Address agents as `SESSION:WINDOW`.

**Never repurpose agents from another project.** Sending `/new` wipes conversation context. If a session belongs to a different project/worktree, leave it alone.

## FLEET.md — Persist and Restore

Maintain `FLEET.md` in the worktree root so the fleet can be restarted if the tmux session dies. Include session name, project path, worktree path, branch, and an agents table with Window, Role, Current Task, and Status columns.

**When to write:** On fleet creation, every cycle, on agent spawn/kill/reset, on rate-limit pause.

Add `FLEET.md` to `.gitignore`. On restart: read the file, check `tm-list` for the session, recreate missing windows, resume the loop.

## Research First, Code Later

If a feature needs design work, **launch a researcher first and wait for the plan.**

1. Start only the researcher with a clear goal: produce a plan doc and task breakdown.
2. Wait for the plan to appear (poll with `ls` or `git log`).
3. Read the plan yourself. Verify it answers: which files, which interfaces, which order.
4. Then spawn workers, each with a concrete task from the plan.

## The Loop

Poll every 10 seconds. Each cycle asks three questions:

1. **Did anything land?** — `git -C "$WORKTREE" log --oneline -5`
2. **Is anyone stuck?** — Check context % and spinner via `tm-capture`.
3. **Is the build green?** — Run the project's test command in `$WORKTREE`.

**Rename windows every cycle** to reflect current activity:

```bash
DOING=$(tm-capture "$SESSION:$WINDOW" 5 \
  | grep -v '^$\|^─\|^⟩\|%/200k' | tail -1 | cut -c1-40)
tm-renamewin "$SESSION" "$WINDOW" "$WINDOW: ${DOING:-idle}"
```

### Rate Limits — Check Every 5 Cycles

Use the `usage.sh` script from this skill's `scripts/` directory. Look for an auth token in common credential locations:

```bash
TOKEN=$(jq -r '.anthropic.access' ~/.fir/agent/auth.json 2>/dev/null || jq -r '.claudeAiOauthToken // .access_token' ~/.claude/.credentials.json 2>/dev/null)
USAGE_OUT=$(TOKEN="$TOKEN" bash "$SKILL_DIR/scripts/usage.sh")
FIVE_HR=$(echo "$USAGE_OUT" | awk '/Five Hour/ {gsub(/%/,"",$3); print int($3)}')
SEVEN_DAY=$(echo "$USAGE_OUT" | awk '/Seven Day[^a-zA-Z]/ {gsub(/%/,"",$3); print int($3)}')
```

**If `FIVE_HR >= 85` or `SEVEN_DAY >= 95`:**

1. Snapshot each agent, then stop them: `tm-sendraw "$SESSION:$WINDOW" Escape`
2. Build and test in `$WORKTREE`.
3. Commit dirty tracked changes with a checkpoint message.
4. Print progress report (phases done, last commits, build health, open issue counts).
5. Update FLEET.md statuses to `paused`.
6. Parse reset times from `$USAGE_OUT`, sleep until earliest reset + 2 min.
7. Verify usage dropped, resume each agent: `tm-send "$SESSION:$WINDOW" "Continue."`

## When to Intervene

**Idle agent** — Give it the next task. Check review issue files first, then the plan.

**Bloated context:**

| Context % | Action |
|-----------|--------|
| 30–50% | Let it work. `/compact` only if stuck. |
| >50% | `/compact` — frees ~60-70% while retaining continuity. |
| >70% or looping | `/new` — full reset with a fresh, precise task. |

**`/compact` flow:**
```bash
tm-sendraw "$SESSION:$WINDOW" Escape
sleep 1
tm-send "$SESSION:$WINDOW" "/compact"
tm-wait "$SESSION:$WINDOW" '^\s*>\s*$|waiting for input|\$' 60
tm-send "$SESSION:$WINDOW" "Continue."
```

**`/new` flow:**
```bash
tm-sendraw "$SESSION:$WINDOW" Escape
sleep 0.5
tm-send "$SESSION:$WINDOW" "/new"
sleep 1
tm-send "$SESSION:$WINDOW" "THE NEW TASK"
```

Only create a new tmux session if the agent process is dead or the window is gone.

**Two agents on the same file** — Redirect the one with less progress.

**Build broken** — Fix or assign immediately. Nothing else matters until green.

**Deleted files in `git status`** — Restore from git.

**Agent going in circles** — `/new` with a smaller, more concrete task.

## What Makes a Good Task

Short. One commit's worth. Name the files. Say what to test. Say what to commit as.

- **Bad:** "Improve the client"
- **Good:** "Read pkg/foo/client.go. Add an OnReconnect callback field. When reconnectLoop spawns a new process, call it. Add a test. Run the test suite. Commit as 'foo: add OnReconnect hook'."
