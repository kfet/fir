# Testing Strategy

## Principle: Every File Has Tests

For every `foo.go`, there is a `foo_test.go` in the same package. No exceptions.

An agent completing a task MUST deliver both the implementation and its tests.
The task is NOT `[x]` until `go test ./...` passes.

## Test Categories

### 1. Unit Tests (default, always runnable)

Pure logic tests. No network, no filesystem side effects, no subprocesses.
These run fast and are the bulk of coverage.

```go
// pkg/ai/types_test.go
func TestUsageCostCalculation(t *testing.T) { ... }

// pkg/core/tools/truncate_test.go
func TestTruncateHead_LineLimitHit(t *testing.T) { ... }
func TestTruncateHead_ByteLimitHit(t *testing.T) { ... }
func TestTruncateTail_EmptyInput(t *testing.T) { ... }
```

### 2. Tests With Temp Filesystem

Tools (read, write, edit, bash, grep, find, ls) and session manager need
real files. Use `t.TempDir()`:

```go
func TestReadTool_TextFile(t *testing.T) {
    dir := t.TempDir()
    os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello\nworld"), 0644)
    
    tool := NewReadTool(dir)
    result, err := tool.Execute(ctx, ReadParams{Path: "hello.txt"})
    require.NoError(t, err)
    assert.Contains(t, result.Text(), "hello\nworld")
}
```

### 3. Tests With Subprocess (bash tool)

The bash tool spawns real processes. Keep commands trivial and fast:

```go
func TestBashTool_Echo(t *testing.T) {
    tool := NewBashTool(t.TempDir())
    result, err := tool.Execute(ctx, BashParams{Command: "echo hello"})
    require.NoError(t, err)
    assert.Equal(t, "hello\n", result.Text())
}

func TestBashTool_Timeout(t *testing.T) {
    tool := NewBashTool(t.TempDir())
    _, err := tool.Execute(ctx, BashParams{Command: "sleep 10", Timeout: 1})
    assert.ErrorContains(t, err, "timeout")
}

func TestBashTool_Abort(t *testing.T) {
    ctx, cancel := context.WithCancel(context.Background())
    go func() { time.Sleep(100*time.Millisecond); cancel() }()
    _, err := tool.Execute(ctx, BashParams{Command: "sleep 10"})
    assert.ErrorContains(t, err, "abort")
}
```

### 4. LLM Provider Mocks (critical — no real API calls)

Every provider test uses a mock HTTP server that replays recorded SSE streams.

```go
// pkg/ai/providers/testutil_test.go

// mockSSEServer returns an httptest.Server that serves a canned SSE response.
func mockSSEServer(t *testing.T, fixture string) *httptest.Server {
    data, err := os.ReadFile(filepath.Join("testdata", fixture))
    require.NoError(t, err)
    
    return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/event-stream")
        w.Write(data)
    }))
}
```

**Fixture files** live in `testdata/` directories next to the test:

```
pkg/ai/providers/testdata/
├── anthropic_simple_response.sse
├── anthropic_tool_call.sse
├── anthropic_thinking.sse
├── anthropic_streaming_error.sse
├── openai_simple_response.sse
├── openai_tool_call.sse
├── google_simple_response.sse
└── google_streaming_error.sse
```

Each `.sse` file is a raw recorded HTTP response body (the SSE event stream).

### 5. Agent Loop Integration Tests (mock LLM)

Test the full agent loop with a mock provider that returns canned responses:

```go
func TestAgentLoop_SingleTurn(t *testing.T) {
    // Mock provider returns: "Hello! I see the file."
    agent := newTestAgent(t, mockProvider(
        simpleResponse("Hello! I see the file."),
    ))
    
    events := collectEvents(t, agent)
    agent.Prompt(ctx, "What files are here?")
    
    assert.Equal(t, "agent_start", events[0].Type)
    assert.Equal(t, "Hello! I see the file.", extractText(events))
}

func TestAgentLoop_ToolCall(t *testing.T) {
    // Mock provider returns: tool call to "read", then "The file contains X"
    agent := newTestAgent(t, mockProvider(
        toolCallResponse("read", map[string]any{"path": "test.txt"}),
        simpleResponse("The file contains X"),
    ))
    
    // Set up tool that returns canned result
    agent.SetTools([]AgentTool{mockReadTool("test.txt", "hello world")})
    
    events := collectEvents(t, agent)
    agent.Prompt(ctx, "Read test.txt")
    
    assertContainsEvent(t, events, "tool_execution_start")
    assertContainsEvent(t, events, "tool_execution_end")
}

func TestAgentLoop_SteeringInterrupt(t *testing.T) {
    // Test that steer() interrupts tool execution
}

func TestAgentLoop_Abort(t *testing.T) {
    // Test that context cancellation aborts mid-stream
}
```

