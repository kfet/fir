---
name: review-and-fix
description: Review all branch changes, fix all issues, then commit. Loops until a pass finds zero new issues.
builtin: true
override: true
---

# High-level flow

Drive the review/fix repeat with the **`loop`** skill in **condition mode**.
Exit predicate: **the last pass found zero new issues**. The loop's per-cycle
banner keeps the iteration from collapsing into a single pass — that single-pass
collapse is the failure this skill exists to prevent.

Banner slugs (these are exactly the section headings below, so the banner indexes
them): `correctness` `security` `simplify` `test-coverage` `changelog` `build`.

Each loop cycle runs one full Review pass then one Fix pass. Exit only when a
Review pass surfaces nothing.

## Review

Start from the changed files, but **fix anything anywhere** — issues in
untouched code, other features, or other authors' work are all in scope.

Find changed files:

```bash
git diff main --name-only 2>/dev/null
git ls-files --others --exclude-standard
```

Read every changed file in full. Then look for:

### correctness
Logic errors, edge cases, races, misuse of APIs.

### security
Injection, unsafe input handling, leaked secrets, unchecked privilege.

### simplify
**Simplify.** Delete dead code, collapse needless abstraction, remove premature
generality and redundant layers. Deletion is a feature.

### test-coverage
Untested paths, missing failing-first tests for bugs, gaps in edge cases.

### changelog
Changes missing from the `[Unreleased]` section of `CHANGELOG.md`.

### build
Run the project's build/test commands (e.g. `make all`). Note failures.

Collect issues as a flat list with `file:line` references and severity (urgent /
backlog). Print the list. If a pass finds nothing, get a second opinion from the
advisor before exiting the loop.

## Fix

Fix all issues found this pass, including from other authors or features. Use the
advisor for guidance. For bugs, write a failing test first, then fix.

## Commit

When the loop exits clean, stage and commit **all** changes.

## Output

Summarise: one-line description of what the reviewed code implements, files
reviewed, loop cycles run, issues found (by category), issues fixed, final build
status, commit hash + message.
