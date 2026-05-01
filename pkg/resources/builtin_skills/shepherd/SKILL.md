---
builtin: true
name: shepherd
description: Shepherd, start, manage, coordinate a fleet of coding agents — spawn agents as separate processes in tmux windows, assign tasks, monitor progress, handle rate limits, and coordinate merges.
---

# Shepherd

You run the outer loop. You don't write code — you make sure the agents who do are effective.

## Scripts

Source at the top of every shell block:

```bash
source "$SKILL_DIR/scripts/auto-helpers.sh"   # gives tm-* commands (tmux or built-in PTY)
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

**Ask the user first** whether work should happen in the current directory or a new worktree. If the user says "we're already in the right worktree," use it as-is — don't create another one.

If a new worktree is needed, follow the worktree convention in `AGENTS.md` to create one. Use `fleet/` as the branch prefix instead of `work/`:

```bash
PROJECT="$PWD"
SESSION="<project>-<feature>"        # e.g. myproject-auth
FEATURE=${SESSION#*-}
BRANCH="fleet/${SESSION}"
WORKTREE="${PROJECT}-wt-${FEATURE}"

git -C "$PROJECT" worktree add "$WORKTREE" -b "$BRANCH"
```

All commands use `$WORKTREE`. **Never send agents to a different project's worktree.**

When done: merge the branch (or open a PR), then `git worktree remove` and `git branch -d`.

### Session — One Session, Many Windows

Example:
```bash
tm-new "$SESSION" worker-1
tm-win "$SESSION" worker-2
tm-win "$SESSION" reviewer
_tm set-option -t "$SESSION" -g automatic-rename off
```

**Model selection:** Workers must use the **same provider** as the shepherd. Default to **Anthropic** unless the user says otherwise. Ask the user if you're unsure.

Select model according to the tasks.

| Provider | Coding workers (cheaper) | Review/design (expensive) |
|----------|---------------|---------------|
| Anthropic | `sonnet` | `opus` |
| OpenAI | `gpt-mini` | `gpt-pro` |
| Google | `gemini-flash` | `gemini-pro` |

**Always pass `--provider` explicitly** to workers so they don't get routed to a different provider by auto-server:

```bash
PROVIDER="anthropic"         # default — ask user if different
CHEAP_MODEL="sonnet"

tm-send "$SESSION:$WINDOW" "cd $WORKTREE && fir --provider $PROVIDER --model $CHEAP_MODEL -d auto-namer -d notify -d provider-usage -d tmuxspinner"
```

The four `-d` flags disable builtin extensions that fight with tmux-driver — see the tmux-driver skill "Spawning `fir` Inside a Window" section for why.

Address agents as `SESSION:WINDOW`.

**Never repurpose agents from another project.** `/new` wipes context. If a session belongs to a different project/worktree, leave it alone.

## Plan Tracking

If the `plan` tool is available, use it to track overall project progress across the fleet. Create it once at fleet setup, and update it every loop cycle.

**At setup:** Create one entry per major task/milestone (not per agent). Set the first active task to `in_progress`, the rest to `pending`. **You must include `metadata` and `title`** in every plan call so the user can find the fleet:

```json
{
  "title": "Refactor pkg/core",
  "metadata": {
    "session": "fir-refactor",
    "worktree": "/path/to/project-wt-refactor",
    "attach": "tmux -S <socket> attach -t fir-refactor  (tmux only)",
    "branch": "fleet/fir-refactor"
  },
  "entries": [
    {"content": "Analyze and design", "priority": "high", "status": "in_progress"},
    {"content": "Implement feature X", "priority": "high", "status": "pending"}
  ]
}
```

The `attach` value must use the **actual socket path** from `tm-new` output (look for the `Monitor:` line). Without it the user can't connect to the fleet session.

**Each cycle:** Reflect actual state — mark completed tasks `completed`, the currently running task `in_progress`, and future tasks `pending`. Keep entries coarse (milestones, not individual file edits).

**Include per-worker status in the plan.** After capturing each worker's output during the loop, add a plan entry per worker summarizing what they're doing. Format: `"[worker-N] <status summary>"`. This makes worker state visible alongside milestones:

```
plan:
  - Move export.go (high, in_progress)
  - Move bashexec.go (high, in_progress)
  - Move footerdataprovider.go (high, pending)
  - [worker-1] reading bashexec.go, planning move (medium, in_progress)
  - [worker-2] rate-limited, waiting for reset (medium, in_progress)
  - [worker-3] idle, unassigned (low, pending)
```

Update worker entries every cycle based on `tm-capture` output — look for spinner (working), error messages (stuck/rate-limited), context %, and the last visible action.

**Worker self-reported progress:** Workers also have access to the `plan` tool and may use it to report their own progress on sub-tasks. To check a worker's current plan, send `/plan` to their tmux window and capture the output:

```bash
tm-send "$SESSION:$WINDOW" "/plan"
sleep 2
tm-capture "$SESSION:$WINDOW" 30
```

This prints the worker's plan entries with their statuses. Incorporate what they report into your top-level plan: if a worker marks their sub-task complete, update the corresponding milestone. This is faster and more reliable than inferring progress from git logs alone.

**Encouraging workers to use the plan tool:** When assigning a task, tell agents to use the `plan` tool to track their sub-steps and mark them as they go. Example instruction: *"Use the plan tool to break this into steps and update progress as you work."* This gives you live visibility without having to poll `tm-capture` for context clues.

**At close-out:** Mark all entries `completed` before stopping the fleet.

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

```bash
tm-loop-tick "$SESSION" "$WORKTREE"
```

This single command does: `git log`, per-worker status (context % + last activity), and auto-renames all windows. Parse its output to decide next actions.

Additional checks each cycle:

1. **Is anyone stuck?** — Look for high context % or `idle` in `tm-loop-tick` output.
2. **Is the build green?** — Run the project's test command in `$WORKTREE`.
3. **Update the plan** — Reflect completed/active milestones if the plan tool is available.

### Rate Limits — Check Every 5 Cycles

```bash
USAGE_OUT=$(tm-check-usage)
FIVE_HR=$(echo "$USAGE_OUT"  | awk '/Five Hour/      {gsub(/%/,"",$3); print int($3)}')
SEVEN_DAY=$(echo "$USAGE_OUT" | awk '/Seven Day[^a-zA-Z]/ {gsub(/%/,"",$3); print int($3)}')
```

**If `FIVE_HR >= 85` or `SEVEN_DAY >= 99`:**

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
**Read-without-edit loop** — If `tm-capture` shows the agent repeatedly reading the same file without any `edit` or `write` tool calls appearing, it has analysis paralysis. `/new` naming the exact file to rewrite: *"Rewrite `<file>` completely from scratch to match the new API. Do not read it first. Here is what it needs to do: ..."*

## Git Conflicts in Shared Worktrees

Multiple agents on one worktree **will** collide on git operations.

**Best practice: the shepherd does all commits.** Tell agents: "Do NOT run `git add` or `git commit`. Make your changes and run tests. I will commit." After each agent reports success, commit their files yourself:

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

## Close-Out — Before Declaring Done

When all tasks appear complete and the build is green, do NOT stop. Run the reviewer one final time (or re-read REVIEW.md). Fix every finding. Then re-read the original task prompt and verify each stated requirement is met — not just compiling, but actually functional. Mark all plan entries `completed`. Only stop the fleet when every requirement is checked off and every review finding is fixed.

## What Makes a Good Task

Short. One commit's worth. Name the files. Say what to test. Say what to commit as.

- **Bad:** "Improve the client"
- **Good:** "Read pkg/foo/client.go. Add an OnReconnect callback field. When reconnectLoop spawns a new process, call it. Add a test. Run the test suite. Commit as 'foo: add OnReconnect hook'."
