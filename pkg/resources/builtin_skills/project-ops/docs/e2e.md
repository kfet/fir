
# End-to-End Testing

You are the E2E testing agent. Run the suite, interpret failures, report bugs to `docs/review/URGENT.md` (crashes/build breaks) and `docs/review/BACKLOG.md` (behavioral bugs). Don't modify source code.

## Running Tests

Use `timeout: 180`
```bash
make test-e2e
```

Repeat a single test if needed by using tag `-tags=e2e`. Tests are in `./tests/e2e/`.

## Test Cycle

Repeat continuously:

1. **Print the reminder** — output `sleep 30 && echo "=== E2E CYCLE REMINDER ==="` as a code block before starting.
2. **Re-read this skill** — call the read tool to keep a fresh copy in the context
3. **Check for active work** — `find pkg/ cmd/ -name "*.go" -mmin -2 | head -5`. If many files, skip the cycle.
4. **Run `make test-e2e`** (timeout: 180).
5. **On all-pass**, run `make all` (timeout: 300) to confirm nothing else broke.
6. **Report results** — summarize pass/fail counts. File failures:
   - Crashes/build breaks → `docs/review/URGENT.md`
   - Behavioral bugs → `docs/review/BACKLOG.md`
   - Remove entries for previously-failing tests that now pass.
7. **Run the reminder** — `sleep 30 && echo "=== E2E CYCLE REMINDER ==="` (timeout: 40), then go to step 1.

## Adding New Tests

1. Add `func TestXxx(t *testing.T)` with `//go:build e2e` tag to the appropriate file in `tests/e2e/`.
2. Use helpers from `helpers_test.go` (`runFirMock`, `parseJSONLines`, `findJSONLine`, etc.).
3. Verify with `make test-e2e`. Do NOT commit — report to the user.

## Rules

- Don't modify source code — write failures to `docs/review/`.
- Use the bash tool's `timeout` parameter on every call.
- Escalate crashes/panics immediately as URGENT.
- Don't re-file known issues — check existing entries first.
