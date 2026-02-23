---
name: fix
description: Continuously pick up issues from the review agent's URGENT.md and BACKLOG.md and fix them. Handles build breaks, security issues, simplification, test gaps, and correctness bugs filed by the reviewer.
---

# Continuous Fixer

You are the fixer agent. The review agent writes issues to `docs/review/URGENT.md` and `docs/review/BACKLOG.md`. Your job is to pick them up one at a time, fix them, verify, and mark them done.

## Fix Loop

Each cycle follows this exact order:

### 0. Print the next reminder command

Before doing any work, output this as a plain code block so it's visible in the chat even if the session times out or the context window fills:

```
Next reminder command:
sleep 30 && echo "=== FIX CYCLE REMINDER === Re-read .fir/skills/fix/SKILL.md and follow its instructions to start the next cycle. Check docs/review/URGENT.md and docs/review/BACKLOG.md for new items."
```

### 1. Re-read this skill file

Re-read `.fir/skills/fix/SKILL.md` to keep instructions in context. Long-running agents drift — this is not optional.

### 2. Read the review queue

```bash
cat docs/review/URGENT.md 2>/dev/null
cat docs/review/BACKLOG.md 2>/dev/null
```

### 3. Pick ONE item

Priority order:
1. **URGENT.md** — build breaks, security, data loss. Always fix these first.
2. **BACKLOG.md → Security** — before they become urgent.
3. **BACKLOG.md → Correctness** — wrong behavior.
4. **BACKLOG.md → Test Coverage** — missing tests.
5. **BACKLOG.md → Simplification** — cleanup.

Pick the single highest-priority item that you haven't already attempted this cycle.

### 4. Check the file isn't being actively edited

```bash
find pkg/ cmd/ -name "*.go" -mmin -5 2>/dev/null | sort
```

If the target file was modified in the last 5 minutes, **skip it** — another agent is likely working on it. Pick the next item instead.

### 5. Read the file and understand the context

Always read the full file before editing. For items that reference a specific line, read the surrounding context too. If the item references a TS upstream source (in the file's header comment), consider reading that for comparison.

### 6. Fix it

Follow the project conventions:
- **Idiomatic Go.** Keep it simple.
- **One concern per edit.** Don't combine unrelated fixes.
- **Preserve file headers.** Every `.go` file has a `// Ported from:` comment — don't remove it.
- **Match existing style.** Read a neighboring file in the same package for patterns.

#### Fix types by category:

**Build breaks:** Fix the compile error. If the fix requires understanding code you didn't write, read the relevant files first. If the file is too broken to fix locally (e.g., depends on types that don't exist yet), note this in URGENT.md and move on.

**Security:** Apply the fix described in the review. For API key leaks, wrap errors to strip sensitive data. For path traversal, add validation. Keep changes minimal.

**Simplification:** Make the change described. When removing dead code, grep the repo first to confirm it's truly unused:
```bash
rg 'functionName' pkg/ cmd/ --type go
```

**Test coverage:** Write the missing test. Follow existing test patterns in the same `_test.go` file. Use table-driven tests where appropriate. Tests must:
- Actually assert meaningful behavior (not just `if err != nil`)
- Cover the specific code path identified in the review
- Pass: `go test ./path/to/package/...`

**Correctness:** Fix the bug. Read the TS source for reference. Add a regression test.

### 7. Verify

After every fix, run:

```bash
go vet ./... 2>&1
go test ./... 2>&1 | tail -30
```

If tests fail **in the package you edited**, your fix is wrong. Undo and retry.
If tests fail **in a different package**, that's not your problem — note it and continue.

### 8. Mark the item done

Re-read the review file (another agent may have edited it), then remove the fixed item:

```bash
cat docs/review/URGENT.md
cat docs/review/BACKLOG.md
```

Edit the file to remove the line/section you fixed. If you fixed the last item in a section, remove the section header too. Update the `**Last reviewed:**` date at the top of BACKLOG.md if you change it.

If the item turned out to be invalid (code was already fixed, or the review was wrong), remove it anyway and note why in your summary.

### 9. Report to the user

Tell the user what you fixed:
> Fixed: `pkg/core/tools/editdiff.go` — replaced `maxOf` with builtin `max()`. Tests pass.

### 10. Run the reminder command

```bash
sleep 30 && echo "=== FIX CYCLE REMINDER === Re-read .fir/skills/fix/SKILL.md and follow its instructions to start the next cycle. Check docs/review/URGENT.md and docs/review/BACKLOG.md for new items."
```

Use timeout 40 on the bash call. When you see the reminder output, immediately go back to step 0.

## Rules

- **One fix at a time.** Don't batch multiple unrelated changes. Fix, verify, mark done, repeat.
- **Don't create new issues.** If you spot something wrong while fixing, don't fix it — let the reviewer catch it on the next cycle. Stay focused on the queue.
- **Don't fight the reviewer.** If you disagree with a review item, remove it from the file and add a note explaining why (e.g., `<!-- Removed: X is intentional because Y -->`). The reviewer will re-evaluate on the next cycle.
- **Respect other agents.** Check for recent modifications before editing any file. Never overwrite work in progress.
- **Keep the build green.** If your fix breaks something, revert it immediately before moving on.
- **No items left behind.** Do not skip items or defer them as "low priority". Every item in the queue must be fixed or genuinely resolved. If an item requires cross-cutting changes across many files, break it down into smaller steps and do them one at a time. If an item needs mock infrastructure, build the minimum viable mock. If it's a design suggestion that truly cannot be acted on (e.g., "consider an opt-in sandbox"), remove it with a clear justification — but this should be rare. The goal is an empty queue.
