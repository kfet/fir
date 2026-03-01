package extproc

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/core"
	"github.com/kfet/fir/pkg/extension"
)

// mockAPI implements extension.API for testing.
type mockAPI struct {
	execCalled      bool
	execCmd         string
	sessionName     string
	activeTools     []string
	sentMessages    []extension.CustomMessageSpec
	userMessages    []string
	labels          map[string]string
	modelSet        *ai.Model
	toolsRegistered []extension.ToolDefinition
}

func newMockAPI() *mockAPI {
	return &mockAPI{labels: make(map[string]string)}
}

func (m *mockAPI) On(string, extension.Handler)                                     {}
func (m *mockAPI) RegisterTool(def extension.ToolDefinition)                        { m.toolsRegistered = append(m.toolsRegistered, def) }
func (m *mockAPI) RegisterCommand(string, extension.Command)                        {}
func (m *mockAPI) RegisterFlag(string, extension.Flag)                              {}
func (m *mockAPI) RegisterShortcut(string, extension.ShortcutHandler)               {}
func (m *mockAPI) SendMessage(msg extension.CustomMessageSpec, _ *extension.SendMessageOptions) {
	m.sentMessages = append(m.sentMessages, msg)
}
func (m *mockAPI) SendUserMessage(content string, _ *extension.SendUserMessageOptions) {
	m.userMessages = append(m.userMessages, content)
}
func (m *mockAPI) AppendEntry(string, any)              {}
func (m *mockAPI) SetSessionName(name string)           { m.sessionName = name }
func (m *mockAPI) GetSessionName() string               { return m.sessionName }
func (m *mockAPI) SetLabel(id, label string)            { m.labels[id] = label }
func (m *mockAPI) ClearLabel(id string)                 { delete(m.labels, id) }
func (m *mockAPI) GetActiveTools() []string             { return m.activeTools }
func (m *mockAPI) GetAllTools() []extension.ToolInfo    { return nil }
func (m *mockAPI) SetActiveTools(names []string)        { m.activeTools = names }
func (m *mockAPI) GetCommands() []core.SlashCommandInfo { return nil }
func (m *mockAPI) SetModel(model *ai.Model) bool        { m.modelSet = model; return true }
func (m *mockAPI) GetThinkingLevel() string              { return "" }
func (m *mockAPI) SetThinkingLevel(string)               {}
func (m *mockAPI) GetFlag(string) any                    { return nil }
func (m *mockAPI) Events() core.EventBus                 { return nil }
func (m *mockAPI) Exec(cmd string, args []string) (*extension.ExecResult, error) {
	m.execCalled = true
	m.execCmd = cmd
	return &extension.ExecResult{Stdout: "ok", ExitCode: 0}, nil
}

// pipePair creates a Bridge connected via pipes (no real process).
// Returns the bridge, the codec for the "extension side", and a cleanup func.
func pipePair(caps *InitResult) (*Bridge, *Codec) {
	// fir→ext: fir writes to extR
	extR, firW := io.Pipe()
	// ext→fir: ext writes to firR
	firR, extW := io.Pipe()

	// Create a fake process with a codec wired to the pipes.
	proc := &Process{}
	proc.codec = NewCodec(firR, firW)

	b := NewBridge(proc, caps)
	extCodec := NewCodec(extR, extW)
	return b, extCodec
}

func TestBridge_EmitEvent_Subscribed(t *testing.T) {
	b, extCodec := pipePair(&InitResult{
		Events: []string{"session_start"},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, newMockAPI()) }()

	// Emit a subscribed event.
	go func() {
		_ = b.EmitEvent("session_start", map[string]string{"foo": "bar"})
	}()

	msg, err := extCodec.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	notif, ok := msg.(*Notification)
	if !ok {
		t.Fatalf("expected Notification, got %T", msg)
	}
	if notif.Method != "event/session_start" {
		t.Fatalf("got method %s, want event/session_start", notif.Method)
	}
}

