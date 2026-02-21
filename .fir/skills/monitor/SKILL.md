---
name: monitor
description: Continually monitor the project for updates
---

# Monitor Skill

Continually monitor the fir project for progress updates and report changes to the user.

> **`PROJECT_ROOT`** refers to the repository root (the directory containing `go.mod`). Set it at the start of every shell session:
> ```bash
> PROJECT_ROOT="$(git rev-parse --show-toplevel)"
> ```

## What to Monitor

- `docs/plan/07-work-tracker.md` — the master checklist of all work items
- New/modified `.go` files appearing in `pkg/` and `cmd/`
- Build and test status across all packages

## Monitoring Loop

Each monitoring cycle has two steps:

### Step 1: Run the snapshot command

This command sleeps for 2 minutes, then collects the snapshot, then prints a prompt that tells you to re-read this skill file and continue the loop:

```bash
sleep 30 && cd "$PROJECT_ROOT" && echo "=== SNAPSHOT @ $(date '+%H:%M:%S') ===" && echo "--- Modified (last 3 min) ---" && find pkg/ cmd/ -name "*.go" -mmin -3 2>/dev/null | sort && echo "--- Total Go files: $(find pkg/ cmd/ -name '*.go' | wc -l | tr -d ' ') ---" && echo "--- Tests ---" && go test ./... 2>&1 | tail -15 && echo "" && echo ">>> Re-read .fir/skills/monitor/SKILL.md and follow the monitoring loop instructions for the next cycle."
```

Use timeout 40 on the bash call.

### Step 2: Report and loop

After the command completes:
1. Analyze the snapshot output and report changes to the user (see **Reporting Style** below).
2. Immediately re-read this skill file (`.fir/skills/monitor/SKILL.md`) and execute **Step 1** again.

This creates a self-sustaining loop: sleep → snapshot → report → re-read skill → sleep → …

## When to Re-read the Work Tracker

Re-read `docs/plan/07-work-tracker.md` every ~10 minutes (every 5th loop iteration) or whenever a burst of new files appears, to give the user a full phase-by-phase status table like:

| Phase | Done / Total | Status |
|---|---|---|
| Phase 0: Scaffolding | 5/5 | ✅ COMPLETE |
| Phase 1: AI Layer | 16/16 | ✅ COMPLETE |
| ... | ... | ... |

## Reporting Style

- **No changes**: Report briefly "No changes, X files, all passing" and loop again
- **New files appeared**: List them, note which phase/item they belong to
- **Build failures**: Note them as "agent mid-work" — these are expected during active porting
- **All passing after failures**: Celebrate with ✅
- **Phase completed**: Highlight with 🎉

## Key Files

- Work tracker: `docs/plan/07-work-tracker.md`
- Agent resume guide: `docs/plan/08-agent-resume.md`
- Available work assignments: `docs/plan/NEXT_WORK.md` (update this when items become unblocked)

## Propose Process Changes

As you monitor the project, consider proposing changes to the process to improve efficiency and reduce errors, conflicts, context, and resource usage.
