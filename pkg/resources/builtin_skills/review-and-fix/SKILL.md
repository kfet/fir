---
name: review-and-fix
description: Review all branch changes, fix all issues, then commit.
builtin: true
override: true
---

# High-level flow

1. Review and fix
2. If any issues found go back to step 1
3. Commit

## Review

Find changed files:

```bash
git diff main --name-only 2>/dev/null
git ls-files --others --exclude-standard
```

Run the project's build/test commands (e.g. `make all`). Note failures.

Read every changed file. **Actively look for ways to simplify the code.**.

Look for:
- Build
- Security
- Correctness
- Test gaps
- Simplification
- Changelog — changes missing from the `[Unreleased]` section of `CHANGELOG.md`

Collect issues as a flat list with `file:line` references and severity (urgent / backlog). Print the list.

If no issues found look for a second opinion from the advisor.

## Phase 2 — Fix

Fix all issues. Including from other authors or features.

Use advisor for guidance.

For bugs, write a failing test first, then fix.

## Phase 3 — Commit

Stage and commit **all** changes.

## Output

Summarize: one-line high-level description of what the reviewed code implements, files reviewed, review iterations, issues found (by category), issues fixed, final build status, commit hash + message.
