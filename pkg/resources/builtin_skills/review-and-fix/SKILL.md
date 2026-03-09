---
name: review-and-fix
description: Run a single code review pass over recent changes, then immediately fix all issues found. One-shot review + fix cycle — not continuous.
---

# Review and Fix (One-Shot)

Run one review pass over recent changes, fix every issue found, and verify the build passes.

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
- **Simplification** — dead code, redundant helpers, duplicate types, verbose patterns

### 4. Compile a findings list

Collect all issues as a flat list with `file:line` references and severity (urgent / backlog). Print the list to the user.

If no issues are found, say so and stop.

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

Confirm the full build and test suite passes. If anything fails, fix it before finishing.

## Output

Summarize to the user:
- How many files reviewed
- How many issues found (by category)
- How many fixed
- Final build status
