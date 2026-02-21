---
name: e2e
description: Continuously test the fir binary end-to-end by running it in print and RPC modes over stdio, verifying tool execution, streaming, and error handling against a real or mock LLM.
---

# End-to-End Testing via stdio

> **`PROJECT_ROOT`** refers to the repository root (the directory containing `go.mod`). Set it at the start of every shell session:
> ```bash
> PROJECT_ROOT="$(git rev-parse --show-toplevel)"
> ```

You are the E2E testing agent. Your job is to build the `fi` binary, run it in print mode and RPC mode, pipe commands and responses over stdio, and verify correct behavior. You report failures to `docs/review/URGENT.md` and `docs/review/BACKLOG.md` so the fix agent can pick them up.

## Environment Notes

These constraints were discovered during testing and MUST be followed:

- **Build output path:** `/tmp/` root is NOT writable in this sandbox (only `$TMPDIR` aka `/tmp/claude/` is). Always build to `./bin/fir-e2e` inside the project directory.
- **Use `$TMPDIR` not `/tmp`:** The sandbox redirects temp writes to `$TMPDIR` (e.g. `/tmp/claude/`). Never hardcode `/tmp/` — always use `$TMPDIR`.
- **No `timeout` command:** macOS does not have `timeout` or `gtimeout`. Instead, use the **bash tool's `timeout` parameter** on every call. Never use `timeout` as a shell command.
- **`env -u` requires `bash -c` wrapping:** To properly unset env vars for a subprocess, use: `env -u VAR bash -c 'command'`
- **Background processes must use `nohup` + `disown`:** Plain `&` causes the bash tool to block waiting for child processes. Always use `nohup ./cmd > logfile 2>&1 & disown $!` when starting long-running background processes.
- **Always append `; echo "EXIT:$?"` to commands** so you can verify the exit code in the output.
- **Always include `2>&1`** to capture both stdout and stderr (unless you specifically need to separate them).

## Prerequisites

Before each test cycle, ensure the binary builds:

```bash
cd "$PROJECT_ROOT" && mkdir -p bin && go build -ldflags="-s -w" -o ./bin/fir-e2e ./cmd/fir/ 2>&1; echo "EXIT:$?"
```

Use `timeout: 60` on the bash tool call. If the build fails, write the error to `docs/review/URGENT.md` as a build break and sleep until the next cycle.

## Mock Provider Setup

**All LLM-dependent tests use a local mock server** so no real API keys are needed.

The mock server is a small Go program at `.fir/skills/e2e/mockserver/main.go` that implements the OpenAI Chat Completions SSE protocol. It:
- Listens on a random TCP port (or `MOCK_PORT` env var) and prints `MOCK_PORT=<port>` to stdout
- Writes the port number to `$TMPDIR/mock-e2e-port` for the test harness to read
- Returns canned streaming responses
- Supports tool calls when the prompt contains keywords (`READ_FILE`, `WRITE_FILE`, `RUN_BASH`)
- After tool results are returned, responds with a text summary containing `MOCK_TOOL_DONE`

### Network requirement

The mock server needs to bind a TCP port on `127.0.0.1`. If the environment blocks `bind()` (sandboxed CI, etc.), the mock approach will fail. In that case:

1. **Skip LLM-dependent tests** (Steps 4 and tool tests) and only run fast tests (Step 3).
2. **Check for real API keys** as a fallback: if any `*_API_KEY` env var is set, use that provider for LLM tests with the commands from the "Real Provider Fallback" section below.
3. Log a warning: `"Mock server unavailable — network restricted. Running non-LLM tests only."`

### Step 0: Build and start the mock server

```bash
cd "$PROJECT_ROOT" && mkdir -p bin && go build -o ./bin/mock-e2e-server ./.fir/skills/e2e/mockserver/ 2>&1; echo "EXIT:$?"
```
Use `timeout: 30`.

Start the mock server in the background:

