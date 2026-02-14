package extension

import (
	"testing"

	"github.com/kfet/pi-go/pkg/agent"
	"github.com/kfet/pi-go/pkg/ai"
	"github.com/kfet/pi-go/pkg/core"
)

func TestRegistry(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	Register("test-ext", func(api API) {
		api.On("session_start", func(event *Event, ctx Context) (any, error) {
			return nil, nil
		})
	})

	factories := RegisteredFactories()
	if len(factories) != 1 {
		t.Fatalf("expected 1 factory, got %d", len(factories))
	}
	if factories[0].Name != "test-ext" {
		t.Errorf("expected name 'test-ext', got %q", factories[0].Name)
	}
}

func TestClearRegistry(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	Register("a", func(api API) {})
	Register("b", func(api API) {})

	if len(RegisteredFactories()) != 2 {
		t.Fatal("expected 2 factories")
	}

	ClearRegistry()
	if len(RegisteredFactories()) != 0 {
		t.Fatal("expected 0 factories after clear")
	}
}

func TestRunnerLoadAll(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	var gotSessionStart bool
	var gotAgentEnd bool

	Register("test", func(api API) {
		api.On("session_start", func(event *Event, ctx Context) (any, error) {
			gotSessionStart = true
			return nil, nil
		})
		api.On("agent_end", func(event *Event, ctx Context) (any, error) {
			gotAgentEnd = true
			return nil, nil
		})
	})

	runner := NewRunner(core.NewEventBus())
	if err := runner.LoadAll(); err != nil {
		t.Fatal(err)
	}

	if len(runner.Extensions()) != 1 {
		t.Fatalf("expected 1 extension, got %d", len(runner.Extensions()))
	}

	if !runner.HasHandlers("session_start") {
		t.Error("expected handlers for session_start")
	}
	if !runner.HasHandlers("agent_end") {
		t.Error("expected handlers for agent_end")
	}
	if runner.HasHandlers("nonexistent") {
		t.Error("should not have handlers for nonexistent")
	}

	// Emit events
	if err := runner.EmitSessionStart(); err != nil {
		t.Fatal(err)
	}
	if !gotSessionStart {
		t.Error("session_start handler was not called")
	}

	if err := runner.EmitAgentEnd(nil); err != nil {
		t.Fatal(err)
	}
	if !gotAgentEnd {
		t.Error("agent_end handler was not called")
	}
}

func TestRunnerToolCallInterception(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	Register("blocker", func(api API) {
		api.On("tool_call", func(event *Event, ctx Context) (any, error) {
			if event.ToolCall != nil && event.ToolCall.ToolName == "bash" {
				cmd, _ := event.ToolCall.Input["command"].(string)
				if cmd == "rm -rf /" {
					return &ToolCallResult{Block: true, Reason: "Dangerous command"}, nil
				}
			}
			return nil, nil
		})
	})

	runner := NewRunner(core.NewEventBus())
	if err := runner.LoadAll(); err != nil {
		t.Fatal(err)
	}

	// Should block dangerous command
	result := runner.EmitToolCall("tc1", "bash", map[string]any{"command": "rm -rf /"})
	if result == nil {
		t.Fatal("expected block result")
	}
	if !result.Block {
		t.Error("expected block=true")
	}
	if result.Reason != "Dangerous command" {
		t.Errorf("expected reason 'Dangerous command', got %q", result.Reason)
	}

	// Should not block safe command
	result = runner.EmitToolCall("tc2", "bash", map[string]any{"command": "ls"})
	if result != nil {
		t.Error("expected nil result for safe command")
	}

	// Should not block other tools
	result = runner.EmitToolCall("tc3", "read", map[string]any{"path": "/etc/passwd"})
	if result != nil {
		t.Error("expected nil result for non-bash tool")
	}
}

func TestRunnerToolResultModification(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	Register("modifier", func(api API) {
		api.On("tool_result", func(event *Event, ctx Context) (any, error) {
			if event.ToolResult != nil && event.ToolResult.ToolName == "bash" {
				// Append a note to the output
				return &ToolResultResult{
					Content: append(event.ToolResult.Content, ai.ToolResultContent{
						Type: "text",
						Text: "[sandbox: command ran in sandbox]",
					}),
				}, nil
			}
			return nil, nil
		})
	})

	runner := NewRunner(core.NewEventBus())
	if err := runner.LoadAll(); err != nil {
		t.Fatal(err)
	}

	result := runner.EmitToolResult(&ToolResultEvent{
		ToolCallID: "tc1",
		ToolName:   "bash",
		Content:    nil,
	})
	if result == nil {
		t.Fatal("expected result modification")
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Content))
	}
}

