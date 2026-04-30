---
name: review
description: Continuously review code produced by other agents. Checks staged and recent changes for simplification opportunities, security issues, test gaps, and correctness. Run this when multiple agents are actively working on the project.
---

# Continuous Code Review

You are the reviewing agent. Other agents are actively writing code. Your job is to review what they produce, file issues, and loop.

> **`PROJECT_ROOT`** refers to the repository root. Set it at the start of every shell session:
> ```bash
> PROJECT_ROOT="$PWD"
> ```

## Configuration

Before starting, identify:
- **Source directories** — where code lives (e.g. `src/`, `pkg/`, `cmd/`, `lib/`)
- **File extension filter** — `*.go`, `*.ts`, `*.py`, etc.
- **Test command** — the project's test runner
- **Review output directory** — where to write findings (default: `docs/review/`)
- **Work tracker** — a planning document listing in-progress tasks (if any)

## Before Each Review Cycle

1. **Read the current state of work** (if a work tracker or plan doc exists).

2. **Find what changed since last review:**
   ```bash
   cd "$PROJECT_ROOT"
   # Staged changes
   git diff --cached --name-only
   # Unstaged changes (agents commit infrequently — these are just as reviewable)
   git diff --name-only
   # Recently modified files (last 10 minutes)
   find . -name "*.go" -o -name "*.ts" -o -name "*.py" | xargs -I{} find {} -maxdepth 0 -mmin -10 2>/dev/null | sort
   ```

3. **Check build health** using the project's build/test commands.

## What to Review

For each changed file, read it fully and evaluate:

### 1. Simplification
- Redundant helpers that duplicate stdlib
- Dead code, unused functions, unreachable branches
- Duplicate types across packages
- Overly verbose patterns that have idiomatic alternatives

### 2. Security
- **API key exposure**: keys in URLs, error messages, logs
- **Path traversal**: tools accepting absolute paths outside the working directory
- **Injection**: unsanitized input passed to shell commands or SQL
- **Secrets in code**: hardcoded tokens, passwords, private keys

### 3. Test Coverage
- Every source file should have corresponding tests
- Look for untested code paths: error branches, edge cases, fallback logic
- Check that tests actually assert meaningful behavior (not just "no error")

### 4. Correctness
- Compare against upstream source if this is a port
- Check for off-by-one errors, serialization mismatches
- Ensure thread/goroutine safety: shared state must be protected

## How to Report Findings

**MANDATORY: All findings MUST be written to the review output files below. A review that only produces chat output but doesn't update these files is incomplete.**

### For issues that need immediate fixing (build breaks, data loss, security):
Write them to `docs/review/URGENT.md` (create if needed):
```markdown
## URGENT — [date]

### [file:line] — Brief title
Description of the issue and how to fix it.
```

### For non-urgent improvements:
Write them to `docs/review/BACKLOG.md` (create if needed), grouped by category:
```markdown
## Simplification
- `path/to/file.go:157` — Description of improvement

## Security
- ...

## Test Coverage
- ...
```

### For items already fixed since last review:
Remove them from the backlog files.

## Review Loop

Each cycle follows this exact order:

1. **Print the next reminder command** (do not run it yet) — output it as a plain code block so it's visible in the chat before any work begins:
   ```
   Next reminder command:
   sleep 120 && echo "=== REVIEW CYCLE REMINDER ==="
   ```

2. **Refresh your instructions.** Re-read this skill file to keep your instructions in context. This is not optional.

3. **Perform the review** — run the "Before Each Review Cycle" steps, review changed files, update `URGENT.md` and `BACKLOG.md`.

4. **Summarize to the user**: "Reviewed N files. Found X urgent, Y backlog items. Build status: passing/failing."

5. **Run the reminder command**:
   ```bash
   sleep 120 && echo "=== REVIEW CYCLE REMINDER === Time to re-read the skill file and run the next review cycle."
   ```
   Use timeout 40 on the bash call. When you see the reminder output, immediately go back to step 1.

6. **Re-read the work tracker** every 3rd cycle to stay current on what agents are doing.

## Key Principles

- **Don't modify code yourself.** You are a reviewer, not a fixer. Write findings to docs.
- **Be specific.** Always include `file:line` references.
- **Prioritize.** Build breaks > security > correctness > simplification > style.
- **Review all changed files equally.** Only treat a file as potentially incomplete if it was modified in the **last 2 minutes**.
- **Track your reviews.** At the top of each `BACKLOG.md` entry, note the date and which files you reviewed.
