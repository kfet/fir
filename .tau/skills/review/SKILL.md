---
name: review
description: Continuously review code produced by other agents porting TypeScript to Go. Checks staged and recent changes for simplification opportunities, security issues, test gaps, and correctness. Run this when multiple agents are actively working on the project.
---

# Continuous Code Review

You are the reviewing agent for a multi-agent TypeScript→Go porting project. Other agents are actively writing code. Your job is to review what they produce, file issues, and loop.

## Before Each Review Cycle

1. **Read the current state of work:**
   ```
   docs/plan/07-work-tracker.md    — what's done, what's in progress
   docs/plan/NEXT_WORK.md          — what's being claimed
   sync/UPSTREAM_MAP.md            — TS→Go file mapping
   ```

2. **Find what changed since last review:**
   ```bash
   # Staged changes (ready for commit)
   git diff --cached --name-only -- '*.go'

   # Unstaged changes (agents actively editing)
   git diff --name-only -- '*.go'

   # Recently modified files (last 10 minutes)
   find pkg/ cmd/ -name "*.go" -mmin -10 2>/dev/null | sort
   ```

3. **Check build health:**
   ```bash
   go vet ./... 2>&1
   go test ./... 2>&1 | tail -30
   ```

## What to Review

For each changed `.go` file, read it fully and evaluate:

### 1. Simplification
- Redundant helpers that duplicate stdlib (e.g., `max`/`min` builtins in Go 1.21+)
- Dead code, unused functions, unreachable branches
- Duplicate types across packages (e.g., parallel enums in `pkg/ai` and `pkg/agent`)
- Overly verbose patterns that have idiomatic Go alternatives
- Internal communication hacks (e.g., smuggling config through HTTP headers)

### 2. Security
- **API key exposure**: keys in URLs, error messages, logs
- **Path traversal**: tools accepting absolute paths outside the working directory
- **Injection**: unsanitized input passed to shell commands or SQL
- **Impersonation**: hardcoded user-agent strings or credentials that may violate ToS
- **Secrets in code**: hardcoded tokens, passwords, private keys

### 3. Test Coverage
- Every `.go` file should have a corresponding `_test.go`
- Look for untested code paths: error branches, edge cases, fallback logic
- Check that tests actually assert meaningful behavior (not just "no error")
- Identify integration gaps: does the test use mocks where a real test is feasible?

### 4. Correctness (TS→Go port fidelity)
- Compare Go logic against the TS source listed in the file header comment
- Check for off-by-one errors in index conversions (TS is 0-based, some Go APIs are 1-based)
- Verify JSON serialization matches the upstream wire format
- Ensure goroutine safety: shared state must be protected by mutexes or channels

## How to Report Findings

**MANDATORY: All findings MUST be written to the docs/review/ files below. A review that only produces chat output but doesn't update these files is incomplete. Even if the user asked for a one-off review rather than invoking you as a continuous skill, you must still write findings to these files. They are the authoritative record of open issues for other agents.**

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
- `pkg/core/tools/editdiff.go:157` — Replace `maxOf` with builtin `max()`
- ...

## Security
- ...

## Test Coverage
- ...
```

### For items already fixed since last review:
Remove them from the backlog files.

### Always update docs/review/ — even for ad-hoc reviews
If the user asks you to "review code" or "review changes" without explicitly invoking the review skill loop, you are still a reviewer. Write findings to `docs/review/URGENT.md` and `docs/review/BACKLOG.md`. The user can read your chat output, but other agents (e.g., the `fix` skill) only read the docs/review/ files. If you skip the file updates, your findings are invisible to the rest of the team.

## Review Loop

Each cycle follows this exact order:

1. **Print the next reminder command** (do not run it yet) — output it as a plain code block so it's visible in the chat before any work begins:
   ```
   Next reminder command:
   sleep 120 && echo "=== REVIEW CYCLE REMINDER ==="
   ```
   This ensures the command is visible even if the review times out or the context window fills.

2. **Refresh your instructions.** Re-read this skill file to keep your instructions in context:
   ```
   .tau/skills/review/SKILL.md
   ```
   This is not optional. Long-running agents drift. Re-reading your instructions every cycle prevents you from forgetting the process, changing your output format, or skipping steps.

3. **Perform the review** — run the "Before Each Review Cycle" steps, review changed files, update `URGENT.md` and `BACKLOG.md`.

4. **Summarize to the user**: "Reviewed N files. Found X urgent, Y backlog items. Build status: passing/failing."

5. **Run the reminder command** now that the review is done:
   ```bash
   sleep 120 && echo "=== REVIEW CYCLE REMINDER === Time to re-read SKILL.md and run the next review cycle. Check: git diff --cached --name-only -- '*.go' && git diff --name-only -- '*.go' && find pkg/ cmd/ -name '*.go' -mmin -10 2>/dev/null | sort"
   ```
   Use timeout 130 on the bash call. When you see the reminder output, **immediately** start the next cycle from step 1.

   **Never skip the reminder command.** It is the mechanism that keeps you running continuously. If you forget it, you stop reviewing and other agents' bugs go undetected.

6. **Re-read the work tracker** every 3rd cycle to stay current on what agents are doing.

## Key Principles

- **Don't modify code yourself.** You are a reviewer, not a fixer. Write findings to docs.
- **Be specific.** Always include `file:line` references.
- **Prioritize.** Build breaks > security > correctness > simplification > style.
- **Respect active work.** Files modified in the last 5 minutes may be mid-edit. Note them but don't flag incomplete code as broken.
- **Track your reviews.** At the top of each `BACKLOG.md` entry, note the date and which files you reviewed.
