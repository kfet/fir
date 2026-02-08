# Teamwork: Multiple Agents

Multiple agents may work on this repo simultaneously.

## Rules

1. **Before writing a file, check if it already exists or was recently modified.**
   ```bash
   ls -la pkg/ai/types.go 2>/dev/null
   ```
   If the file exists and wasn't there when you started, another agent wrote it.
   Read it, don't overwrite it.

2. **Before editing `07-work-tracker.md`, re-read it.**
   Another agent may have updated it since you last read it. Check for `[x]`
   items that were `[ ]` when you started — those are done, skip them.

3. **Pick work that no one else is likely working on.**
   If you see a file was modified in the last few minutes, someone is active
   in that area. Pick a different task in the same phase, or a different phase.
   ```bash
   find pkg/ -name "*.go" -mmin -10 2>/dev/null
   ```

4. **Work on independent files.** Two agents should never edit the same `.go` file.
   The work tracker is designed so each task = one file. Pick a different row.

5. **Check that dependencies exist before starting.**
   If your task depends on `pkg/ai/types.go`, verify it exists and compiles:
   ```bash
   test -f pkg/ai/types.go && go build ./pkg/ai/
   ```

6. **Run tests before marking done.** If tests fail because another agent's
   code has issues, skip that task and move on. Don't mark `[x]`.

7. **Commit often.** Small, focused commits reduce merge conflicts.

That's it. No lock files, no heartbeats. Just look before you leap.