### 6. Session Manager Tests

Test JSONL persistence, branching, tree navigation:

```go
func TestSessionManager_AppendAndReload(t *testing.T) {
    dir := t.TempDir()
    sm := NewSessionManager(dir)
    sm.AppendMessage(userMessage("hello"))
    sm.AppendMessage(assistantMessage("hi there"))
    
    // Reload from disk
    sm2 := OpenSessionManager(sm.GetSessionFile())
    ctx := sm2.BuildSessionContext()
    assert.Len(t, ctx.Messages, 2)
}

func TestSessionManager_Branching(t *testing.T) { ... }
func TestSessionManager_Compaction(t *testing.T) { ... }
```

### 7. TUI Tests (headless)

TUI components render to string arrays — no real terminal needed:

```go
func TestText_Render(t *testing.T) {
    text := NewText("Hello, world!")
    lines := text.Render(80)
    assert.Equal(t, []string{"Hello, world!"}, lines)
}

func TestText_Wrapping(t *testing.T) {
    text := NewText("Hello, world!")
    lines := text.Render(5)
    // Should wrap
    assert.True(t, len(lines) > 1)
}

func TestVisibleWidth_ANSI(t *testing.T) {
    assert.Equal(t, 5, VisibleWidth("\x1b[31mhello\x1b[0m"))
}

func TestBox_Border(t *testing.T) {
    box := NewBox(NewText("hi"), BoxOptions{Border: true})
    lines := box.Render(20)
    assert.Contains(t, lines[0], "─")  // top border
}
```

### 8. End-to-End Tests (mock LLM, real tools)

Test the full pipeline: CLI args → AgentSession → tool execution → output.

```go
func TestE2E_PrintMode(t *testing.T) {
    // Mock LLM that returns "Hello from the agent"
    // Run in print mode
    // Verify stdout contains "Hello from the agent"
}

func TestE2E_PrintMode_ToolCall(t *testing.T) {
    // Mock LLM that calls read tool, then responds
    // Set up temp dir with a file
    // Verify tool was executed and LLM got the result
}
```

### 9. OAuth Flow Tests (mock HTTP, no real providers)

OAuth tests must never call real provider endpoints. Use `httptest.Server` to mock:
- Authorization endpoints (returns redirect with code)
- Token exchange endpoints (returns access/refresh tokens)
- Token refresh endpoints (returns new access token)
- Device flow polling endpoints (returns device code, then tokens)

```go
func TestAnthropicOAuth_Login(t *testing.T) {
    // Mock the Anthropic token endpoint
    tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        json.NewEncoder(w).Encode(map[string]any{
            "access_token":  "test-access",
            "refresh_token": "test-refresh",
            "expires_in":    3600,
        })
    }))
    defer tokenServer.Close()

    provider := NewAnthropicOAuth(WithTokenURL(tokenServer.URL))
    creds, err := provider.Login(mockCallbacks())
    require.NoError(t, err)
    assert.Equal(t, "test-access", creds.Access)
}

func TestPKCE_GenerateVerifierAndChallenge(t *testing.T) {
    verifier, challenge, err := GeneratePKCE()
    require.NoError(t, err)
    assert.Len(t, verifier, 43) // base64url of 32 bytes
    assert.Len(t, challenge, 43)
    assert.NotEqual(t, verifier, challenge)
}
```

PKCE can be tested with pure unit tests (crypto operations, no network).
For callback-server-based flows, use `httptest.Server` as the local callback.

## Test Helpers

Shared test utilities in each package:

```
pkg/ai/testutil_test.go          # mockSSEServer, fixture loader
pkg/agent/testutil_test.go       # newTestAgent, mockProvider, collectEvents
pkg/core/tools/testutil_test.go  # setupTestDir, mock tools
pkg/core/testutil_test.go        # test session helpers
pkg/tui/testutil_test.go         # component render helpers
```

## Coverage Target

- **Unit + mock tests:** 90%+ line coverage per package

```bash
make test                  # All unit/mock tests
make test-cover            # With coverage report
make test-live             # Live API tests (needs API keys)
```

## Makefile Test Targets

```makefile
test:
	go test ./...

test-cover:
	go test -coverprofile=bin/coverage.out ./...
	go tool cover -func=bin/coverage.out

test-race:
	go test -race ./...
```

## Rule: No Test, No Done

A work tracker item is `[x]` only when:
1. The Go file compiles
2. A `_test.go` file exists with meaningful tests
3. `go test ./path/to/package/...` passes
4. `go vet ./path/to/package/...` passes
