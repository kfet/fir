package extension

import (
	"context"
	"testing"

	"github.com/kfet/pi-go/pkg/agent"
	"github.com/kfet/pi-go/pkg/ai"
	"github.com/kfet/pi-go/pkg/core"
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

	result, err := Setup(session, core.NewEventBus())
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

	result, err := Setup(session, core.NewEventBus())
	if err != nil {
		t.Fatal(err)
	}

	// Hooks should not be set when no extensions exist
	if session.Hooks() != nil {
		t.Error("expected no hooks when no extensions registered")
	}

	// Runner should still exist
	if result == nil || result.Runner == nil {
		t.Fatal("expected non-nil runner even with no extensions")
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

	_, err := Setup(session, core.NewEventBus())
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