func TestBridge_EmitEvent_NotSubscribed(t *testing.T) {
	b, _ := pipePair(&InitResult{
		Events: []string{"session_start"},
	})

	// Emit an event the extension didn't subscribe to — should be a no-op.
	err := b.EmitEvent("turn_end", nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestBridge_CallHook_Timeout(t *testing.T) {
	b, extCodec := pipePair(&InitResult{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, newMockAPI()) }()

	// Simulate extension: read request but never respond.
	go func() {
		for {
			_, err := extCodec.ReadMessage()
			if err != nil {
				return
			}
			// deliberately do not respond
		}
	}()

	_, err := b.CallHook("hook/test", nil, 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if got := err.Error(); got == "" {
		t.Fatal("expected non-empty error")
	}
}

func TestBridge_CallHook_Success(t *testing.T) {
	b, extCodec := pipePair(&InitResult{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, newMockAPI()) }()

	// Respond to the hook from the extension side.
	go func() {
		msg, err := extCodec.ReadMessage()
		if err != nil {
			return
		}
		req := msg.(*Request)
		result := json.RawMessage(`{"blocked":false}`)
		_ = extCodec.WriteResponse(req.ID, &result, nil)
	}()

	raw, err := b.CallHook("hook/tool_call", map[string]string{"x": "y"}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"blocked":false}` {
		t.Fatalf("unexpected result: %s", raw)
	}
}

func TestBridge_InboundExec(t *testing.T) {
	b, extCodec := pipePair(&InitResult{})
	api := newMockAPI()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = b.Run(ctx, api)
	}()

	// Send an exec request from the extension.
	params := json.RawMessage(`{"command":"echo","args":["hello"]}`)
	_ = extCodec.WriteRequest(1, "exec", &params)

	// Read the response.
	msg, err := extCodec.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	resp, ok := msg.(*Response)
	if !ok {
		t.Fatalf("expected Response, got %T", msg)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	if !api.execCalled {
		t.Fatal("exec was not called on API")
	}
}

func TestBridge_InboundNotify(t *testing.T) {
	b, extCodec := pipePair(&InitResult{})
	api := newMockAPI()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = b.Run(ctx, api)
	}()

	params := json.RawMessage(`{"message":"hello","level":"info"}`)
	_ = extCodec.WriteRequest(1, "notify", &params)

	msg, err := extCodec.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	resp := msg.(*Response)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
}

func TestBridge_RegisterToolsAndExecute(t *testing.T) {
	caps := &InitResult{
		Tools: []ToolSpec{
			{Name: "my_tool", Description: "does stuff", Parameters: map[string]any{"type": "object"}},
		},
	}
	b, extCodec := pipePair(caps)
	api := newMockAPI()

	b.RegisterTools(api)

	if len(api.toolsRegistered) != 1 {
		t.Fatalf("expected 1 tool registered, got %d", len(api.toolsRegistered))
	}
	if api.toolsRegistered[0].Name != "my_tool" {
		t.Fatalf("got tool name %s", api.toolsRegistered[0].Name)
	}

	// Start Run in background to route the response.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, api) }()

	// Execute the tool — the extension side must respond.
	go func() {
		msg, err := extCodec.ReadMessage()
		if err != nil {
			return
		}
		req := msg.(*Request)
		result := json.RawMessage(`{"content":[{"type":"text","text":"result"}],"is_error":false}`)
		_ = extCodec.WriteResponse(req.ID, &result, nil)
	}()

	toolResult, err := api.toolsRegistered[0].Execute(extension.ToolContext{
		ToolCallID: "tc1",
		Params:     map[string]any{"arg": "val"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if toolResult.IsError {
		t.Fatal("expected no error")
	}
	if len(toolResult.Content) != 1 || toolResult.Content[0].Text != "result" {
		t.Fatalf("unexpected content: %+v", toolResult.Content)
	}
}

// Verify mockAPI satisfies extension.API at compile time.
var _ extension.API = (*mockAPI)(nil)
var _ agent.AgentToolResult // ensure import used
