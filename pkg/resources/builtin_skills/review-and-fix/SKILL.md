---
name: review-and-fix
description: Run a single code review pass over recent changes, fix all issues found, verify the build passes, then commit the result. One-shot review + fix + commit cycle — not continuous.
---

# Review, Fix, and Commit (One-Shot)

Run one review pass over recent changes, fix every issue found, verify the build passes, then commit all changes.

> **`PROJECT_ROOT`** refers to the repository root. Set it at the start of every shell session:
> ```bash
> PROJECT_ROOT="$(git rev-parse --show-toplevel)"
> ```

## Phase 1 — Review

### 1. Find what changed

```bash
cd "$PROJECT_ROOT"
git diff --cached --name-only
git diff --name-only
git diff main --name-only 2>/dev/null
```

### 2. Check build health

Run the project's build/test commands (e.g. `make all`). Note any failures.

### 3. Review each changed file

Read every changed file fully. Look for:

- **Build breaks** — compilation errors, missing imports, broken tests
- **Security** — API key exposure, path traversal, injection, hardcoded secrets
- **Correctness** — off-by-one, race conditions, wrong logic, serialization mismatches
- **Test gaps** — untested code paths, error branches, missing assertions
- **Simplification** — dead code, redundant helpers, duplicate types, verbose patterns. Three dimensions:
  - *Code reuse* — eliminate duplication, extract shared helpers
  - *Code quality* — improve readability, naming, structure, adherence to project conventions
  - *Efficiency* — remove unnecessary allocations, redundant work, or slow patterns

### 4. Compile a findings list

Collect all issues as a flat list with `file:line` references and severity (urgent / backlog). Print the list to the user.

If no issues are found, say so and skip to Phase 3.

## Phase 2 — Fix

Work through every issue from the findings list, prioritized:

1. Build breaks
2. Security
3. Correctness
4. Test gaps
5. Simplification

For each issue:

1. Read the relevant file and surrounding context.
2. Make the fix.
3. If fixing a bug, write a test first that fails, then apply the fix so it passes.
4. Run the project's test command to confirm nothing broke.

After all issues are fixed:

```bash
cd "$PROJECT_ROOT"
make all
```

Confirm the full build and test suite passes. If anything fails, fix it before proceeding to Phase 3.

## Phase 3 — Commit

Stage and commit all changes made during the fix phase.

```bash
cd "$PROJECT_ROOT"
git add -A
```

Write a commit message that summarizes the fixes made. Use a short subject line (≤72 chars) followed by a blank line and a bullet list of changes if there are multiple fixes:

```bash
GIT_EDITOR=true git commit -m "<subject>

- <fix 1>
- <fix 2>
..."
```

If there were no issues found and nothing was changed, skip the commit.

## Output

Summarize to the user:
- How many files reviewed
- How many issues found (by category)
- How many fixed
- Final build status
- Commit hash and message (or "nothing to commit")
