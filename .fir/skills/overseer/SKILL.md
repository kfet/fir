---
name: overseer
description: "Shepherd a fleet of coding agents in tmux. Keep them productive, unstuck, and not stepping on each other."
---

# Overseer

You run the outer loop. You don't write code — you make sure the agents who do are effective.

## API Rate-Limit Guard (check every cycle)

At the start of every cycle, check the **Five Hour** (daily) utilisation:

```bash
TOKEN=$(jq -r '.anthropic.access' ~/.fir/agent/auth.json 2>/dev/null)
SCRIPT=/Users/kfet/dev/ai/fir/.fir/skills/claude-usage/scripts/usage.sh
TOKEN="$TOKEN" bash "$SCRIPT"
```

Extract the Five Hour percentage and reset time:
```bash
USAGE_OUT=$(TOKEN="$TOKEN" bash "$SCRIPT")
FIVE_HR=$(echo "$USAGE_OUT" | awk '/Five Hour/ {gsub(/%/,"",$3); print int($3)}')
RESET_TIME=$(echo "$USAGE_OUT" | awk '/Five Hour/ {print $NF}')   # e.g. "11:00 PM PST"
```

**If `FIVE_HR >= 85`:**
1. Escape all agents (`send-keys Escape`) — stop any generation.
2. Run `go build ./... && go test -count=1 ./...` — ensure project is clean and buildable.
3. Commit any uncommitted tracked changes: `git add -u && git commit -m "chore: checkpoint before rate-limit pause"` (only if dirty).
4. **Print a short progress report** before sleeping so the user knows where things stand:
   - Phases complete vs. in-progress (from PLAN.md status line)
   - Last 5 commits (`git log --oneline -5`)
   - Build health (all green / any failures)
   - URGENT and BACKLOG open item counts
   - Which agents were active and what they were doing
5. Print a clear notice with the reset time:
   ```
   ⛔ Rate limit at ${FIVE_HR}% — pausing all agents.
   📅 Five Hour window resets at ${RESET_TIME}.
   ⏰ Will resume 2 minutes after reset.
   ```
6. Compute seconds until reset + 2 minutes and sleep:
   ```bash
   # Parse reset time and sleep until reset + 2 min
   RESET_EPOCH=$(date -j -f "%I:%M %p" "$RESET_CLOCK" "+%s" 2>/dev/null \
     || date -d "$RESET_CLOCK" "+%s")   # macOS vs Linux
   NOW=$(date +%s)
   WAIT=$(( RESET_EPOCH - NOW + 120 ))
   [ "$WAIT" -lt 0 ] && WAIT=$(( WAIT + 86400 ))  # next day if already past
   echo "Sleeping ${WAIT}s (~$((WAIT/60)) min) until ${RESET_TIME} + 2 min..."
   sleep "$WAIT"
   ```
7. After waking, verify usage has dropped, then **resume the loop** — re-send tasks to any idle agents and continue monitoring.

Check usage every **5 cycles** (not every cycle) to avoid adding overhead.

## One Fleet Per Project — Never Repurpose

**Each project gets its own dedicated agents. Never redirect, reset, or repurpose agents from another project.**

### Use a single tmux session with one window per agent

The preferred layout is **one session named after the project, with one window per agent**:

```bash
# Create the session (first window becomes the reviewer)
tmux -S "$SOCKET" new -d -s acp-claw -n reviewer -c /path/to/project

# Add worker windows
tmux -S "$SOCKET" new-window -t acp-claw -n worker-core  -c /path/to/project
tmux -S "$SOCKET" new-window -t acp-claw -n worker-acp   -c /path/to/project
tmux -S "$SOCKET" new-window -t acp-claw -n worker-telegram -c /path/to/project

# Turn off auto-rename globally
tmux -S "$SOCKET" set-option -t acp-claw -g automatic-rename off
```

Address agents as `SESSION:WINDOW` (e.g. `acp-claw:reviewer`, `acp-claw:worker-core`).

To move an existing window from another session into the fleet session:

```bash
tmux -S "$SOCKET" move-window -s other-session:0 -t acp-claw
tmux -S "$SOCKET" rename-window -t acp-claw:N new-name
```

**Why single session:** easier to attach (`tmux attach -t acp-claw`), all agents visible at once, no stray orphan sessions.

### Never repurpose agents from another project

Before spawning any agent, list all existing sessions and check which project they belong to:

```bash
tmux -S "$SOCKET" list-sessions
# then for any session whose project isn't obvious:
tmux -S "$SOCKET" capture-pane -p -J -t NAME:0.0 -S -5 | grep "~/"
```

If a session is working in a different project directory — **leave it alone**. Do not send it Escape, `/new`, `/compact`, or any task. It is not yours to manage.

Spawn fresh sessions with project-scoped names to avoid confusion:

```bash
# Good — name encodes the project
tmux -S "$SOCKET" new -d -s fir-researcher   -c /path/to/fir
tmux -S "$SOCKET" new -d -s fir-worker-mcp   -c /path/to/fir
tmux -S "$SOCKET" new -d -s acp-worker-core  -c /path/to/acp-claw

# Bad — generic names that look reusable
tmux -S "$SOCKET" new -d -s worker
tmux -S "$SOCKET" new -d -s reviewer
```

**Why:** Sending `/new` to an agent mid-task wipes its entire conversation context — it forgets everything it was doing. There is no benefit to reusing agents across projects; the only bookkeeping value is having an overseer agent track which sessions belong to which project.

## Research First, Code Later

If a feature needs design work — a new package, a new protocol, a non-trivial integration — **launch a researcher first and wait for the plan before spawning implementation agents.**

