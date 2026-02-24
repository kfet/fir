---
name: e2e
description: Continuously test the fir binary end-to-end by running it in print, RPC, and ACP modes over stdio, verifying tool execution, streaming, model resolution, theme flags, and error handling against a real or mock LLM.
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

### 5. Extended CLI Flag Tests

#### 5a. --list-models includes gemini-2.5-pro (new model added in recent merge)

```bash
cd "$PROJECT_ROOT" && ./bin/fir-e2e --list-models 2>&1 | grep -E "^google/gemini-2.5-pro$"; echo "EXIT:$?"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- `google/gemini-2.5-pro` appears in the output (exact line match)
- Exit code 0

### 6. Additional RPC Command Tests (mock)

These tests cover RPC commands that weren't previously in the test suite, added as part of the server refactor.

#### 6a. RPC: set_model

```bash
cd "$PROJECT_ROOT" && printf '{"id":"1","type":"set_model","provider":"mock","modelId":"mock-model-2"}\n{"id":"2","type":"get_state"}\n' | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 15` on the bash tool call. (Requires `MOCK_AGENT_DIR` to have two models: `mock-model` and `mock-model-2`.)

**Verify:**
- First response: `"command":"set_model"`, `"success":true`, data contains `"id":"mock-model-2"`
- Second response: `get_state` shows `"id":"mock-model-2"` in the model field

#### 6b. RPC: cycle_model

```bash
cd "$PROJECT_ROOT" && printf '{"id":"1","type":"cycle_model"}\n' | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 15` on the bash tool call. (Requires two models in `MOCK_AGENT_DIR`.)

**Verify:**
- Response has `"command":"cycle_model"`, `"success":true`
- `data.model` contains the next model's `id`
- `data.thinkingLevel` and `data.isScoped` fields are present

#### 6c. RPC: cycle_thinking_level

```bash
cd "$PROJECT_ROOT" && printf '{"id":"1","type":"cycle_thinking_level"}\n{"id":"2","type":"get_state"}\n' | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 15` on the bash tool call.

**Verify:**
- First response: `"command":"cycle_thinking_level"`, `"success":true`, `data.level` is `"minimal"` (cycles from default `"off"`)
- Second response: `get_state` shows `"thinkingLevel":"minimal"`

#### 6d. RPC: bash (direct execution, not agent tool)

```bash
cd "$PROJECT_ROOT" && printf '{"id":"1","type":"bash","command":"echo RPC_BASH_DIRECT_OK"}\n' | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 15` on the bash tool call.

**Verify:**
- Response: `"command":"bash"`, `"success":true`
- `data.Output` contains `"RPC_BASH_DIRECT_OK"`
- `data.ExitCode` is `0`

#### 6e. RPC: bash with empty command (error)

```bash
cd "$PROJECT_ROOT" && printf '{"id":"1","type":"bash","command":""}\n' | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Response: `"command":"bash"`, `"success":false`, `"error"` contains "command is required"

#### 6f. RPC: get_session_stats

```bash
cd "$PROJECT_ROOT" && printf '{"id":"1","type":"get_session_stats"}\n' | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 15` on the bash tool call.

**Verify:**
- Response: `"command":"get_session_stats"`, `"success":true`
- `data` contains `totalMessages`, `userMessages`, `assistantMessages`, `tokens`, `cost` fields

#### 6g. RPC: get_messages

```bash
cd "$PROJECT_ROOT" && printf '{"id":"1","type":"get_messages"}\n' | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Response: `"command":"get_messages"`, `"success":true`
- `data.messages` is an array (empty for a new session)

#### 6h. RPC: get_commands

```bash
cd "$PROJECT_ROOT" && printf '{"id":"1","type":"get_commands"}\n' | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Response: `"command":"get_commands"`, `"success":true`
- `data.commands` is an array (may be empty or contain skill entries)
- Each command has `name` and `source` fields

#### 6i. RPC: get_last_assistant_text

