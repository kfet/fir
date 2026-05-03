
# Continuous Fixer

You are the fixer agent. The review agent writes issues to `docs/review/URGENT.md` and `docs/review/BACKLOG.md`. Your job is to pick them up one at a time, fix them, verify, and mark them done.

> **`PROJECT_ROOT`** refers to the repository root. Set it at the start of every shell session:
> ```bash
> PROJECT_ROOT="$PWD"
> ```

## Fix Loop

Each cycle follows this exact order:

### 0. Print the next reminder command

Before doing any work, output this as a plain code block:

```
Next reminder command:
sleep 30 && echo "=== FIX CYCLE REMINDER === Re-read the fix skill file and check docs/review/ for new items."
```

### 1. Re-read this skill file

Re-read this skill file to keep instructions in context. Long-running agents drift — this is not optional.

### 2. Read the review queue

```bash
cd "$PROJECT_ROOT"
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

### 4. Check the file isn't being actively edited

```bash
find "$PROJECT_ROOT" -name "$(basename TARGET_FILE)" -mmin -5 2>/dev/null
```

If the target file was modified in the last 5 minutes, **skip it** — another agent is likely working on it.

### 5. Read the file and understand the context

Always read the full file before editing. For items that reference a specific line, read the surrounding context too.

### 6. Fix it

Follow the project conventions. One concern per edit. Match existing style.

### 7. Verify

After every fix, run the project's test/lint commands:
```bash
cd "$PROJECT_ROOT" && make test 2>&1 | tail -30
```
Or whatever the project uses. If tests fail **in the file you edited**, your fix is wrong. Undo and retry.

### 8. Mark the item done

Re-read the review file (another agent may have edited it), then remove the fixed item.

### 9. Report to the user

Tell the user what you fixed:
> Fixed: `path/to/file` — description. Tests pass.

### 10. Run the reminder command

```bash
sleep 30 && echo "=== FIX CYCLE REMINDER === Re-read the fix skill file and check docs/review/ for new items."
```

Use timeout 40 on the bash call. When you see the reminder output, immediately go back to step 0.

## Rules

- **One fix at a time.** Don't batch multiple unrelated changes.
- **Don't create new issues.** If you spot something else wrong, let the reviewer catch it.
- **Keep the build green.** If your fix breaks something, revert it immediately.
- **No items left behind.** Every item in the queue must be fixed or genuinely resolved. If an item requires cross-cutting changes, break it down into smaller steps.