func TestRunnerFlags(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	Register("flagtest", func(api API) {
		api.RegisterFlag("verbose", Flag{
			Description: "Enable verbose mode",
			Type:        "boolean",
			Default:     false,
		})
		api.RegisterFlag("output-dir", Flag{
			Description: "Output directory",
			Type:        "string",
			Default:     "/tmp",
		})
	})

	runner := NewRunner(core.NewEventBus())
	if err := runner.LoadAll(); err != nil {
		t.Fatal(err)
	}

	flags := runner.GetFlags()
	if len(flags) != 2 {
		t.Fatalf("expected 2 flags, got %d", len(flags))
	}

	// Check defaults
	if v := runner.GetFlagValue("verbose"); v != false {
		t.Errorf("expected false default, got %v", v)
	}
	if v := runner.GetFlagValue("output-dir"); v != "/tmp" {
		t.Errorf("expected /tmp default, got %v", v)
	}

	// Set value
	runner.SetFlagValue("verbose", true)
	if v := runner.GetFlagValue("verbose"); v != true {
		t.Errorf("expected true after set, got %v", v)
	}
}

func TestRunnerCommands(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	var gotArgs string

	Register("cmdtest", func(api API) {
		api.RegisterCommand("hello", Command{
			Description: "Say hello",
			Handler: func(args string, ctx CommandContext) error {
				gotArgs = args
				return nil
			},
		})
	})

	runner := NewRunner(core.NewEventBus())
	if err := runner.LoadAll(); err != nil {
		t.Fatal(err)
	}

	commands := runner.GetCommands()
	if len(commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(commands))
	}

	if runner.GetCommand("hello") == nil {
		t.Error("expected to find 'hello' command")
	}
	if runner.GetCommand("nonexistent") != nil {
		t.Error("expected nil for nonexistent command")
	}

	// Execute command
	found, err := runner.ExecuteCommand("hello", "world")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("expected command to be found")
	}
	if gotArgs != "world" {
		t.Errorf("expected args 'world', got %q", gotArgs)
	}

	// Non-existent command
	found, err = runner.ExecuteCommand("nonexistent", "")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("expected command not to be found")
	}
}

func TestRunnerTools(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	Register("tooltest", func(api API) {
		api.RegisterTool(ToolDefinition{
			Name:        "my_tool",
			Label:       "My Tool",
			Description: "A test tool",
			Parameters:  map[string]any{"type": "object"},
			Execute: func(ctx ToolContext) (agent.AgentToolResult, error) {
				return agent.AgentToolResult{
					Content: []ai.ToolResultContent{{Type: "text", Text: "done"}},
				}, nil
			},
		})
	})

	runner := NewRunner(core.NewEventBus())
	if err := runner.LoadAll(); err != nil {
		t.Fatal(err)
	}

	tools := runner.GetTools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}

	tool := tools["my_tool"]
	if tool == nil {
		t.Fatal("expected to find 'my_tool'")
	}
	if tool.Name != "my_tool" {
		t.Errorf("expected name 'my_tool', got %q", tool.Name)
	}
	if tool.Description != "A test tool" {
		t.Errorf("expected description 'A test tool', got %q", tool.Description)
	}
}

func TestRunnerInputEvents(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	Register("input", func(api API) {
		api.On("input", func(event *Event, ctx Context) (any, error) {
			if event.Input != nil && event.Input.Text == "ping" {
				return &InputResult{Action: "handled"}, nil
			}
			if event.Input != nil && event.Input.Text == "hello" {
				return &InputResult{Action: "transform", Text: "HELLO"}, nil
			}
			return nil, nil
		})
	})

	runner := NewRunner(core.NewEventBus())
	if err := runner.LoadAll(); err != nil {
		t.Fatal(err)
	}

	// Handled input
	result := runner.EmitInput("ping", nil, "interactive")
	if result == nil {
		t.Fatal("expected result for 'ping'")
	}
	if result.Action != "handled" {
		t.Errorf("expected action 'handled', got %q", result.Action)
	}

	// Transformed input
	result = runner.EmitInput("hello", nil, "interactive")
	if result == nil {
		t.Fatal("expected result for 'hello'")
	}
	if result.Action != "transform" {
		t.Errorf("expected action 'transform', got %q", result.Action)
	}
	if result.Text != "HELLO" {
		t.Errorf("expected text 'HELLO', got %q", result.Text)
	}

	// Passthrough input
	result = runner.EmitInput("other", nil, "interactive")
	if result != nil {
		t.Error("expected nil result for passthrough")
	}
}

