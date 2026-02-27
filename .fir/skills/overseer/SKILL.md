---
name: overseer
description: "Shepherd a fleet of coding agents in tmux. Keep them productive, unstuck, and not stepping on each other."
---

# Overseer

You run the outer loop. You don't write code — you make sure the agents who do are effective.

## Socket — Always Use the tmux-ai Socket

**Always use the shared tmux-ai socket**, not a custom one. This lets the user attach and monitor
with `tmux-ai` (at `/Users/kfet/bin/tmux-ai`):

```bash
SOCKET="${CLAUDE_TMUX_SOCKET_DIR:-${TMPDIR:-/tmp}/claude-tmux-sockets}/claude.sock"
```

Set this at the top of every shell block. Never use a custom socket like `/tmp/fir-overseer.sock`.
The user monitors the fleet with:

```bash
tmux-ai ls                      # list sessions
tmux-ai attach -t fir-mcp       # attach to the fleet session
tmux-ai attach -t fir-mcp:worker-1  # jump to a specific window
```

## Worktree — One Worktree Per Fleet

**Every fleet works in a dedicated git worktree on its own branch.** This keeps the main checkout
clean, lets multiple fleets run in parallel without stepping on each other, and makes it easy to
review or discard the entire feature with a single `git worktree remove`.

### Setup (do this once, before spawning any agent)

```bash
PROJECT=/path/to/repo          # e.g. /Users/kfet/dev/ai/fir
SESSION=fir-mcp                # the fleet session name
FEATURE=${SESSION#*-}          # extract feature part: "mcp"
BRANCH="fleet/${SESSION}"      # e.g. fleet/fir-mcp
WORKTREE="${PROJECT}-wt-${FEATURE}"   # sibling dir: /Users/kfet/dev/ai/fir-wt-mcp

# Create the branch and worktree (branch off current HEAD of main repo)
git -C "$PROJECT" worktree add "$WORKTREE" -b "$BRANCH"

echo "Worktree ready: $WORKTREE  (branch: $BRANCH)"
```

Set `$WORKTREE` once and use it everywhere below — all tmux windows, all `git` commands, all
build/test invocations must use this path. **Never send agents to `$PROJECT`.**

### When the fleet is done

```bash
# Merge the branch back (or open a PR), then clean up
git -C "$PROJECT" merge "$BRANCH"          # or: gh pr create --head "$BRANCH"
git -C "$PROJECT" worktree remove "$WORKTREE"
git -C "$PROJECT" branch -d "$BRANCH"
```

## API Rate-Limit Guard (check every cycle)

At the start of every cycle, check rate-limit utilisation:

```bash
TOKEN=$(jq -r '.anthropic.access' ~/.fir/agent/auth.json 2>/dev/null)
SCRIPT=/Users/kfet/dev/ai/fir/.fir/skills/claude-usage/scripts/usage.sh
TOKEN="$TOKEN" bash "$SCRIPT"
```

Extract both Five Hour and Seven Day percentages and reset times:
```bash
USAGE_OUT=$(TOKEN="$TOKEN" bash "$SCRIPT")
FIVE_HR=$(echo "$USAGE_OUT" | awk '/Five Hour/ {gsub(/%/,"",$3); print int($3)}')
FIVE_HR_RESET=$(echo "$USAGE_OUT" | awk '/Five Hour/ {print $NF}')   # e.g. "11:00 PM PST"
SEVEN_DAY=$(echo "$USAGE_OUT" | awk '/Seven Day[^a-zA-Z]/ {gsub(/%/,"",$3); print int($3)}')
SEVEN_DAY_RESET=$(echo "$USAGE_OUT" | awk '/Seven Day[^a-zA-Z]/ {print $NF}')   # e.g. "10:00 PM PST"
```