```bash
cd "$PROJECT_ROOT" && printf '{"id":"1","type":"get_last_assistant_text"}\n' | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Response: `"command":"get_last_assistant_text"`, `"success":true`
- `data.text` is `null` for a fresh session (no assistant messages yet)

#### 6j. RPC: set_session_name

```bash
cd "$PROJECT_ROOT" && printf '{"id":"1","type":"set_session_name","name":"my-test-session"}\n' | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Response: `"command":"set_session_name"`, `"success":true`

#### 6k. RPC: set_session_name with empty name (error)

```bash
cd "$PROJECT_ROOT" && printf '{"id":"1","type":"set_session_name","name":""}\n' | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Response: `"command":"set_session_name"`, `"success":false`, `"error"` contains "cannot be empty"

#### 6l. RPC: get_fork_messages

```bash
cd "$PROJECT_ROOT" && printf '{"id":"1","type":"get_fork_messages"}\n' | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Response: `"command":"get_fork_messages"`, `"success":true`
- `data.messages` is an array (empty for a fresh session)

#### 6m. RPC: new_session

```bash
cd "$PROJECT_ROOT" && printf '{"id":"1","type":"new_session"}\n' | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Response: `"command":"new_session"`, `"success":true`
- `data.cancelled` is `false`

#### 6n. RPC: set_auto_compaction persists to get_state

```bash
cd "$PROJECT_ROOT" && printf '{"id":"1","type":"set_auto_compaction","enabled":false}\n{"id":"2","type":"get_state"}\n' | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- First response: `"command":"set_auto_compaction"`, `"success":true`
- Second response: `get_state` shows `"autoCompactionEnabled":false`

#### 6o. RPC: set_steering_mode and set_follow_up_mode

```bash
cd "$PROJECT_ROOT" && printf '{"id":"1","type":"set_steering_mode","mode":"one-at-a-time"}\n{"id":"2","type":"set_follow_up_mode","mode":"one-at-a-time"}\n' | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Both responses: `"success":true`

#### 6p. RPC: abort_bash and abort_retry

```bash
cd "$PROJECT_ROOT" && printf '{"id":"1","type":"abort_bash"}\n{"id":"2","type":"abort_retry"}\n' | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Both responses: `"success":true` (no-ops when nothing is running, but should not error)

#### 6q. RPC: export_html

```bash
TMPTEST=$(mktemp -d) && cd "$TMPTEST" && printf '{"id":"1","type":"export_html"}\n' | FIR_AGENT_DIR="$MOCK_AGENT_DIR" "$PROJECT_ROOT"/bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"; rm -rf "$TMPTEST"
```
Use `timeout: 15` on the bash tool call.

**Verify:**
- Response: `"command":"export_html"`, `"success":true`
- `data.path` is a non-empty file path
- The file at `data.path` exists and begins with `<!doctype html` or `<html`

#### 6r. RPC: set_model with non-existent model (error)

```bash
cd "$PROJECT_ROOT" && printf '{"id":"1","type":"set_model","provider":"mock","modelId":"nonexistent"}\n' | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Response: `"command":"set_model"`, `"success":false`, `"error"` contains "Model not found"

#### 6s. RPC: switch_session with empty path (error)

```bash
cd "$PROJECT_ROOT" && printf '{"id":"1","type":"switch_session","sessionPath":""}\n' | FIR_AGENT_DIR="$MOCK_AGENT_DIR" ./bin/fir-e2e --provider mock --model mock-model --mode rpc --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Response: `"command":"switch_session"`, `"success":false`, `"error"` contains "session path is required"

### 7. Print Mode Error Handling (ErrAgentAborted)

These tests verify the `ErrAgentAborted` refactor: `print.Run` now returns an error instead of calling `os.Exit(1)` directly. The binary must still exit with code 1 without leaking a double error message.

#### 7a. Print mode exits non-zero on API failure

