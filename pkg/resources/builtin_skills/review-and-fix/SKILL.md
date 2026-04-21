---
name: review-and-fix
description: Review all branch changes, fix issues in a loop until clean, then commit. Iterates review→fix→recheck until no issues remain.
builtin: true
---

# Review, Fix, and Commit

Review all changes on the branch, fix every issue found, re-review until clean, then commit.

## Phase 1 — Review

Find changed files:

```bash
git diff main --name-only 2>/dev/null
git ls-files --others --exclude-standard
```

Run the project's build/test commands (e.g. `make all`). Note failures.

Read every changed file. **Actively look for ways to simplify the code.** Look for:

- **Build breaks** — compilation errors, missing imports, broken tests
- **Security** — key exposure, path traversal, injection, hardcoded secrets
- **Correctness** — off-by-one, races, wrong logic, serialization mismatches
- **Test gaps** — untested code paths, missing assertions
- **Simplification** — dead code, duplication, verbose patterns, unnecessary allocations
- **Changelog** — commits on the branch missing from the `[Unreleased]` section of `CHANGELOG.md`

Collect issues as a flat list with `file:line` references and severity (urgent / backlog). Print the list.

If no issues found, skip to Phase 3.

## Phase 2 — Fix

Fix all issues.

Work through findings in priority order: build breaks → security → correctness → test gaps → simplification.

For each issue: read context, make the fix, run tests. For bugs, write a failing test first, then fix.

After all fixes, run `make all` and confirm it passes.

**Return to Phase 1.** Fixes can introduce new issues. Re-review all changed files (including newly modified ones) and repeat until a review pass finds zero issues.

## Phase 3 — Commit

Only entered when Phase 1 finds no issues.

Stage and commit **all** outstanding changes — the original work plus any fixes.

```bash
git add -A
GIT_EDITOR=true git commit -m "<subject ≤72 chars>

- <change 1>
- <change 2>"
```

Always commit. Only skip if the working tree is truly clean.

## Output

Summarize: one-line high-level description of what the reviewed code implements, files reviewed, review iterations, issues found (by category), issues fixed, final build status, commit hash + message.