func TestRunnerPanicRecovery(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	Register("panicker", func(api API) {
		api.On("session_start", func(event *Event, ctx Context) (any, error) {
			panic("deliberate panic in handler")
		})
	})

	runner := NewRunner(core.NewEventBus())
	if err := runner.LoadAll(); err != nil {
		t.Fatal(err)
	}

	// Should not panic — runner recovers
	if err := runner.EmitSessionStart(); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerFactoryPanicRecovery(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	Register("panicker", func(api API) {
		panic("panic during init")
	})

	runner := NewRunner(core.NewEventBus())
	// Should not panic — LoadAll recovers
	if err := runner.LoadAll(); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerMultipleExtensions(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	var order []string

	Register("ext1", func(api API) {
		api.On("session_start", func(event *Event, ctx Context) (any, error) {
			order = append(order, "ext1")
			return nil, nil
		})
	})

	Register("ext2", func(api API) {
		api.On("session_start", func(event *Event, ctx Context) (any, error) {
			order = append(order, "ext2")
			return nil, nil
		})
	})

	runner := NewRunner(core.NewEventBus())
	if err := runner.LoadAll(); err != nil {
		t.Fatal(err)
	}

	_ = runner.EmitSessionStart()

	if len(order) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(order))
	}
	if order[0] != "ext1" || order[1] != "ext2" {
		t.Errorf("expected [ext1, ext2], got %v", order)
	}
}

func TestRunnerNoopUIContext(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	var uiWasNil bool

	Register("ui-test", func(api API) {
		api.On("session_start", func(event *Event, ctx Context) (any, error) {
			ui := ctx.UI()
			// Should get noop UI, not nil
			uiWasNil = (ui == nil)
			// These should not panic
			ui.Notify("test", "info")
			ui.SetStatus("key", "value")
			ui.SetWidget("key", []string{"line1"})
			ui.ClearWidget("key")
			return nil, nil
		})
	})

	runner := NewRunner(core.NewEventBus())
	if err := runner.LoadAll(); err != nil {
		t.Fatal(err)
	}

	_ = runner.EmitSessionStart()

	if uiWasNil {
		t.Error("expected non-nil UI context (noop)")
	}
}

func TestRunnerContextEvent(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	Register("ctx-test", func(api API) {
		api.On("context", func(event *Event, ctx Context) (any, error) {
			// Filter out messages (simplified test)
			if event.Context != nil && len(event.Context.Messages) > 1 {
				return &ContextResult{
					Messages: event.Context.Messages[:1],
				}, nil
			}
			return nil, nil
		})
	})

	runner := NewRunner(core.NewEventBus())
	if err := runner.LoadAll(); err != nil {
		t.Fatal(err)
	}

	// Should not be used because ToolResultResult.Content is []ai.ToolResultContent
	// This test just verifies context event handler works
	if !runner.HasHandlers("context") {
		t.Error("expected context handlers")
	}
}

func TestRunnerGetFlagWithPrefix(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	var flagVal any

	Register("flagpfx", func(api API) {
		api.RegisterFlag("no-sandbox", Flag{
			Type:    "boolean",
			Default: false,
		})

		api.On("session_start", func(event *Event, ctx Context) (any, error) {
			flagVal = api.GetFlag("--no-sandbox") // with -- prefix
			return nil, nil
		})
	})

	runner := NewRunner(core.NewEventBus())
	if err := runner.LoadAll(); err != nil {
		t.Fatal(err)
	}

	runner.SetFlagValue("no-sandbox", true)
	_ = runner.EmitSessionStart()

	if flagVal != true {
		t.Errorf("expected true, got %v", flagVal)
	}
}

func TestRunnerEventBus(t *testing.T) {
	ClearRegistry()
	defer ClearRegistry()

	var received bool

	Register("bus-test", func(api API) {
		bus := api.Events()
		bus.On("custom:event", func(data any) {
			received = true
		})

		api.On("session_start", func(event *Event, ctx Context) (any, error) {
			bus := api.Events()
			bus.Emit("custom:event", map[string]string{"hello": "world"})
			return nil, nil
		})
	})

	runner := NewRunner(core.NewEventBus())
	if err := runner.LoadAll(); err != nil {
		t.Fatal(err)
	}

	_ = runner.EmitSessionStart()

	if !received {
		t.Error("expected custom event to be received via event bus")
	}
}
