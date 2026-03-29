---
name: work
description: Implement, build, refactor, or fix anything substantial — creates a git worktree and feature branch before any edits so work is isolated and reversible.
builtin: true
---

# Work

All non-trivial work happens in a **git worktree** on a feature branch. Never edit the main branch directly.

**If the task touches multiple packages or needs parallel work streams, use the [shepherd skill](../shepherd/SKILL.md) instead.** The shepherd will refer back to this skill for worktree setup, then coordinate multiple agents within it.

## Starting Work

```bash
PROJECT="$PWD"
FEATURE="<short-kebab-name>"          # e.g. acp-auth-methods
BRANCH="work/${FEATURE}"
WORKTREE="${PROJECT}-wt-${FEATURE}"

git -C "$PROJECT" worktree add "$WORKTREE" -b "$BRANCH"
cd "$WORKTREE"
```

All subsequent edits, tests, and commits happen in `$WORKTREE`.

## Research First

If the task needs design work, research and write a plan doc **in the worktree** before writing code. The plan should name specific files, interfaces, and test cases.

## During Work

- Run `make test` often.
- Commit incremental progress with clear messages.
- Finish with `make all` to confirm the full build passes.

## Finishing

1. Final `make all` in the worktree.
2. Commit everything.
3. Merge to main (or open a PR):
   ```bash
   git -C "$PROJECT" merge "$BRANCH"
   ```
4. Clean up:
   ```bash
   git -C "$PROJECT" worktree remove "$WORKTREE"
   git -C "$PROJECT" branch -d "$BRANCH"
   ```