**Important:** Use `nohup` + `disown` to truly detach the process. A plain `&` causes the bash tool to block waiting for child processes.

```bash
cd "$PROJECT_ROOT"
PORTFILE="$TMPDIR/mock-e2e-port"
LOGFILE="$TMPDIR/mock-e2e-server.log"
rm -f "$PORTFILE"
nohup ./bin/mock-e2e-server > "$LOGFILE" 2>&1 &
MOCK_PID=$!
disown $MOCK_PID
sleep 1
MOCK_PORT=$(cat "$PORTFILE" 2>/dev/null)
if [ -z "$MOCK_PORT" ]; then
  echo "MOCK_UNAVAILABLE=1"
  echo "Server log: $(cat $LOGFILE 2>/dev/null)"
  kill $MOCK_PID 2>/dev/null
  MOCK_PID=""
else
  echo "Mock server PID=$MOCK_PID PORT=$MOCK_PORT"
  echo "MOCK_UNAVAILABLE=0"
fi
echo "EXIT:$?"
```
Use `timeout: 10`.

If `MOCK_UNAVAILABLE=1`, skip to the "Real Provider Fallback" check below.

### Setting up the mock agent dir

Create a temporary agent dir with a `models.json` that defines a mock provider:

```bash
MOCK_AGENT_DIR=$(mktemp -d)
cat > "$MOCK_AGENT_DIR/models.json" << ENDMODELS
{
  "providers": {
    "mock": {
      "baseUrl": "http://localhost:${MOCK_PORT}",
      "apiKey": "mock-key",
      "api": "openai-completions",
      "models": [
        {
          "id": "mock-model",
          "name": "Mock Model",
          "contextWindow": 128000,
          "maxTokens": 4096
        }
      ]
    }
  }
}
ENDMODELS
echo "MOCK_AGENT_DIR=$MOCK_AGENT_DIR"
```

### Running tests with mock

All LLM-dependent commands use this pattern:

```bash
FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model [other flags] 2>&1; echo "EXIT:$?"
```

### Teardown

After all tests complete:

```bash
kill $MOCK_PID 2>/dev/null; rm -f ./bin/mock-e2e-server "$TMPDIR/mock-e2e-port"; rm -rf "$MOCK_AGENT_DIR"
```

### Real Provider Fallback

If the mock server cannot start (network restricted), check for real API keys:

```bash
[ -n "$ANTHROPIC_API_KEY" ] && echo "ANTHROPIC" || true
[ -n "$OPENAI_API_KEY" ] && echo "OPENAI" || true
[ -n "$GEMINI_API_KEY" ] && echo "GEMINI" || true
```

If any key is available, run LLM tests using the real provider (omit `--provider mock --model mock-model` and `FIR_AGENT_DIR` overrides). The real-provider variants are the same commands but without the mock env vars, e.g.:

```bash
cd "$PROJECT_ROOT" && echo "What is 2+2?" | ./bin/fir-e2e --no-session -p 2>&1; echo "EXIT:$?"
```

Use `timeout: 30` for real-provider tests (they are slower). Verify the same conditions — just note that response text will be from the real LLM, not "MOCK_RESPONSE".

If **no API keys and no mock server**, report that LLM tests were skipped and only run fast tests.

## Test Modes

### 1. Print Mode Tests (`-p`)

Print mode sends a prompt and exits. Test it by piping input and checking stdout/stderr.

