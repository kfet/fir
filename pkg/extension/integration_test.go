package extension

import (
	"context"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/core"
)

// stubResourceLoader implements core.ResourceLoader for testing.
type stubResourceLoader struct{}

func (s *stubResourceLoader) GetSkills() ([]core.Skill, []core.ResourceDiagnostic) { return nil, nil }
func (s *stubResourceLoader) GetPrompts() ([]core.PromptTemplate, []core.ResourceDiagnostic) {
	return nil, nil
}
func (s *stubResourceLoader) GetAgentsFiles() []core.AgentsFile { return nil }
func (s *stubResourceLoader) GetSystemPrompt() string           { return "" }
func (s *stubResourceLoader) GetAppendSystemPrompt() []string   { return nil }
func (s *stubResourceLoader) GetPathMetadata() map[string]core.PathMetadata {
	return nil
}
func (s *stubResourceLoader) ExtendResources(core.ResourceExtensionPaths) {}
func (s *stubResourceLoader) Reload() error                               { return nil }

func newTestSession(t *testing.T, cwd string) *core.AgentSession {
	t.Helper()
	sm := core.InMemorySessionManager()
	dummyModel := &ai.Model{
		Provider:      "test",
		ID:            "test-model",
		Name:          "Test",
		ContextWindow: 100000,
	}
	a := agent.NewAgent(agent.AgentOptions{
		InitialState: &agent.AgentState{
			Model: dummyModel,
		},
	})
	return core.NewAgentSession(core.AgentSessionOptions{
		Agent:          a,
		SessionManager: sm,
		ResourceLoader: &stubResourceLoader{},
		Cwd:            cwd,
	})
}

