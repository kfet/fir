---
name: monitor
description: Continuously monitor a project's file activity, build health, and work-tracker progress. Loops every 30 seconds, reports changes, and flags stuck or broken states.
---

# Monitor Skill

Continually monitor the project for progress updates and report changes to the user.

> **`PROJECT_ROOT`** refers to the repository root. Set it at the start of every shell session:
> ```bash
> PROJECT_ROOT="$PWD"
> ```

## Configuration

Before starting, identify:
- **Work tracker file** — a checklist or plan document tracking work items (ask the user if unclear)
- **Source directories** — where code lives (e.g. `src/`, `pkg/`, `cmd/`, `lib/`)
- **Test command** — the project's test runner (e.g. `make test`, `go test ./...`, `npm test`)
- **File extension** — the primary language extension (e.g. `*.go`, `*.ts`, `*.py`)

## Monitoring Loop

Each cycle follows this exact order:

### 0. Print the next reminder command

Before doing any work, output this as a plain code block so it's visible in the chat even if the session times out or the context window fills:

```
Next reminder command:
sleep 30 && echo "=== MONITOR CYCLE REMINDER === Re-read the monitor skill file and follow the monitoring loop instructions for the next cycle."
```

### 1. Re-read this skill file

Re-read this skill file to keep instructions in context. Long-running agents drift — this is not optional.

### 2. Run the snapshot

Collect the current state of the project using the script in this skill's `scripts/` directory:

```bash
bash "$SKILL_DIR/scripts/snapshot.sh" "$PROJECT_ROOT"
```

Use timeout 40 on the bash tool call.

### 3. Report to the user

Analyze the snapshot output and report changes (see **Reporting Style** below).

### 4. Run the reminder command

```bash
sleep 30 && echo "=== MONITOR CYCLE REMINDER === Re-read the monitor skill file and follow the monitoring loop instructions for the next cycle."
```

Use timeout 40 on the bash call. When you see the reminder output, immediately go back to step 0.

## When to Re-read the Work Tracker

Re-read the work tracker every ~10 minutes (every 5th loop iteration) or whenever a burst of new files appears, to give the user a full status table.

## Reporting Style

- **No changes**: Report briefly "No changes, X files, all passing" and loop again
- **New files appeared**: List them, note which phase/item they belong to
- **Build failures**: Note them as "agent mid-work" — these are expected during active porting
- **All passing after failures**: Celebrate with ✅
- **Phase completed**: Highlight with 🎉


