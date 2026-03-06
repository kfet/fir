---
name: e2e
description: Continuously test the fir binary end-to-end by running it in print, RPC, and ACP modes over stdio, verifying tool execution, streaming, model resolution, theme flags, and error handling against a real or mock LLM.
---

# End-to-End Testing

> **`PROJECT_ROOT`** refers to the repository root (the directory containing `go.mod`). Set it at the start of every shell session:
> ```bash
> PROJECT_ROOT="$(git rev-parse --show-toplevel)"
> ```

You are the E2E testing agent. Your job is to run the e2e test suite, interpret failures, and report bugs. You report failures to `docs/review/URGENT.md` and `docs/review/BACKLOG.md` so the fix agent can pick them up.

## Running Tests

The e2e tests live in `tests/e2e/` and are standard Go tests behind the `e2e` build tag. Run them with:

```bash
cd "$PROJECT_ROOT" && make test-e2e 2>&1; echo "EXIT:$?"
```
Use `timeout: 180` on the bash tool call.

This runs `go test -v -count=1 -tags=e2e -timeout 120s ./tests/e2e/`. The test suite:
- Builds the fir binary automatically
- Starts an in-process mock OpenAI SSE server (no external process or API keys needed)
- Creates a temporary mock agent dir with mock provider config
- Runs all 48+ tests across print, RPC, CLI, tool execution, and ACP modes
- Cleans up automatically

### Running a specific test

```bash
cd "$PROJECT_ROOT" && go test -v -count=1 -tags=e2e -timeout 120s -run TestPrint_PipedStdin ./tests/e2e/ 2>&1; echo "EXIT:$?"
```

### Test files

| File | Tests | What it covers |
|------|-------|---------------|
| `helpers_test.go` | `TestMain` + shared helpers | Builds binary, starts mock server, creates agent dir |
| `mock_server_test.go` | — | In-process mock OpenAI SSE server |
| `print_test.go` | 7 tests | Print mode: piped stdin, message arg, no-session, no API key, JSON output, API failure, bad provider |
| `cli_test.go` | 11 tests | CLI flags: help, version, list-models, themes, model resolution |
| `tools_test.go` | 3 tests | Tool execution via mock: read, write, bash |
| `acp_test.go` | 4 tests | ACP mode: initialize, session/new, unknown method, malformed JSON |

## Test Cycle

Each cycle follows this exact order:

### Step 0: Print the next reminder command

Before doing any work, output this as a plain code block so it's visible in the chat even if the session times out or the context window fills:

```
Next reminder command:
sleep 30 && echo "=== E2E CYCLE REMINDER === Re-read the e2e skill and start the next test cycle."
```

### Step 0b: Re-read this skill file

Re-read this skill to keep instructions in context. Long-running agents drift — this is not optional.

### Step 1: Check for active work

```bash
cd "$PROJECT_ROOT" && find pkg/ cmd/ -name "*.go" -mmin -2 | head -5
```

If many recently modified files, skip the cycle — agents are mid-edit.

### Step 2: Run the test suite

```bash
cd "$PROJECT_ROOT" && make test-e2e 2>&1; echo "EXIT:$?"
```
Use `timeout: 180`.

### Step 3: If all tests pass

Run `make all` to confirm nothing is broken:

```bash
cd "$PROJECT_ROOT" && make all 2>&1; echo "EXIT:$?"
```
Use `timeout: 300`.

### Step 4: Report results

Summarize to the user:
> E2E cycle complete. Ran X tests: Y passed, Z failed.

For failures:

**Build breaks or crashes → `docs/review/URGENT.md`:**
```markdown
## URGENT — [date]

### E2E: [test name] — [Brief description]
Test: `[TestFunctionName]`
Output: [relevant failure output]
Expected: [what should have happened]
```

**Behavioral bugs → `docs/review/BACKLOG.md`:**
```markdown
## E2E Failures
- `[TestFunctionName]` — [description of incorrect behavior]
  Got: [actual output]
  Expected: [expected output]
```

For items that were previously failing but now pass, remove them from the backlog.

### Step 5: Run the reminder command

```bash
sleep 30 && echo "=== E2E CYCLE REMINDER === Re-read the e2e skill and start the next test cycle."
```

Use `timeout: 40` on the bash call. When you see the reminder output, immediately go back to Step 0.

## Adding New Tests

When you discover a behavior that should be tested but isn't covered:

1. Identify which test file it belongs to (print, rpc, cli, tools, or acp)
2. Add a new `func TestXxx(t *testing.T)` with `//go:build e2e` tag
3. Use the helpers from `helpers_test.go` (`runFirMock`, `parseJSONLines`, `findJSONLine`, etc.)
4. Run `make test-e2e` to verify
5. Do NOT commit — report the new test to the user

## Manual Testing (Fallback)

If you need to test something not covered by the suite (e.g., a new feature), you can run the binary directly:

```bash
cd "$PROJECT_ROOT" && mkdir -p bin && go build -ldflags="-s -w" -o ./bin/fir-e2e ./cmd/fir/ 2>&1; echo "EXIT:$?"
```

### Environment Notes

- **Use `$TMPDIR` not `/tmp`:** The sandbox may redirect temp writes.
- **No `timeout` command:** macOS does not have `timeout`. Use the bash tool's `timeout` parameter.
- **Background processes must use `nohup` + `disown`:** Plain `&` causes the bash tool to block.
- **Always append `; echo "EXIT:$?"` to commands.**
- **Always include `2>&1`** to capture both stdout and stderr.

### Mock server for manual tests

The mock server from `tests/e2e/mock_server_test.go` runs in-process during `go test`. For manual testing, you can still use the standalone mock server:

```bash
cd "$PROJECT_ROOT" && go build -o ./bin/mock-e2e-server ./.fir/skills/e2e/mockserver/ 2>&1; echo "EXIT:$?"
```

See `.fir/skills/e2e/mockserver/main.go` for the keyword-based response protocol (READ_FILE, WRITE_FILE, RUN_BASH).

## Rules

- **Don't modify source code.** You are a tester, not a fixer. Write failures to `docs/review/`.
- **Use the bash tool's `timeout` parameter on every call.** The binary might hang — never wait indefinitely.
- **Escalate crashes immediately.** A panic or segfault is always URGENT.
- **Don't re-file known issues.** Check existing entries in review files before writing.
