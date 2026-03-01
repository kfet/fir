package extproc

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
)

// mockBridgeAPI implements BridgeAPI for testing.
type mockBridgeAPI struct {
	execCalled      bool
	execCmd         string
	sessionName     string
	activeTools     []string
	sentMessages    []CustomMessageSpec
	userMessages    []string
	labels          map[string]string
	modelSet        *ai.Model
	toolsRegistered []ToolDefinition
}

func newMockAPI() *mockBridgeAPI {
	return &mockBridgeAPI{labels: make(map[string]string)}
}

func (m *mockBridgeAPI) RegisterTool(def ToolDefinition) {
	m.toolsRegistered = append(m.toolsRegistered, def)
}
func (m *mockBridgeAPI) SendMessage(msg CustomMessageSpec, _ *SendMessageOptions) {
	m.sentMessages = append(m.sentMessages, msg)
}
func (m *mockBridgeAPI) SendUserMessage(content string, _ *SendUserMessageOptions) {
	m.userMessages = append(m.userMessages, content)
}
func (m *mockBridgeAPI) SetSessionName(name string)    { m.sessionName = name }
func (m *mockBridgeAPI) GetSessionName() string        { return m.sessionName }
func (m *mockBridgeAPI) SetLabel(id, label string)     { m.labels[id] = label }
func (m *mockBridgeAPI) ClearLabel(id string)          { delete(m.labels, id) }
func (m *mockBridgeAPI) GetActiveTools() []string      { return m.activeTools }
func (m *mockBridgeAPI) SetActiveTools(names []string)  { m.activeTools = names }
func (m *mockBridgeAPI) SetModel(model *ai.Model) bool  { m.modelSet = model; return true }
func (m *mockBridgeAPI) Exec(cmd string, args []string) (ExecResult, error) {
	m.execCalled = true
	m.execCmd = cmd
	return ExecResult{Stdout: "ok", ExitCode: 0}, nil
}

// Verify mockBridgeAPI satisfies BridgeAPI at compile time.
var _ BridgeAPI = (*mockBridgeAPI)(nil)

// pipePair creates a Bridge connected via pipes (no real process).
func pipePair(caps *InitResult) (*Bridge, *Codec) {
	extR, firW := io.Pipe()
	firR, extW := io.Pipe()

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

	go func() {
		for {
			_, err := extCodec.ReadMessage()
			if err != nil {
				return
			}
		}
	}()

	_, err := b.CallHook("hook/test", nil, 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestBridge_CallHook_Success(t *testing.T) {
	b, extCodec := pipePair(&InitResult{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, newMockAPI()) }()

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
	go func() { _ = b.Run(ctx, api) }()

	params := json.RawMessage(`{"command":"echo","args":["hello"]}`)
	_ = extCodec.WriteRequest(1, "exec", &params)

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
	go func() { _ = b.Run(ctx, api) }()

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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, api) }()

	go func() {
		msg, err := extCodec.ReadMessage()
		if err != nil {
			return
		}
		req := msg.(*Request)
		result := json.RawMessage(`{"content":[{"type":"text","text":"result"}],"is_error":false}`)
		_ = extCodec.WriteResponse(req.ID, &result, nil)
	}()

	toolResult, err := api.toolsRegistered[0].Execute(ToolContext{
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