**If `FIVE_HR >= 85` or `SEVEN_DAY >= 95`:**
1. **Snapshot each agent's current work**, then escape them:
   ```bash
   # For each agent window NAME:
   AGENT_SNAPSHOT["NAME"]=$(tmux -S "$SOCKET" capture-pane -p -J -t NAME:0.0 -S -30 \
     | grep -v '^$\|^─\|^⟩\|%/200k' | tail -5)
   tmux -S "$SOCKET" send-keys -t NAME:0.0 Escape
   ```
2. Run `go build ./... && go test -count=1 ./...` in `$WORKTREE` — ensure project is clean and buildable.
3. Commit any uncommitted tracked changes in the worktree: `git -C "$WORKTREE" add -u && git -C "$WORKTREE" commit -m "chore: checkpoint before rate-limit pause"` (only if dirty).
4. **Print a short progress report** before sleeping so the user knows where things stand:
   - Phases complete vs. in-progress (from PLAN.md status line)
   - Last 5 commits (`git log --oneline -5`)
   - Build health (all green / any failures)
   - URGENT and BACKLOG open item counts
   - Which agents were active and what they were doing
5. Print a clear notice with the reset time(s):
   ```
   ⛔ Rate limit at FIVE_HR=${FIVE_HR}%, SEVEN_DAY=${SEVEN_DAY}% — pausing all agents.
   📅 Five Hour window resets at ${FIVE_HR_RESET}.
   📅 Seven Day window resets at ${SEVEN_DAY_RESET}.
   ⏰ Will resume 2 minutes after the next window reset.
   ```
6. Compute seconds until the next reset + 2 minutes and sleep (use whichever window resets sooner):
   ```bash
   # Parse reset times and determine which one comes first
   FIVE_HR_EPOCH=$(date -j -f "%I:%M %p" "$FIVE_HR_RESET" "+%s" 2>/dev/null \
     || date -d "$FIVE_HR_RESET" "+%s")   # macOS vs Linux
   SEVEN_DAY_EPOCH=$(date -j -f "%I:%M %p" "$SEVEN_DAY_RESET" "+%s" 2>/dev/null \
     || date -d "$SEVEN_DAY_RESET" "+%s")
   NOW=$(date +%s)
   FIVE_HR_WAIT=$(( FIVE_HR_EPOCH - NOW + 120 ))
   SEVEN_DAY_WAIT=$(( SEVEN_DAY_EPOCH - NOW + 120 ))
   [ "$FIVE_HR_WAIT" -lt 0 ] && FIVE_HR_WAIT=$(( FIVE_HR_WAIT + 86400 ))  # next day if already past
   [ "$SEVEN_DAY_WAIT" -lt 0 ] && SEVEN_DAY_WAIT=$(( SEVEN_DAY_WAIT + 604800 ))  # next week if already past
   
   # Use whichever window resets sooner
   if [ "$FIVE_HR_WAIT" -le "$SEVEN_DAY_WAIT" ]; then
     WAIT=$FIVE_HR_WAIT
     RESET_AT="$FIVE_HR_RESET"
   else
     WAIT=$SEVEN_DAY_WAIT
     RESET_AT="$SEVEN_DAY_RESET"
   fi
   echo "Sleeping ${WAIT}s (~$((WAIT/60)) min) until ${RESET_AT} + 2 min..."
   sleep "$WAIT"
   ```
7. After waking, verify usage has dropped, then **resume each agent with `"Continue."`** — the agent's own conversation history is intact, so it can pick up exactly where it left off without re-stating the task:
   ```bash
   # For each agent window NAME:
   tmux -S "$SOCKET" send-keys -t NAME:0.0 -l 'Continue.'
   tmux -S "$SOCKET" send-keys -t NAME:0.0 Enter
   ```
   Then resume the overseer loop.

Check usage every **5 cycles** (not every cycle) to avoid adding overhead.

## One Fleet Per Project — Never Repurpose

**Each project gets its own dedicated agents. Never redirect, reset, or repurpose agents from another project.**

### Use a single tmux session, with many windows - one window per agent

The preferred layout is **one session named `<project>-<feature>`, with one window per agent**:

Name the session after both the project and the feature being worked on — e.g. `fir-mcp`, `fir-acp`, `acp-claw-transport`. This makes the fleet immediately identifiable in `tmux-ai ls` without having to inspect windows.

```bash
# Create the session (first window becomes the researcher or first worker)
tmux -S "$SOCKET" new -d -s fir-mcp -n worker-1 -c "$WORKTREE"

# Add more windows
tmux -S "$SOCKET" new-window -t fir-mcp -n worker-2 -c "$WORKTREE"
tmux -S "$SOCKET" new-window -t fir-mcp -n reviewer -c "$WORKTREE"

# Turn off auto-rename globally
tmux -S "$SOCKET" set-option -t fir-mcp -g automatic-rename off
```

Address agents as `SESSION:WINDOW` (e.g. `fir-mcp:worker-1`, `fir-mcp:reviewer`).

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

If a session is working in a different project directory or worktree — **leave it alone**. Do not send it Escape, `/new`, `/compact`, or any task. It is not yours to manage. Worktree paths look like `/path/to/repo-wt-<feature>` — each fleet owns exactly one.

Spawn fresh sessions with `<project>-<feature>` names to avoid confusion:

```bash
# Good — name encodes project AND feature being worked on
tmux -S "$SOCKET" new -d -s fir-mcp      -c "$WORKTREE"  # fir, MCP feature work
tmux -S "$SOCKET" new -d -s fir-acp      -c "$WORKTREE"  # fir, ACP mode work
tmux -S "$SOCKET" new -d -s claw-transport -c "$WORKTREE"

# Bad — generic names that look reusable across projects
tmux -S "$SOCKET" new -d -s worker
tmux -S "$SOCKET" new -d -s fir           # too vague — which fir feature?
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
   until ls "$WORKTREE"/docs/plan/NN-feature.md 2>/dev/null; do sleep 15; done
   ```
3. **Read the plan yourself** before dispatching workers. Verify it answers: which files, which interfaces, which order.
4. **Then spawn workers**, each with a concrete task drawn directly from the plan's task breakdown. One task per agent, one commit's worth each.

**Do not** pre-load workers with "read code and wait for the plan" tasks — that burns tokens on redundant reading. Cold workers start faster and cheaper once the plan is ready.

## Rhythm

Poll every 10 seconds. Each cycle is three questions:

1. **Did anything land?** — `git -C "$WORKTREE" log --oneline -5`. Commits mean progress.
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
# Compact with auto-resume (preferred — retains summarised history and continues mid-task work)

# 1. Snapshot what the agent was doing before interrupting
SNAPSHOT=$(tmux -S "$SOCKET" capture-pane -p -J -t NAME:0.0 -S -30 \
  | grep -v '^$\|^─\|^⟩\|%/200k' | tail -5)

# 2. Interrupt and compact
tmux -S "$SOCKET" send-keys -t NAME:0.0 Escape
sleep 1
tmux -S "$SOCKET" send-keys -t NAME:0.0 -l '/compact'
tmux -S "$SOCKET" send-keys -t NAME:0.0 Enter

# 3. Wait for compaction to finish — poll until the prompt returns (spinner gone)
for i in $(seq 1 30); do
  sleep 2
  PANE=$(tmux -S "$SOCKET" capture-pane -p -J -t NAME:0.0 -S -3)
  echo "$PANE" | grep -qE '^\s*>\s*$|waiting for input|\$' && break
done

# 4. Resume — /compact already summarised the context, so "Continue." is sufficient.
#    The agent's summary includes what it was mid-doing; no need to restate the task.
#    Only include $SNAPSHOT if the summary seems thin (agent says "I don't know what I was doing").
tmux -S "$SOCKET" send-keys -t NAME:0.0 -l 'Continue.'
tmux -S "$SOCKET" send-keys -t NAME:0.0 Enter
# First response will show the new (lower) ctx %

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
tmux -S "$SOCKET" new -d -s NAME -n NAME -c "$WORKTREE"
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