**Note:** Piping stdin into the binary automatically triggers print mode (the binary's `readPipedStdin()` consumes it and sets `-p`). You do NOT need to pass `-p` explicitly when piping, but it's harmless to include it.

#### 1a. Print mode with piped stdin (mock)

```bash
cd "$PROJECT_ROOT" && echo "What is 2+2?" | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --no-session -p 2>&1; echo "EXIT:$?"
```
Use `timeout: 15` on the bash tool call.

**Verify:**
- Exit code 0
- Stdout contains a response (non-empty)
- No panic/stack trace in stderr

#### 1b. Print mode with message argument (mock)

```bash
cd "$PROJECT_ROOT" && FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --no-session -p "Say exactly: HELLO_E2E_TEST" 2>&1; echo "EXIT:$?"
```
Use `timeout: 15` on the bash tool call.

**Verify:**
- Exit code 0
- Stdout contains "MOCK_RESPONSE" (the mock server's canned reply)
- No panic/stack trace

#### 1c. Print mode with `--no-session` (ephemeral, mock)

```bash
cd "$PROJECT_ROOT" && echo "Say OK" | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --no-session -p 2>&1; echo "EXIT:$?"
```
Use `timeout: 15` on the bash tool call.

**Verify:**
- Exit code 0
- No session files created in the working directory

#### 1d. Print mode error handling (no API key)

```bash
cd "$PROJECT_ROOT" && env -u ANTHROPIC_API_KEY -u OPENAI_API_KEY -u GEMINI_API_KEY -u GROQ_API_KEY -u XAI_API_KEY -u OPENROUTER_API_KEY -u MISTRAL_API_KEY -u AWS_PROFILE bash -c 'echo "test" | ./bin/fir-e2e -p 2>&1'; echo "EXIT:$?"
```
Use `timeout: 15` on the bash tool call.

**Verify:**
- Exit code non-zero OR stderr contains "No models available" / "API key" / "Forbidden"
- No panic/stack trace

#### 1e. Print mode JSON output (mock)

```bash
cd "$PROJECT_ROOT" && echo "Say hello" | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --no-session --mode json 2>&1; echo "EXIT:$?"
```
Use `timeout: 15` on the bash tool call.

**Verify:**
- Each stdout line is valid JSON
- At least one event has an agent-related type

### 2. RPC Mode Tests (`--mode rpc`)

RPC mode reads JSON commands from stdin and writes JSON responses/events to stdout. This is the richest testing surface.

**Important:** RPC mode blocks reading stdin. You must write commands and then close stdin (or use a timeout) to avoid hanging. Use the bash tool's `timeout` parameter — do NOT use the `timeout` shell command (it doesn't exist on macOS).

#### 2a. Basic RPC: get_state

```bash
cd "$PROJECT_ROOT" && echo '{"id":"1","type":"get_state"}' | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 15` on the bash tool call.

**Verify:**
- At least one JSON line in stdout
- One line has `"command":"get_state"` and `"success":true`
- Response `data` contains `model`, `thinkingLevel`, `isStreaming` fields

#### 2b. RPC: prompt → events → response (mock)

**Important:** The `prompt` command launches a goroutine (fire-and-forget). Stdin must stay open long enough for the agent to complete before EOF causes the server to exit. Use `{ printf ...; sleep 5; }` to hold stdin open.

```bash
cd "$PROJECT_ROOT" && { printf '{"id":"1","type":"prompt","message":"Say exactly: RPC_TEST_OK"}\n'; sleep 5; } | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 15` on the bash tool call.

**Verify:**
- Multiple JSON lines appear (streaming events + response)
- At least one line contains `"type":"agent_start"` or `"type":"message_start"` (from the event subscription)
- A line with `"type":"agent_end"` appears after the agent completes
- No panic in stderr

#### 2c. RPC: get_available_models (mock)

```bash
cd "$PROJECT_ROOT" && echo '{"id":"1","type":"get_available_models"}' | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 15` on the bash tool call.

**Verify:**
- Response has `"command":"get_available_models"` and `"success":true`
- `data.models` is an array containing at least the mock model

#### 2d. RPC: set_thinking_level (mock)

```bash
cd "$PROJECT_ROOT" && printf '{"id":"1","type":"set_thinking_level","level":"high"}\n{"id":"2","type":"get_state"}\n' | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 15` on the bash tool call.

**Verify:**
- First response: `"command":"set_thinking_level"`, `"success":true`
- Second response: get_state shows `"thinkingLevel":"high"`

#### 2e. RPC: unknown command

```bash
cd "$PROJECT_ROOT" && echo '{"id":"1","type":"bogus_command"}' | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Response has `"success":false` and `"error"` contains "Unknown command"

#### 2f. RPC: malformed JSON

```bash
cd "$PROJECT_ROOT" && echo 'this is not json' | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Response has `"success":false` and `"error"` contains "parse"
- Process does NOT crash

#### 2g. RPC: abort (mock)

```bash
cd "$PROJECT_ROOT" && { printf '{"id":"1","type":"prompt","message":"Write a very long essay about the history of mathematics"}\n'; sleep 3; } | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Process exits cleanly when stdin closes (after sleep expires)
- Exit code is 0
- At least `"type":"agent_start"` appears (agent started and completed)

### 3. CLI Flag Tests

#### 3a. --help

```bash
cd "$PROJECT_ROOT" && ./bin/fir-e2e --help 2>&1; echo "EXIT:$?"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Exit code 0
- Stdout contains "Usage:" and "--provider" and "--model"

#### 3b. --version

```bash
cd "$PROJECT_ROOT" && ./bin/fir-e2e --version 2>&1; echo "EXIT:$?"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Exit code 0
- Stdout contains "fir"

#### 3c. --list-models

```bash
cd "$PROJECT_ROOT" && ./bin/fir-e2e --list-models 2>&1; echo "EXIT:$?"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Exit code 0 (even if no API keys — should list built-in models or exit gracefully)
- Output contains model names in `provider/model` format

### 4. Tool Execution Tests (via RPC with mock)

These tests use the mock server. The mock server detects keywords in the prompt and returns tool-call responses accordingly.

#### 4a. Read tool (mock)

The mock server returns a `read` tool call when it sees `READ_FILE` in the prompt. The file path is `testfile.txt`.

**Important:** Use `{ printf ...; sleep 5; }` to keep stdin open so the agent goroutine has time to complete and execute tools.

```bash
TMPTEST=$(mktemp -d) && echo "E2E_TEST_CONTENT_12345" > "$TMPTEST/testfile.txt" && cd "$TMPTEST" && { printf '{"id":"1","type":"prompt","message":"READ_FILE testfile.txt"}\n'; sleep 5; } | FIR_AGENT_DIR="$MOCK_AGENT_DIR" "$PROJECT_ROOT"/bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"; rm -rf "$TMPTEST"
```
Use `timeout: 15` on the bash tool call.

**Verify:**
- Events include tool execution (`"Type":"tool_execution_start"` with `"ToolName":"write"`)
- A tool result event contains "E2E_TEST_CONTENT_12345"

#### 4b. Write tool (mock)

The mock server returns a `write` tool call when it sees `WRITE_FILE` in the prompt.

**Important:** Use `{ printf ...; sleep 5; }` to keep stdin open so the agent goroutine has time to complete and execute tools.

```bash
TMPTEST=$(mktemp -d) && cd "$TMPTEST" && { printf '{"id":"1","type":"prompt","message":"WRITE_FILE output.txt WRITTEN_BY_FIR"}\n'; sleep 5; } | FIR_AGENT_DIR="$MOCK_AGENT_DIR" "$PROJECT_ROOT"/bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"; cat "$TMPTEST/output.txt" 2>/dev/null; echo "---"; rm -rf "$TMPTEST"
```
Use `timeout: 15` on the bash tool call.

**Verify:**
- `output.txt` exists and contains "WRITTEN_BY_FIR"

#### 4c. Bash tool (mock)

The mock server returns a `bash` tool call when it sees `RUN_BASH` in the prompt.

**Important:** Use `{ printf ...; sleep 5; }` to keep stdin open so the agent goroutine has time to complete and execute tools.

```bash
TMPTEST=$(mktemp -d) && cd "$TMPTEST" && { printf '{"id":"1","type":"prompt","message":"RUN_BASH echo BASH_E2E_OK"}\n'; sleep 5; } | FIR_AGENT_DIR="$MOCK_AGENT_DIR" "$PROJECT_ROOT"/bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"; rm -rf "$TMPTEST"
```
Use `timeout: 15` on the bash tool call.

**Verify:**
- Events include tool execution for "bash" (`"ToolName":"bash"`)
- A tool result contains "BASH_E2E_OK"

## Test Cycle

Each cycle:

### Step 1: Build

```bash
cd "$PROJECT_ROOT" && mkdir -p bin && go build -ldflags="-s -w" -o ./bin/fir-e2e ./cmd/fir/ 2>&1; echo "EXIT:$?"
```
Use `timeout: 60` on the bash tool call.

If build fails → write to `docs/review/URGENT.md`, skip to sleep.

### Step 2: Build and start mock server

```bash
cd "$PROJECT_ROOT" && mkdir -p bin && go build -o ./bin/mock-e2e-server ./.fir/skills/e2e/mockserver/ 2>&1; echo "EXIT:$?"
```
Use `timeout: 30`.

**Important:** Use `nohup` + `disown` — plain `&` causes the bash tool to block.

```bash
PORTFILE="$TMPDIR/mock-e2e-port"
LOGFILE="$TMPDIR/mock-e2e-server.log"
rm -f "$PORTFILE"
cd "$PROJECT_ROOT"
nohup ./bin/mock-e2e-server > "$LOGFILE" 2>&1 &
MOCK_PID=$!
disown $MOCK_PID
sleep 1
MOCK_PORT=$(cat "$PORTFILE" 2>/dev/null)
if [ -z "$MOCK_PORT" ]; then
  echo "WARN: mock server did not start (network restricted?)"
  echo "Server log: $(cat $LOGFILE 2>/dev/null)"
  kill $MOCK_PID 2>/dev/null
  MOCK_PID=""
  MOCK_UNAVAILABLE=1
else
  echo "Mock server running PID=$MOCK_PID PORT=$MOCK_PORT"
  MOCK_UNAVAILABLE=0
fi
echo "EXIT:$?"
```
Use `timeout: 10`.

If mock is available, set up the agent dir:

```bash
if [ "$MOCK_UNAVAILABLE" = "0" ]; then
  MOCK_AGENT_DIR=$(mktemp -d)
  cat > "$MOCK_AGENT_DIR/models.json" << ENDMODELS
{
  "providers": {
    "mock": {
      "baseUrl": "http://localhost:${MOCK_PORT}",
      "apiKey": "mock-key",
      "api": "openai-completions",
      "models": [
        {
          "id": "mock-model",
          "name": "Mock Model",
          "contextWindow": 128000,
          "maxTokens": 4096
        }
      ]
    }
  }
}
ENDMODELS
  echo "MOCK_AGENT_DIR=$MOCK_AGENT_DIR"
fi
echo "EXIT:$?"
```

If mock server fails and no API keys are available → skip LLM tests, run only fast tests.

### Step 3: Run fast tests (no LLM or mock needed)

Run these tests that don't require any provider. **Run independent tests in parallel** (multiple bash tool calls in the same block):
- `--help`, `--version`, `--list-models` (3a, 3b, 3c)
- RPC unknown command (2e)
- RPC malformed JSON (2f)
- Print mode with no API keys (1d)

### Step 4: Run LLM tests (mock or real fallback)

**If mock is available** (`MOCK_UNAVAILABLE=0`), run all these using the mock commands from the test sections above:
- Print mode piped stdin (1a)
- Print mode message arg (1b)
- Print mode `--no-session` (1c)
- Print mode JSON (1e)
- RPC get_state (2a)
- RPC prompt (2b)
- RPC get_available_models (2c)
- RPC set_thinking_level (2d)
- RPC abort (2g)
- Tool execution tests (4a, 4b, 4c)

**If mock is unavailable** but a real API key exists, run the same tests using the real provider (remove `FIR_AGENT_DIR`, `--provider mock`, `--model mock-model`). Use `timeout: 30` for real provider tests. Tool tests (4a-4c) should be **skipped** with real providers since they depend on the mock server's keyword-based tool call dispatch.

**If neither mock nor real keys**, skip all LLM tests and report: "LLM tests skipped — no mock server or API keys available."

### Step 5: Teardown mock server

```bash
[ -n "$MOCK_PID" ] && kill $MOCK_PID 2>/dev/null
rm -f ./bin/mock-e2e-server "$TMPDIR/mock-e2e-port"
[ -n "$MOCK_AGENT_DIR" ] && rm -rf "$MOCK_AGENT_DIR"
echo "Teardown complete"
```

### Step 6: Report results

Summarize to the user:
> E2E cycle complete. Ran X tests: Y passed, Z failed.

For failures:

**Build breaks or crashes → `docs/review/URGENT.md`:**
```markdown
## URGENT — [date]

### E2E: [test name] — [Brief description]
Command: `[the command that was run]`
Exit code: [code]
Stderr: [relevant stderr output]
Expected: [what should have happened]
```

**Behavioral bugs → `docs/review/BACKLOG.md`:**
```markdown
## E2E Failures
- `[test id]` — [description of incorrect behavior]
  Command: `[command]`
  Got: [actual output]
  Expected: [expected output]
```

For items that were previously failing but now pass, remove them from the backlog.

### Step 7: Refresh and loop

**Re-read this skill file** to keep instructions in context:
```
.fir/skills/e2e/SKILL.md
```

Sleep and loop:
```bash
sleep 30 && echo "=== E2E CYCLE REMINDER === Re-read .fir/skills/e2e/SKILL.md and start the next test cycle. Build: go build -o ./bin/fir-e2e ./cmd/fir/"
```

Use `timeout: 40` on the bash call. When you see the reminder, immediately:
1. Re-read this skill file
2. Start from Step 1

## Known Issues

Track known bugs here so you don't re-file them every cycle:

### ~~RPC stdin consumption~~ (FIXED — 2026-02-10)
`readPipedStdin()` was consuming all stdin before RPC server started. Fixed by guarding with `args.OutputMode != ModeRPC`. All RPC tests 2a–2g now pass.

### ~~RPC Server: prompt goroutine killed on EOF~~ (FIXED — 2026-02-18)
`server.go` `Run()` now uses a `sync.WaitGroup` to wait for pending agent goroutines before returning. Tests 2b, 2g, 4a-4c all pass. The `{ printf ...; sleep 5; }` pattern is retained in tests for stability but is no longer strictly required.

## Rules

- **Don't modify source code.** You are a tester, not a fixer. Write failures to `docs/review/`.
- **Use the bash tool's `timeout` parameter on every call.** The binary might hang — never wait indefinitely. The `timeout` shell command does NOT exist on macOS.
- **Use temp directories.** Never write test files to the project directory. Always `mktemp -d` and clean up.
- **Parse JSON carefully.** RPC output is one JSON object per line. Parse each line independently.
- **Don't test during active work.** If `find pkg/ cmd/ -name "*.go" -mmin -2` shows many recently modified files, skip the cycle — agents are mid-edit.
- **Track what you tested.** At the top of each report, note the date and which tests ran.
- **Escalate crashes immediately.** A panic or segfault is always URGENT.
- **Be patient with LLM tests.** Even mocked tests need a few seconds for the agent loop. Use 15s+ timeouts.
- **Verify JSON output.** When testing RPC or JSON mode, validate that each line parses as JSON. A line that isn't valid JSON is a bug.
- **Don't re-file known issues.** Check the Known Issues section above before writing to review files.
- **Always `cd "$PROJECT_ROOT"`** at the start of commands. Don't assume the working directory.
- **Use absolute path to binary** when running from temp directories (e.g., `"$PROJECT_ROOT"/bin/fir-e2e`).
- **Always clean up the mock server** (`kill $MOCK_PID`) even if tests fail.