Implementation agents given a vague task waste tokens reading the same files the researcher already read, make inconsistent design decisions, and often have to be redirected. A finished plan pays for itself immediately.

The sequence:

1. **Start only the researcher.** Give it a clear goal: produce a plan doc and a task breakdown. Do not start workers yet.
2. **Wait for the plan to land** — poll `git log` for the commit, or watch for the plan file:
   ```bash
   # poll until plan appears
   until ls /path/to/project/docs/plan/NN-feature.md 2>/dev/null; do sleep 15; done
   ```
3. **Read the plan yourself** before dispatching workers. Verify it answers: which files, which interfaces, which order.
4. **Then spawn workers**, each with a concrete task drawn directly from the plan's task breakdown. One task per agent, one commit's worth each.

**Do not** pre-load workers with "read code and wait for the plan" tasks — that burns tokens on redundant reading. Cold workers start faster and cheaper once the plan is ready.

## Rhythm

Poll every 10 seconds. Each cycle is three questions:

1. **Did anything land?** — `git log --oneline -5`. Commits mean progress.
2. **Is anyone stuck?** — Check context % and spinner. No spinner = idle or dead.
3. **Is the build green?** — `go test ./...` catches cross-agent breakage fast.

Act on what you find. If everything's fine, move on.

**After checking each agent, rename its tmux window** to reflect what it's currently doing. This is mandatory every cycle — it gives you a live dashboard at a glance.

Derive the label from the last visible line of the agent's output:

```bash
# Using raw tmux:
DOING=$(tmux -S "$SOCKET" capture-pane -p -J -t NAME:0.0 -S -5 \
  | grep -v '^$\|^─\|^⟩\|%/200k' | tail -1 | cut -c1-40)
tmux -S "$SOCKET" rename-window -t NAME:0 "NAME: ${DOING:-idle}"

# Using tmux-driver helpers (tm-renamewin renames by old→new name):
DOING=$(tm-capture SESSION:WINDOW 5 \
  | grep -v '^$\|^─\|^⟩\|%/200k' | tail -1 | cut -c1-40)
tmux -S "$SOCKET" rename-window -t "SESSION:WINDOW" "WINDOW: ${DOING:-idle}"
```

Examples of good window names:
- `worker-mcp: writing client.go`
- `reviewer: inspecting tool_adapter`
- `worker-acp: waiting for plan`
- `researcher: idle`

## When to intervene

**Idle agent** — Give it the next thing. Check `docs/review/URGENT.md` first, then `BACKLOG.md`, then invent a task from SPEC.md.

**Bloated context (>50%)** — Prefer `/compact` over `/new`: it summarises history into a short prefix so the agent retains what it was doing but frees ~60–70% of tokens. Reserve `/new` for when the agent is going in circles and you *want* it to forget.

```bash
# Compact (preferred — retains summarised history)
tmux -S "$SOCKET" send-keys -t NAME:0.0 Escape   # stop if generating
sleep 1
tmux -S "$SOCKET" send-keys -t NAME:0.0 -l '/compact'
tmux -S "$SOCKET" send-keys -t NAME:0.0 Enter
sleep 20   # compact takes ~15s
# Then send the next task; first response will show the new (lower) ctx %

# /new (use when agent is looping or you want a blank slate)
tmux -S "$SOCKET" send-keys -t NAME:0.0 Escape
sleep 0.5
tmux -S "$SOCKET" send-keys -t NAME:0.0 -l '/new'
tmux -S "$SOCKET" send-keys -t NAME:0.0 Enter
sleep 2
```

Rough guide:
- **30–50%**: Let it work — `/compact` only if it gets stuck or feels slow.
- **>50%**: `/compact` — frees tokens while retaining continuity.
- **>70% or looping**: `/new` — full reset, give a fresh precise task.

**Two agents touching the same file** — One of them loses. Redirect the one with less progress to something else.

**Build broken** — Fix it yourself or assign it immediately. Nothing else matters until green.

**Deleted files in `git status`** — Agents delete things they shouldn't. Restore from git and move on.

**Agent going in circles** — Repeating itself, re-reading the same files, not committing. Kill it. Give it a smaller, more concrete task.

## Resetting a bloated agent (>50%)

**Prefer `/compact`** — summarises history, frees tokens, agent retains context. Only use `/new` or a new session when the agent is truly stuck or looping (>70%).

```bash
tmux -S "$SOCKET" send-keys -t NAME:0.0 Escape   # stop current generation
sleep 0.5
tmux -S "$SOCKET" send-keys -t NAME:0.0 -l '/new'
tmux -S "$SOCKET" send-keys -t NAME:0.0 Enter
sleep 1
tmux -S "$SOCKET" send-keys -t NAME:0.0 -l 'THE TASK'
tmux -S "$SOCKET" send-keys -t NAME:0.0 Enter
```

Only create a new session if the agent process itself is dead or the window is gone:

```bash
tmux -S "$SOCKET" new -d -s NAME -n NAME -c "$PROJECT"
tmux -S "$SOCKET" send-keys -t NAME:0.0 -l "fir --provider anthropic --model claude-sonnet-4-6"
tmux -S "$SOCKET" send-keys -t NAME:0.0 Enter
sleep 3
tmux -S "$SOCKET" send-keys -t NAME:0.0 -l 'THE TASK'
tmux -S "$SOCKET" send-keys -t NAME:0.0 Enter
```

## What makes a good task

Short. One commit's worth. Name the files. Say what to test. Say what to commit as.

Bad: "Improve the ACP client"
Good: "Read pkg/acp/client.go. Add an OnReconnect callback field. When reconnectLoop spawns a new process, call it. Add a test. Run make test. Commit as 'acp: add OnReconnect hook'."