```bash
cd "$PROJECT_ROOT"
DEAD_AGENT_DIR=$(mktemp -d)
cat > "$DEAD_AGENT_DIR/models.json" << 'EOF'
{"providers":{"dead":{"baseUrl":"http://127.0.0.1:1","apiKey":"bad","api":"openai-completions","models":[{"id":"dead-model","name":"Dead","contextWindow":128000,"maxTokens":4096}]}}}
EOF
FIR_AGENT_DIR="$DEAD_AGENT_DIR" ./bin/fir-e2e --provider dead --model dead-model --no-session -p "say hello" 2>&1; echo "EXIT:$?"
rm -rf "$DEAD_AGENT_DIR"
```
Use `timeout: 15` on the bash tool call.

**Verify:**
- Exit code is non-zero (1)
- No panic/stack trace
- Output does NOT contain `"Error: agent aborted"` (ErrAgentAborted is handled silently by main — print mode already wrote the error)

#### 7b. Bad provider config does not panic

```bash
cd "$PROJECT_ROOT"
BAD_AGENT_DIR=$(mktemp -d)
cat > "$BAD_AGENT_DIR/models.json" << 'EOF'
{"providers":{"bad":{"baseUrl":"http://localhost:9999","apiKey":"key","models":[{"id":"m","name":"M","contextWindow":128000,"maxTokens":4096}]}}}
EOF
FIR_AGENT_DIR="$BAD_AGENT_DIR" ./bin/fir-e2e --list-models 2>&1; echo "EXIT:$?"
rm -rf "$BAD_AGENT_DIR"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- No `panic:` in output (previously would panic; now logs an error gracefully)
- Exit code 0 (other built-in models still list fine)

### 8. ACP Mode Tests (`--mode acp`)

ACP mode speaks JSON-RPC 2.0 over stdin/stdout. These are fast tests (no LLM needed for basic protocol checks).

**Note:** ACP mode reads stdin directly (no `readPipedStdin` consumption). Use `{ printf ...; sleep N; }` to keep stdin open for the server to respond.

#### 8a. ACP initialize — agentInfo.name is "fir"

```bash
cd "$PROJECT_ROOT" && { printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":10,"clientCapabilities":{}}}\n'; sleep 2; } | FIR_AGENT_DIR="$(mktemp -d)" ./bin/fir-e2e --mode acp --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Response line is valid JSON with `"id":1` and `"result"` (not `"error"`)
- `result.agentInfo.name` is `"fir"`
- `result.protocolVersion` is a number (≥ 1)
- `result.agentCapabilities.sessionCapabilities` contains `list` and `resume`
- Exit code 0

#### 8b. ACP `session/new` — creates session, returns sessionId

```bash
cd "$PROJECT_ROOT" && { printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":10,"clientCapabilities":{}}}\n{"jsonrpc":"2.0","id":2,"method":"session/new","params":{}}\n'; sleep 2; } | FIR_AGENT_DIR="$(mktemp -d)" ./bin/fir-e2e --mode acp --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Two response lines appear
- The `session/new` response (`"id":2`) has a non-empty `result.sessionId` UUID string
- A `session/update` notification (`"method":"session/update"`) is emitted with `update.sessionUpdate = "available_commands_update"`
- Exit code 0

#### 8c. ACP `session/update` notification includes `/share` and `/export` commands

(Reuse output from 8b.)

**Verify:**
- The `session/update` notification's `update.availableCommands` array contains entries with `"name":"share"` and `"name":"export"`

#### 8d. ACP unknown method returns method-not-found error

```bash
cd "$PROJECT_ROOT" && { printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":10,"clientCapabilities":{}}}\n{"jsonrpc":"2.0","id":2,"method":"agent/bogus","params":{}}\n'; sleep 1; } | FIR_AGENT_DIR="$(mktemp -d)" ./bin/fir-e2e --mode acp --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Response for `"id":2` has `"error"` (not `"result"`)
- `error.code` is `-32601` (Method not found)
- Process does NOT crash

#### 8e. ACP malformed JSON does not crash

```bash
cd "$PROJECT_ROOT" && { printf 'this is not json\n'; sleep 1; } | FIR_AGENT_DIR="$(mktemp -d)" ./bin/fir-e2e --mode acp --no-session 2>&1; echo "EXIT:$?"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- No `panic:` in output
- Process exits cleanly (exit code 0 or 1, but not a crash)

### 9. `--theme` Flag Tests (fast, no LLM needed)

These tests verify that the `--theme` flag is now actually wired through (it was previously parsed but silently ignored).

#### 9a. `--theme <valid-dir>` does not crash; `--list-models` still works

```bash
cd "$PROJECT_ROOT" && THEMEDIR=$(mktemp -d) && ./bin/fir-e2e --theme "$THEMEDIR" --list-models 2>&1 | head -5; echo "EXIT:$?"; rm -rf "$THEMEDIR"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Exit code 0
- Output contains model names in `provider/model` format (theme dir doesn't break model listing)
- No `panic:` in output

#### 9b. `--theme <nonexistent-path>` does not crash

```bash
cd "$PROJECT_ROOT" && ./bin/fir-e2e --theme /nonexistent/path/that/does/not/exist --list-models 2>&1 | head -5; echo "EXIT:$?"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Exit code 0 (graceful degradation — missing theme dirs are ignored)
- No `panic:` in output
- Model listing still works

#### 9c. `--no-themes` flag is accepted without crash

```bash
cd "$PROJECT_ROOT" && ./bin/fir-e2e --no-themes --list-models 2>&1 | head -5; echo "EXIT:$?"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Exit code 0
- No `panic:` in output

#### 9d. Custom theme file in `--theme <dir>` is discovered

```bash
cd "$PROJECT_ROOT"
THEMEDIR=$(mktemp -d)
cat > "$THEMEDIR/myTheme.json" << 'EOF'
{"name":"myTheme","description":"Custom test theme"}
EOF
./bin/fir-e2e --theme "$THEMEDIR" --list-models 2>&1 | head -3; echo "EXIT:$?"; rm -rf "$THEMEDIR"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Exit code 0
- No crash (theme file loaded without error even if minimal/incomplete)

### 10. `ResolveCliModel` Tests (mock)

These tests verify the `ResolveCliModel` feature added in v0.4.0: sophisticated CLI model resolution with prefix/fuzzy matching and thinking-level suffix (`:high`, `:low`, etc.).

#### 10a. `--model <prefix>` fuzzy/prefix match selects correct model

This test uses a dedicated agent dir to avoid ambiguity from multiple models.

```bash
cd "$PROJECT_ROOT"
PREFIX_AGENT_DIR=$(mktemp -d)
cat > "$PREFIX_AGENT_DIR/models.json" << 'EOF'
{"providers":{"mock":{"baseUrl":"http://localhost:1","apiKey":"mock-key","api":"openai-completions","models":[{"id":"special-match-model","name":"Special Match Model","contextWindow":128000,"maxTokens":4096}]}}}
EOF
printf '{"id":"1","type":"get_state"}\n' | FIR_AGENT_DIR="$PREFIX_AGENT_DIR" ./bin/fir-e2e --provider mock --model "special-match" --mode rpc --no-session 2>&1; echo "EXIT:$?"
rm -rf "$PREFIX_AGENT_DIR"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Response has `"command":"get_state"`, `"success":true`
- `data.model.id` is `"special-match-model"` (prefix "special-match" resolved to the unique matching model)

#### 10b. `--model provider/model` notation selects correct model

```bash
cd "$PROJECT_ROOT"
PNM_AGENT_DIR=$(mktemp -d)
cat > "$PNM_AGENT_DIR/models.json" << 'EOF'
{"providers":{"mock":{"baseUrl":"http://localhost:1","apiKey":"mock-key","api":"openai-completions","models":[{"id":"mock-model","name":"Mock Model","contextWindow":128000,"maxTokens":4096}]}}}
EOF
printf '{"id":"1","type":"get_state"}\n' | FIR_AGENT_DIR="$PNM_AGENT_DIR" ./bin/fir-e2e --model "mock/mock-model" --mode rpc --no-session 2>&1; echo "EXIT:$?"
rm -rf "$PNM_AGENT_DIR"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Response has `"command":"get_state"`, `"success":true`
- `data.model.id` is `"mock-model"` and `data.model.provider` is `"mock"`

#### 10c. `--model <id>:<thinking-level>` suffix sets thinkingLevel (reasoning model)

This requires a mock model with `"reasoning":true`. Create a dedicated agent dir:

```bash
cd "$PROJECT_ROOT"
REASONING_AGENT_DIR=$(mktemp -d)
cat > "$REASONING_AGENT_DIR/models.json" << 'EOF'
{"providers":{"mock":{"baseUrl":"http://localhost:1","apiKey":"mock-key","api":"openai-completions","models":[{"id":"think-model","name":"Think Model","contextWindow":128000,"maxTokens":4096,"reasoning":true}]}}}
EOF
printf '{"id":"1","type":"get_state"}\n' | FIR_AGENT_DIR="$REASONING_AGENT_DIR" ./bin/fir-e2e --model "think-model:high" --mode rpc --no-session 2>&1; echo "EXIT:$?"
rm -rf "$REASONING_AGENT_DIR"
```
Use `timeout: 10` on the bash tool call.

**Verify:**
- Response has `"command":"get_state"`, `"success":true`
- `data.model.id` is `"think-model"` (`:high` suffix stripped before model lookup)
- `data.thinkingLevel` is `"high"` (thinking level extracted from suffix and applied)

## Test Cycle

Each cycle follows this exact order:

### Step 0: Print the next reminder command

Before doing any work, output this as a plain code block so it's visible in the chat even if the session times out or the context window fills:

```
Next reminder command:
sleep 30 && echo "=== E2E CYCLE REMINDER === Re-read .fir/skills/e2e/SKILL.md and start the next test cycle. Build: go build -o ./bin/fir-e2e ./cmd/fir/"
```

### Step 0b: Re-read this skill file

Re-read `.fir/skills/e2e/SKILL.md` to keep instructions in context. Long-running agents drift — this is not optional.

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
        },
        {
          "id": "mock-model-2",
          "name": "Mock Model 2",
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
- `--list-models` includes `google/gemini-2.5-pro` (5a)
- RPC unknown command (2e)
- RPC malformed JSON (2f)
- Print mode with no API keys (1d)
- Bad provider config does not panic (7b)
- ACP initialize (8a), session/new (8b/8c), unknown method (8d), malformed JSON (8e)
- `--theme` flag tests: valid dir (9a), nonexistent path (9b), `--no-themes` (9c), custom theme file (9d)
- ResolveCliModel tests (10a, 10b, 10c): these use `get_state` only — no live LLM calls needed

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
- Additional RPC tests (6a–6s): set_model, cycle_model, cycle_thinking_level, bash, get_session_stats, get_messages, get_commands, get_last_assistant_text, set_session_name, get_fork_messages, new_session, set_auto_compaction, set_steering_mode, set_follow_up_mode, abort_bash, abort_retry, export_html, and error paths
- Print mode ErrAgentAborted (7a)

**If mock is unavailable** but a real API key exists, run the same tests using the real provider (remove `FIR_AGENT_DIR`, `--provider mock`, `--model mock-model`). Use `timeout: 30` for real provider tests. Tool tests (4a-4c) and additional RPC model tests (6a-6b) should be **skipped** with real providers since they depend on the mock server's keyword-based tool call dispatch.

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

### Step 7: Run the reminder command

```bash
sleep 30 && echo "=== E2E CYCLE REMINDER === Re-read .fir/skills/e2e/SKILL.md and start the next test cycle. Build: go build -o ./bin/fir-e2e ./cmd/fir/"
```

Use `timeout: 40` on the bash call. When you see the reminder output, immediately go back to Step 0.

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