func TestSetupWithSession(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	var (
		gotSessionStart bool
		gotAgentEnd     bool
		blockedTool     bool
	)

	Register("test-integration", func(api API) {
		api.On("session_start", func(event *Event, ctx Context) (any, error) {
			gotSessionStart = true
			return nil, nil
		})
		api.On("agent_end", func(event *Event, ctx Context) (any, error) {
			gotAgentEnd = true
			return nil, nil
		})
		api.On("tool_call", func(event *Event, ctx Context) (any, error) {
			if event.ToolCall != nil && event.ToolCall.ToolName == "dangerous" {
				blockedTool = true
				return &ToolCallResult{Block: true, Reason: "blocked"}, nil
			}
			return nil, nil
		})
	})

	cwd := t.TempDir()
	session := newTestSession(t, cwd)
	defer session.Close()

	result, err := Setup(session, core.NewEventBus(), SetupOptions{
		EnabledNames: []string{"test-integration"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Runner == nil {
		t.Fatal("expected non-nil setup result")
	}

	// Emit session_start
	_ = result.Runner.EmitSessionStart()
	if !gotSessionStart {
		t.Error("expected session_start handler to be called")
	}

	// Check hooks were set
	hooks := session.Hooks()
	if hooks == nil {
		t.Fatal("expected hooks to be set on session")
	}

	// Test tool_call hook
	if hooks.OnToolCall == nil {
		t.Fatal("expected OnToolCall hook")
	}

	block := hooks.OnToolCall("tc1", "dangerous", map[string]any{})
	if block == nil {
		t.Fatal("expected block for dangerous tool")
	}
	if !blockedTool {
		t.Error("expected tool_call handler to be called")
	}
	if block.Reason != "blocked" {
		t.Errorf("expected reason 'blocked', got %q", block.Reason)
	}

	// Safe tool should pass
	block = hooks.OnToolCall("tc2", "read", map[string]any{"path": "test.txt"})
	if block != nil {
		t.Error("expected nil block for safe tool")
	}

	// Emit agent_end
	_ = result.Runner.EmitAgentEnd(nil)
	if !gotAgentEnd {
		t.Error("expected agent_end handler to be called")
	}
}

func TestSetupNoExtensions(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	cwd := t.TempDir()
	session := newTestSession(t, cwd)
	defer session.Close()

	result, err := Setup(session, core.NewEventBus(), SetupOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Hooks are always set (even with no extensions) to support runtime reload.
	// When no handlers are registered, the hooks are no-ops.
	if session.Hooks() == nil {
		t.Error("expected hooks to be set (for reload support)")
	}

	// Runner should still exist
	if result == nil || result.Runner == nil {
		t.Fatal("expected non-nil runner even with no extensions")
	}

	// No extensions should be loaded
	if len(result.Runner.Extensions()) != 0 {
		t.Error("expected no extensions loaded")
	}
}

func TestWrapToolsWithHooks(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	Register("blocker", func(api API) {
		api.On("tool_call", func(event *Event, ctx Context) (any, error) {
			if event.ToolCall != nil && event.ToolCall.ToolName == "bash" {
				cmd, _ := event.ToolCall.Input["command"].(string)
				if cmd == "danger" {
					return &ToolCallResult{Block: true, Reason: "blocked"}, nil
				}
			}
			return nil, nil
		})
	})

	cwd := t.TempDir()
	session := newTestSession(t, cwd)
	defer session.Close()

	_, err := Setup(session, core.NewEventBus(), SetupOptions{
		EnabledNames: []string{"blocker"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create a mock tool
	executed := false
	bashTool := agent.AgentTool{
		Tool: ai.Tool{Name: "bash"},
		Execute: func(ctx context.Context, id string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			executed = true
			return agent.AgentToolResult{
				Content: []ai.ToolResultContent{{Type: "text", Text: "output"}},
			}, nil
		},
	}

	// Wrap tools with hooks
	wrapped := session.WrapToolsWithHooks([]agent.AgentTool{bashTool})
	if len(wrapped) != 1 {
		t.Fatalf("expected 1 wrapped tool, got %d", len(wrapped))
	}

	// Execute with blocked command
	result, err := wrapped[0].Execute(context.Background(), "tc1", map[string]any{"command": "danger"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if executed {
		t.Error("tool should not have been executed (blocked)")
	}
	if len(result.Content) == 0 || result.Content[0].Text != "blocked" {
		t.Error("expected blocked reason in result")
	}

	// Execute with safe command
	executed = false
	result, err = wrapped[0].Execute(context.Background(), "tc2", map[string]any{"command": "echo hi"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !executed {
		t.Error("tool should have been executed")
	}
	if len(result.Content) == 0 || result.Content[0].Text != "output" {
		t.Error("expected original output")
	}
}

func TestRunnerReset(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	var called bool
	Register("resettable", func(api API) {
		api.On("session_start", func(event *Event, ctx Context) (any, error) {
			called = true
			return nil, nil
		})
		api.RegisterCommand("test", Command{Description: "test cmd"})
		api.RegisterFlag("myflag", Flag{Type: "boolean", Default: true})
	})

	runner := NewRunner(core.NewEventBus())
	if err := runner.LoadEnabled([]string{"resettable"}); err != nil {
		t.Fatal(err)
	}

	// Verify loaded
	if len(runner.Extensions()) != 1 {
		t.Fatalf("expected 1 extension, got %d", len(runner.Extensions()))
	}
	if len(runner.GetCommands()) != 1 {
		t.Error("expected 1 command")
	}
	if !runner.HasHandlers("session_start") {
		t.Error("expected session_start handlers")
	}

	// Reset
	runner.Reset()

	if len(runner.Extensions()) != 0 {
		t.Error("expected 0 extensions after reset")
	}
	if len(runner.GetCommands()) != 0 {
		t.Error("expected 0 commands after reset")
	}
	if runner.HasHandlers("session_start") {
		t.Error("expected no session_start handlers after reset")
	}

	// Emit should be a no-op now
	called = false
	_ = runner.EmitSessionStart()
	if called {
		t.Error("handler should not fire after reset")
	}
}

func TestSetupResultReload(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	var (
		extAStarted  bool
		extAShutdown bool
		extBStarted  bool
	)

	Register("ext-a", func(api API) {
		api.On("session_start", func(event *Event, ctx Context) (any, error) {
			extAStarted = true
			return nil, nil
		})
		api.On("session_shutdown", func(event *Event, ctx Context) (any, error) {
			extAShutdown = true
			return nil, nil
		})
		api.RegisterCommand("cmd-a", Command{Description: "from ext-a"})
	})

	Register("ext-b", func(api API) {
		api.On("session_start", func(event *Event, ctx Context) (any, error) {
			extBStarted = true
			return nil, nil
		})
		api.RegisterCommand("cmd-b", Command{Description: "from ext-b"})
	})

	cwd := t.TempDir()
	session := newTestSession(t, cwd)
	defer session.Close()

	// Initial setup with ext-a
	result, err := Setup(session, core.NewEventBus(), SetupOptions{
		EnabledNames: []string{"ext-a"},
	})
	if err != nil {
		t.Fatal(err)
	}

	_ = result.Runner.EmitSessionStart()
	if !extAStarted {
		t.Error("ext-a should have received session_start")
	}
	if cmds := result.Runner.GetCommands(); len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	} else if cmds["cmd-a"] == nil {
		t.Error("expected cmd-a")
	}

	// Reload with ext-b instead
	extAStarted = false
	extBStarted = false
	if err := result.Reload(context.Background(), []string{"ext-b"}); err != nil {
		t.Fatal(err)
	}

	// ext-a should have received shutdown
	if !extAShutdown {
		t.Error("ext-a should have received session_shutdown during reload")
	}

	// ext-b should have received start
	if !extBStarted {
		t.Error("ext-b should have received session_start after reload")
	}

	// ext-a should NOT have received start again
	if extAStarted {
		t.Error("ext-a should not have received session_start after reload")
	}

	// Commands should now be from ext-b only
	cmds := result.Runner.GetCommands()
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command after reload, got %d", len(cmds))
	}
	if cmds["cmd-b"] == nil {
		t.Error("expected cmd-b after reload")
	}
	if cmds["cmd-a"] != nil {
		t.Error("expected cmd-a to be gone after reload")
	}
}

func TestSetupResultReloadToEmpty(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	var shutdownCalled bool
	Register("ext-temp", func(api API) {
		api.On("session_shutdown", func(event *Event, ctx Context) (any, error) {
			shutdownCalled = true
			return nil, nil
		})
		api.On("tool_call", func(event *Event, ctx Context) (any, error) {
			return &ToolCallResult{Block: true, Reason: "blocked by ext"}, nil
		})
	})

	cwd := t.TempDir()
	session := newTestSession(t, cwd)
	defer session.Close()

	result, err := Setup(session, core.NewEventBus(), SetupOptions{
		EnabledNames: []string{"ext-temp"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify hook blocks
	hooks := session.Hooks()
	if hooks == nil || hooks.OnToolCall == nil {
		t.Fatal("expected hooks")
	}
	block := hooks.OnToolCall("tc1", "bash", map[string]any{})
	if block == nil {
		t.Fatal("expected block before reload")
	}

	// Reload to empty (disable all extensions)
	if err := result.Reload(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !shutdownCalled {
		t.Error("expected shutdown on reload to empty")
	}

	// Hooks still exist but should be no-ops (no handlers registered)
	block = hooks.OnToolCall("tc2", "bash", map[string]any{})
	if block != nil {
		t.Error("expected no block after reload to empty (no handlers)")
	}
}

func TestSetupResultReloadFromEmpty(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	var extStarted bool
	Register("ext-late", func(api API) {
		api.On("session_start", func(event *Event, ctx Context) (any, error) {
			extStarted = true
			return nil, nil
		})
		api.On("tool_call", func(event *Event, ctx Context) (any, error) {
			if event.ToolCall != nil && event.ToolCall.ToolName == "blocked" {
				return &ToolCallResult{Block: true, Reason: "late blocker"}, nil
			}
			return nil, nil
		})
	})

	cwd := t.TempDir()
	session := newTestSession(t, cwd)
	defer session.Close()

	// Start with no extensions
	result, err := Setup(session, core.NewEventBus(), SetupOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Hooks exist but are no-ops
	hooks := session.Hooks()
	if hooks == nil || hooks.OnToolCall == nil {
		t.Fatal("hooks should always be set for reload support")
	}
	block := hooks.OnToolCall("tc1", "blocked", map[string]any{})
	if block != nil {
		t.Error("expected no block with no extensions")
	}

	// Reload to add an extension
	if err := result.Reload(context.Background(), []string{"ext-late"}); err != nil {
		t.Fatal(err)
	}

	if !extStarted {
		t.Error("ext-late should have received session_start")
	}

	// Now hook should block
	block = hooks.OnToolCall("tc2", "blocked", map[string]any{})
	if block == nil {
		t.Error("expected block after reload with ext-late")
	}
	if block != nil && block.Reason != "late blocker" {
		t.Errorf("expected reason 'late blocker', got %q", block.Reason)
	}
}

// ============================================================================
// bridgeSessionEvents — all event type mappings
// ============================================================================

func TestBridgeSessionEvents_TurnStart(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	var got *TurnStartEvent
	Register("turn-start-ext", func(api API) {
		api.On("turn_start", func(event *Event, ctx Context) (any, error) {
			got = event.TurnStart
			return nil, nil
		})
	})

	cwd := t.TempDir()
	session := newTestSession(t, cwd)
	defer session.Close()

	_, err := Setup(session, core.NewEventBus(), SetupOptions{
		EnabledNames: []string{"turn-start-ext"},
	})
	if err != nil {
		t.Fatal(err)
	}

	session.PublishEvent(core.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{Type: agent.EventTurnStart},
	})

	if got == nil {
		t.Fatal("expected turn_start event to be bridged to extension")
	}
}

func TestBridgeSessionEvents_TurnEnd(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	var got *TurnEndEvent
	Register("turn-end-ext", func(api API) {
		api.On("turn_end", func(event *Event, ctx Context) (any, error) {
			got = event.TurnEnd
			return nil, nil
		})
	})

	cwd := t.TempDir()
	session := newTestSession(t, cwd)
	defer session.Close()

	_, err := Setup(session, core.NewEventBus(), SetupOptions{
		EnabledNames: []string{"turn-end-ext"},
	})
	if err != nil {
		t.Fatal(err)
	}

	turnMsg := agent.AgentMessage{Message: ai.NewUserMsg("hello", 0)}
	session.PublishEvent(core.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{
			Type:        agent.EventTurnEnd,
			TurnMessage: &turnMsg,
			ToolResults: []ai.ToolResultMessage{
				{ToolCallID: "tc1"},
			},
		},
	})

	if got == nil {
		t.Fatal("expected turn_end event to be bridged to extension")
	}
	if len(got.ToolResults) != 1 {
		t.Errorf("expected 1 tool result, got %d", len(got.ToolResults))
	}
}

func TestBridgeSessionEvents_TurnEndNilFields(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	var got *TurnEndEvent
	Register("turn-end-nil-ext", func(api API) {
		api.On("turn_end", func(event *Event, ctx Context) (any, error) {
			got = event.TurnEnd
			return nil, nil
		})
	})

	cwd := t.TempDir()
	session := newTestSession(t, cwd)
	defer session.Close()

	_, err := Setup(session, core.NewEventBus(), SetupOptions{
		EnabledNames: []string{"turn-end-nil-ext"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// turn_end with nil TurnMessage and ToolResults
	session.PublishEvent(core.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{Type: agent.EventTurnEnd},
	})

	if got == nil {
		t.Fatal("expected turn_end event")
	}
	if got.ToolResults != nil {
		t.Errorf("expected nil tool results, got %v", got.ToolResults)
	}
}

func TestBridgeSessionEvents_MessageStart(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	var got *MessageStartEvent
	Register("msg-start-ext", func(api API) {
		api.On("message_start", func(event *Event, ctx Context) (any, error) {
			got = event.MessageStart
			return nil, nil
		})
	})

	cwd := t.TempDir()
	session := newTestSession(t, cwd)
	defer session.Close()

	_, err := Setup(session, core.NewEventBus(), SetupOptions{
		EnabledNames: []string{"msg-start-ext"},
	})
	if err != nil {
		t.Fatal(err)
	}

	msg := agent.AgentMessage{Message: ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{ai.NewTextContent("hi")},
	})}
	session.PublishEvent(core.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{
			Type:    agent.EventMessageStart,
			Message: &msg,
		},
	})

	if got == nil {
		t.Fatal("expected message_start event to be bridged")
	}
}

func TestBridgeSessionEvents_MessageEnd(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	var got *MessageEndEvent
	Register("msg-end-ext", func(api API) {
		api.On("message_end", func(event *Event, ctx Context) (any, error) {
			got = event.MessageEnd
			return nil, nil
		})
	})

	cwd := t.TempDir()
	session := newTestSession(t, cwd)
	defer session.Close()

	_, err := Setup(session, core.NewEventBus(), SetupOptions{
		EnabledNames: []string{"msg-end-ext"},
	})
	if err != nil {
		t.Fatal(err)
	}

	msg := agent.AgentMessage{Message: ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{ai.NewTextContent("done")},
	})}
	session.PublishEvent(core.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{
			Type:    agent.EventMessageEnd,
			Message: &msg,
		},
	})

	if got == nil {
		t.Fatal("expected message_end event to be bridged")
	}
}

func TestBridgeSessionEvents_MessageStartNilMessage(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	var called bool
	Register("msg-start-nil-ext", func(api API) {
		api.On("message_start", func(event *Event, ctx Context) (any, error) {
			called = true
			return nil, nil
		})
	})

	cwd := t.TempDir()
	session := newTestSession(t, cwd)
	defer session.Close()

	_, err := Setup(session, core.NewEventBus(), SetupOptions{
		EnabledNames: []string{"msg-start-nil-ext"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// message_start with nil Message should not emit
	session.PublishEvent(core.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{
			Type:    agent.EventMessageStart,
			Message: nil,
		},
	})

	if called {
		t.Error("expected message_start with nil message NOT to be emitted")
	}
}

func TestBridgeSessionEvents_ToolExecutionStart(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	var got *ToolExecutionStartEvent
	Register("tool-exec-start-ext", func(api API) {
		api.On("tool_execution_start", func(event *Event, ctx Context) (any, error) {
			got = event.ToolExecutionStart
			return nil, nil
		})
	})

	cwd := t.TempDir()
	session := newTestSession(t, cwd)
	defer session.Close()

	_, err := Setup(session, core.NewEventBus(), SetupOptions{
		EnabledNames: []string{"tool-exec-start-ext"},
	})
	if err != nil {
		t.Fatal(err)
	}

	session.PublishEvent(core.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{
			Type:       agent.EventToolExecutionStart,
			ToolCallID: "tc-123",
			ToolName:   "bash",
			Args:       map[string]any{"command": "ls"},
		},
	})

	if got == nil {
		t.Fatal("expected tool_execution_start event to be bridged")
	}
	if got.ToolCallID != "tc-123" {
		t.Errorf("expected tool call ID 'tc-123', got %q", got.ToolCallID)
	}
	if got.ToolName != "bash" {
		t.Errorf("expected tool name 'bash', got %q", got.ToolName)
	}
	if args, ok := got.Args.(map[string]any); !ok || args["command"] != "ls" {
		t.Errorf("expected args with command=ls, got %v", got.Args)
	}
}

func TestBridgeSessionEvents_ToolExecutionEnd(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	var got *ToolExecutionEndEvent
	Register("tool-exec-end-ext", func(api API) {
		api.On("tool_execution_end", func(event *Event, ctx Context) (any, error) {
			got = event.ToolExecutionEnd
			return nil, nil
		})
	})

	cwd := t.TempDir()
	session := newTestSession(t, cwd)
	defer session.Close()

	_, err := Setup(session, core.NewEventBus(), SetupOptions{
		EnabledNames: []string{"tool-exec-end-ext"},
	})
	if err != nil {
		t.Fatal(err)
	}

	session.PublishEvent(core.AgentSessionEvent{
		AgentEvent: &agent.AgentEvent{
			Type:       agent.EventToolExecutionEnd,
			ToolCallID: "tc-456",
			ToolName:   "read",
			Result:     "file contents",
			IsError:    true,
		},
	})

	if got == nil {
		t.Fatal("expected tool_execution_end event to be bridged")
	}
	if got.ToolCallID != "tc-456" {
		t.Errorf("expected tool call ID 'tc-456', got %q", got.ToolCallID)
	}
	if got.ToolName != "read" {
		t.Errorf("expected tool name 'read', got %q", got.ToolName)
	}
	if got.Result != "file contents" {
		t.Errorf("expected result 'file contents', got %v", got.Result)
	}
	if !got.IsError {
		t.Error("expected IsError=true")
	}
}

func TestBridgeSessionEvents_NonAgentEventIgnored(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	var called bool
	Register("all-events-ext", func(api API) {
		api.On("agent_start", func(event *Event, ctx Context) (any, error) {
			called = true
			return nil, nil
		})
	})

	cwd := t.TempDir()
	session := newTestSession(t, cwd)
	defer session.Close()

	_, err := Setup(session, core.NewEventBus(), SetupOptions{
		EnabledNames: []string{"all-events-ext"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Session-only event (no AgentEvent) should be silently ignored
	session.PublishEvent(core.AgentSessionEvent{
		Type: "auto_compaction_start",
	})

	if called {
		t.Error("expected non-agent events to be ignored by bridge")
	}
}

func TestSetupAddsExtensionToolsToAgent(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	var toolExecuted bool
	Register("tool-ext", func(api API) {
		api.RegisterTool(ToolDefinition{
			Name:        "my_ext_tool",
			Label:       "My Extension Tool",
			Description: "A test extension tool",
			Parameters:  map[string]any{"type": "object"},
			Execute: func(ctx ToolContext) (agent.AgentToolResult, error) {
				toolExecuted = true
				return agent.AgentToolResult{
					Content: []ai.ToolResultContent{{Type: "text", Text: "ext tool result"}},
				}, nil
			},
		})
	})

	cwd := t.TempDir()
	session := newTestSession(t, cwd)
	defer session.Close()

	_, err := Setup(session, core.NewEventBus(), SetupOptions{
		EnabledNames: []string{"tool-ext"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify the extension tool was added to the agent's tool list
	state := session.Agent.State()
	var found bool
	for _, tool := range state.Tools {
		if tool.Name == "my_ext_tool" {
			found = true
			// Execute it to verify wiring
			result, err := tool.Execute(context.Background(), "tc1", map[string]any{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !toolExecuted {
				t.Error("expected extension tool to be executed")
			}
			if len(result.Content) != 1 || result.Content[0].Text != "ext tool result" {
				t.Errorf("unexpected result: %v", result)
			}
			break
		}
	}
	if !found {
		t.Error("expected extension tool 'my_ext_tool' in agent's tool list")
	}
}

func TestReloadRemovesAndAddsExtensionTools(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	Register("ext-with-tool-a", func(api API) {
		api.RegisterTool(ToolDefinition{
			Name:        "tool_a",
			Label:       "Tool A",
			Description: "Tool from ext A",
			Parameters:  map[string]any{"type": "object"},
			Execute: func(ctx ToolContext) (agent.AgentToolResult, error) {
				return agent.AgentToolResult{
					Content: []ai.ToolResultContent{{Type: "text", Text: "a"}},
				}, nil
			},
		})
	})

	Register("ext-with-tool-b", func(api API) {
		api.RegisterTool(ToolDefinition{
			Name:        "tool_b",
			Label:       "Tool B",
			Description: "Tool from ext B",
			Parameters:  map[string]any{"type": "object"},
			Execute: func(ctx ToolContext) (agent.AgentToolResult, error) {
				return agent.AgentToolResult{
					Content: []ai.ToolResultContent{{Type: "text", Text: "b"}},
				}, nil
			},
		})
	})

	cwd := t.TempDir()
	session := newTestSession(t, cwd)
	defer session.Close()

	// Setup with ext-a
	result, err := Setup(session, core.NewEventBus(), SetupOptions{
		EnabledNames: []string{"ext-with-tool-a"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify tool_a is present
	state := session.Agent.State()
	hasToolA := false
	for _, tool := range state.Tools {
		if tool.Name == "tool_a" {
			hasToolA = true
		}
	}
	if !hasToolA {
		t.Error("expected tool_a after initial setup")
	}

	// Reload with ext-b
	if err := result.Reload(context.Background(), []string{"ext-with-tool-b"}); err != nil {
		t.Fatal(err)
	}

	// Verify tool_a is gone and tool_b is present
	state = session.Agent.State()
	hasToolA = false
	hasToolB := false
	for _, tool := range state.Tools {
		if tool.Name == "tool_a" {
			hasToolA = true
		}
		if tool.Name == "tool_b" {
			hasToolB = true
		}
	}
	if hasToolA {
		t.Error("expected tool_a to be removed after reload")
	}
	if !hasToolB {
		t.Error("expected tool_b after reload with ext-b")
	}
}

// ============================================================================
// SendMessage / SendUserMessage delivery routing
// ============================================================================

// TestSendMessage_DeliverAs_Steer verifies that SendMessage with DeliverAs:"steer"
// enqueues the message via agent.Steer().
func TestSendMessage_DeliverAs_Steer(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	Register("steer-ext", func(api API) {
		api.On("session_start", func(event *Event, ctx Context) (any, error) {
			api.SendMessage(CustomMessageSpec{
				CustomType: "test",
				Content:    "steer-me",
			}, &SendMessageOptions{DeliverAs: "steer"})
			return nil, nil
		})
	})

	cwd := t.TempDir()
	session := newTestSession(t, cwd)
	defer session.Close()

	result, err := Setup(session, core.NewEventBus(), SetupOptions{
		EnabledNames: []string{"steer-ext"},
	})
	if err != nil {
		t.Fatal(err)
	}

	_ = result.Runner.EmitSessionStart()

	if !session.Agent.HasQueuedMessages() {
		t.Error("expected agent.Steer() to have queued a message (HasQueuedMessages should be true)")
	}
}

// TestSendMessage_DeliverAs_FollowUp verifies that SendMessage with DeliverAs:"followUp"
// enqueues the message via agent.FollowUp().
func TestSendMessage_DeliverAs_FollowUp(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	Register("followup-ext", func(api API) {
		api.On("session_start", func(event *Event, ctx Context) (any, error) {
			api.SendMessage(CustomMessageSpec{
				CustomType: "test",
				Content:    "follow-me",
			}, &SendMessageOptions{DeliverAs: "followUp"})
			return nil, nil
		})
	})

	cwd := t.TempDir()
	session := newTestSession(t, cwd)
	defer session.Close()

	result, err := Setup(session, core.NewEventBus(), SetupOptions{
		EnabledNames: []string{"followup-ext"},
	})
	if err != nil {
		t.Fatal(err)
	}

	_ = result.Runner.EmitSessionStart()

	if !session.Agent.HasQueuedMessages() {
		t.Error("expected agent.FollowUp() to have queued a message (HasQueuedMessages should be true)")
	}
}

// TestSendMessage_TriggerTurn verifies that SendMessage with TriggerTurn:true
// calls agent.Continue(), which starts the agent loop (emitting EventAgentStart).
func TestSendMessage_TriggerTurn(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	Register("trigger-ext", func(api API) {
		api.On("session_start", func(event *Event, ctx Context) (any, error) {
			api.SendMessage(CustomMessageSpec{
				CustomType: "test",
				Content:    "trigger",
			}, &SendMessageOptions{TriggerTurn: true})
			return nil, nil
		})
	})

	cwd := t.TempDir()
	session := newTestSession(t, cwd)
	defer session.Close()

	// Pre-seed a user message so Continue() doesn't return early with "no messages".
	session.Agent.AppendMessage(agent.AgentMessage{
		Message: ai.NewUserMsg("seed", 0),
	})

	// Subscribe to detect EventAgentStart, which is emitted inside the goroutine
	// spawned by Continue() before any API calls are made.
	started := make(chan struct{}, 1)
	session.Agent.Subscribe(func(e agent.AgentEvent) {
		if e.Type == agent.EventAgentStart {
			select {
			case started <- struct{}{}:
			default:
			}
		}
	})

	result, err := Setup(session, core.NewEventBus(), SetupOptions{
		EnabledNames: []string{"trigger-ext"},
	})
	if err != nil {
		t.Fatal(err)
	}

	_ = result.Runner.EmitSessionStart()

	select {
	case <-started:
		// agent.Continue() was called and the loop started — pass.
	case <-time.After(2 * time.Second):
		t.Error("expected agent.Continue() to be called (TriggerTurn=true), but EventAgentStart was never received")
	}
}

// TestSendUserMessage_DeliverAs_Steer verifies that SendUserMessage with DeliverAs:"steer"
// enqueues the message via agent.Steer().
func TestSendUserMessage_DeliverAs_Steer(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	Register("user-steer-ext", func(api API) {
		api.On("session_start", func(event *Event, ctx Context) (any, error) {
			api.SendUserMessage("steer user msg", &SendUserMessageOptions{DeliverAs: "steer"})
			return nil, nil
		})
	})

	cwd := t.TempDir()
	session := newTestSession(t, cwd)
	defer session.Close()

	result, err := Setup(session, core.NewEventBus(), SetupOptions{
		EnabledNames: []string{"user-steer-ext"},
	})
	if err != nil {
		t.Fatal(err)
	}

	_ = result.Runner.EmitSessionStart()

	if !session.Agent.HasQueuedMessages() {
		t.Error("expected agent.Steer() to have queued a message for SendUserMessage with DeliverAs:steer")
	}
}

// TestSendUserMessage_DeliverAs_FollowUp verifies that SendUserMessage with DeliverAs:"followUp"
// enqueues the message via agent.FollowUp().
func TestSendUserMessage_DeliverAs_FollowUp(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	Register("user-followup-ext", func(api API) {
		api.On("session_start", func(event *Event, ctx Context) (any, error) {
			api.SendUserMessage("followup user msg", &SendUserMessageOptions{DeliverAs: "followUp"})
			return nil, nil
		})
	})

	cwd := t.TempDir()
	session := newTestSession(t, cwd)
	defer session.Close()

	result, err := Setup(session, core.NewEventBus(), SetupOptions{
		EnabledNames: []string{"user-followup-ext"},
	})
	if err != nil {
		t.Fatal(err)
	}

	_ = result.Runner.EmitSessionStart()

	if !session.Agent.HasQueuedMessages() {
		t.Error("expected agent.FollowUp() to have queued a message for SendUserMessage with DeliverAs:followUp")
	}
}

// TestHookFiresExactlyOnceWithPreExistingTools is a regression test for the
// double-wrapping bug. When a session has pre-existing tools AND extension tools
// are registered, SetHooks wraps the pre-existing tools once. addExtensionTools
// must NOT re-wrap those tools — only the new extension tools should be wrapped.
//
// Before the fix, the OnToolCall hook fired twice per execution for pre-existing
// tools when any extension tool was also registered.
func TestHookFiresExactlyOnceWithPreExistingTools(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	var hookCalls int

	Register("counter-ext", func(api API) {
		// Event handler that counts tool_call interceptions for "base_tool".
		api.On("tool_call", func(event *Event, ctx Context) (any, error) {
			if event.ToolCall != nil && event.ToolCall.ToolName == "base_tool" {
				hookCalls++
			}
			return nil, nil
		})
		// Registering an extension tool triggers addExtensionTools to run,
		// which is where the double-wrap bug manifested.
		api.RegisterTool(ToolDefinition{
			Name:        "ext_tool",
			Label:       "Extension Tool",
			Description: "A tool registered by the extension",
			Parameters:  map[string]any{"type": "object"},
			Execute: func(ctx ToolContext) (agent.AgentToolResult, error) {
				return agent.AgentToolResult{
					Content: []ai.ToolResultContent{{Type: "text", Text: "ext done"}},
				}, nil
			},
		})
	})

	cwd := t.TempDir()
	session := newTestSession(t, cwd)
	defer session.Close()

	// Pre-load a tool onto the agent BEFORE Setup, simulating production where
	// DefaultCodingTools (bash, read, etc.) are set before extensions are loaded.
	var executeCount int
	baseTool := agent.AgentTool{
		Tool: ai.Tool{Name: "base_tool"},
		Execute: func(ctx context.Context, id string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			executeCount++
			return agent.AgentToolResult{
				Content: []ai.ToolResultContent{{Type: "text", Text: "base done"}},
			}, nil
		},
	}
	session.Agent.SetTools([]agent.AgentTool{baseTool})

	_, err := Setup(session, core.NewEventBus(), SetupOptions{
		EnabledNames: []string{"counter-ext"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Find and execute the pre-existing base_tool through the agent's (wrapped) tool list.
	state := session.Agent.State()
	var found *agent.AgentTool
	for i := range state.Tools {
		if state.Tools[i].Name == "base_tool" {
			found = &state.Tools[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected base_tool in agent state after Setup")
	}

	if _, err := found.Execute(context.Background(), "tc1", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}

	if executeCount != 1 {
		t.Errorf("expected base_tool.Execute to be called exactly once, got %d", executeCount)
	}
	if hookCalls != 1 {
		t.Errorf("expected OnToolCall hook to fire exactly once, got %d (double-wrap bug?)", hookCalls)
	}
}
