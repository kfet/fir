package extension

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
)

// mockBridgeAPI implements BridgeAPI for testing.
type mockBridgeAPI struct {
	mu              sync.Mutex
	execCalled      bool
	execCmd         string
	sessionName     string
	activeTools     []string
	sentMessages    []CustomMessageSpec
	sentMsgOpts     []*SendMessageOptions
	userMessages    []string
	userMsgOpts     []*SendUserMessageOptions
	labels          map[string]string
	modelSet        *ai.Model
	toolsRegistered []ToolDefinition
	sessionData     map[string]string
}

func newMockAPI() *mockBridgeAPI {
	return &mockBridgeAPI{labels: make(map[string]string)}
}

func (m *mockBridgeAPI) toolCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.toolsRegistered)
}

func (m *mockBridgeAPI) clearTools() {
	m.mu.Lock()
	m.toolsRegistered = nil
	m.mu.Unlock()
}

func (m *mockBridgeAPI) RegisterTool(def ToolDefinition) {
	m.mu.Lock()
	m.toolsRegistered = append(m.toolsRegistered, def)
	m.mu.Unlock()
}
func (m *mockBridgeAPI) SendMessage(msg CustomMessageSpec, opts *SendMessageOptions) {
	m.mu.Lock()
	m.sentMessages = append(m.sentMessages, msg)
	m.sentMsgOpts = append(m.sentMsgOpts, opts)
	m.mu.Unlock()
}
func (m *mockBridgeAPI) SendUserMessage(content string, opts *SendUserMessageOptions) {
	m.mu.Lock()
	m.userMessages = append(m.userMessages, content)
	m.userMsgOpts = append(m.userMsgOpts, opts)
	m.mu.Unlock()
}
func (m *mockBridgeAPI) SetSessionName(name string)    { m.sessionName = name }
func (m *mockBridgeAPI) GetSessionName() string        { return m.sessionName }
func (m *mockBridgeAPI) SetLabel(id, label string)     { m.labels[id] = label }
func (m *mockBridgeAPI) ClearLabel(id string)          { delete(m.labels, id) }
func (m *mockBridgeAPI) GetActiveTools() []string      { return m.activeTools }
func (m *mockBridgeAPI) SetActiveTools(names []string) { m.activeTools = names }
func (m *mockBridgeAPI) SetModel(model *ai.Model) bool { m.modelSet = model; return true }
func (m *mockBridgeAPI) ContinueSession() error        { return nil }
func (m *mockBridgeAPI) SideQuery(question string) (string, error) {
	return "mock response", nil
}
func (m *mockBridgeAPI) Exec(cmd string, args []string) (ExecResult, error) {
	m.execCalled = true
	m.execCmd = cmd
	return ExecResult{Stdout: "ok", ExitCode: 0}, nil
}
func (m *mockBridgeAPI) SetSessionData(key, value string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessionData == nil {
		m.sessionData = make(map[string]string)
	}
	m.sessionData[key] = value
}
func (m *mockBridgeAPI) GetSessionData(key string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.sessionData[key]
	return v, ok
}
func (m *mockBridgeAPI) CallTool(name string, params map[string]any) (ToolResult, error) {
	return ToolResult{
		Content: []ai.ToolResultContent{{Text: "mock tool result for " + name}},
	}, nil
}
func (m *mockBridgeAPI) PrependContext(_ string) {}
func (m *mockBridgeAPI) ListTools() []ToolInfo   { return nil }

// Verify mockBridgeAPI satisfies BridgeAPI at compile time.
var _ BridgeAPI = (*mockBridgeAPI)(nil)

// slowSideQueryAPI wraps mockBridgeAPI with a configurable delay on SideQuery.
type slowSideQueryAPI struct {
	*mockBridgeAPI
	delay time.Duration
}

func (s *slowSideQueryAPI) SideQuery(question string) (string, error) {
	time.Sleep(s.delay)
	return s.mockBridgeAPI.SideQuery(question)
}

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

func TestBridge_CallHook_ActivityExtendsTimeout(t *testing.T) {
	b, extCodec := pipePair(&InitResult{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, newMockAPI()) }()

	// Extension reads the hook request, sends inbound requests to simulate
	// activity (like aside's call_tool), then responds after 150ms.
	// The hook timeout is 100ms, so without activity-awareness it would fail.
	go func() {
		msg, err := extCodec.ReadMessage()
		if err != nil {
			return
		}
		req := msg.(*Request)

		// Send inbound requests every 40ms to keep activity alive.
		for i := 0; i < 4; i++ {
			time.Sleep(40 * time.Millisecond)
			// Send a "notify" request (simplest inbound request).
			_ = extCodec.WriteRequest(1000+i, "notify", map[string]string{
				"level": "info", "message": "working...",
			})
			// Drain the response.
			_, _ = extCodec.ReadMessage()
		}

		// Now respond to the original hook.
		result := json.RawMessage(`{"ok":true}`)
		_ = extCodec.WriteResponse(req.ID, &result, nil)
	}()

	raw, err := b.CallHook("hook/test", nil, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("expected activity to extend timeout, got: %v", err)
	}
	if string(raw) != `{"ok":true}` {
		t.Fatalf("unexpected result: %s", raw)
	}
}

func TestBridge_CallHook_ActivityStopsTimeoutFires(t *testing.T) {
	b, extCodec := pipePair(&InitResult{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, newMockAPI()) }()

	// Extension sends activity for 80ms, then goes silent.
	// Timeout is 60ms, so after activity stops the timeout should fire.
	go func() {
		msg, err := extCodec.ReadMessage()
		if err != nil {
			return
		}
		_ = msg.(*Request) // read the hook request but never respond

		// Send activity for 80ms.
		for i := 0; i < 4; i++ {
			time.Sleep(20 * time.Millisecond)
			_ = extCodec.WriteRequest(1000+i, "notify", map[string]string{
				"level": "info", "message": "working...",
			})
			_, _ = extCodec.ReadMessage() // drain response
		}
		// Now go silent — never respond to the hook.
	}()

	_, err := b.CallHook("hook/test", nil, 60*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error after activity stopped")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got: %v", err)
	}
}

func TestBridge_KeepAlive_ExtendsDuringSlowSideQuery(t *testing.T) {
	// Use a fast keepAlive interval so we can observe it in a short test.
	old := keepAliveInterval
	keepAliveInterval = 20 * time.Millisecond
	defer func() { keepAliveInterval = old }()

	b, extCodec := pipePair(&InitResult{})

	// Slow API: side_query takes 200ms.
	api := &slowSideQueryAPI{mockBridgeAPI: newMockAPI(), delay: 200 * time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, api) }()

	// Extension sends a side_query request (simulating what aside does
	// inside a tool_call hook). The bridge's handleInbound runs keepAlive
	// during the slow SideQuery call.
	params := json.RawMessage(`{"question":"test"}`)
	_ = extCodec.WriteRequest(1, "side_query", &params)

	// Wait 100ms — keepAlive should have ticked multiple times by now.
	time.Sleep(100 * time.Millisecond)
	activity := time.Unix(0, b.lastActivity.Load())
	age := time.Since(activity)
	if age > 50*time.Millisecond {
		t.Fatalf("expected recent lastActivity from keepAlive (age %v), keepAlive may not be running", age)
	}

	// Wait for the response to confirm side_query completed.
	msg, err := extCodec.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	resp, ok := msg.(*Response)
	if !ok {
		t.Fatalf("expected Response, got %T", msg)
	}
	if resp.Error != nil {
		t.Fatalf("side_query returned error: %v", resp.Error)
	}
}

func TestBridge_KeepAlive_UpdatesLastActivity(t *testing.T) {
	old := keepAliveInterval
	keepAliveInterval = 10 * time.Millisecond
	defer func() { keepAliveInterval = old }()

	b, _ := pipePair(&InitResult{})
	b.lastActivity.Store(0) // clear

	stop := b.keepAlive()
	time.Sleep(50 * time.Millisecond) // wait for several ticks
	stop()

	if b.lastActivity.Load() == 0 {
		t.Fatal("expected keepAlive to update lastActivity")
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

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}

func TestBridge_SendMessage_DeliverAs(t *testing.T) {
	b, extCodec := pipePair(&InitResult{})
	api := newMockAPI()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, api) }()

	params := json.RawMessage(`{"custom_type":"nudge","content":"hello","display":false,"deliver_as":"steer","trigger_turn":true}`)
	_ = extCodec.WriteRequest(1, "send_message", &params)

	msg, err := extCodec.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	resp := msg.(*Response)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	waitFor(t, func() bool {
		api.mu.Lock()
		defer api.mu.Unlock()
		return len(api.sentMessages) > 0
	}, "send_message not called")
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.sentMessages[0].CustomType != "nudge" {
		t.Fatalf("got custom_type %q, want nudge", api.sentMessages[0].CustomType)
	}
	if api.sentMsgOpts[0] == nil {
		t.Fatal("expected non-nil SendMessageOptions")
	}
	if api.sentMsgOpts[0].DeliverAs != "steer" {
		t.Fatalf("got deliver_as %q, want steer", api.sentMsgOpts[0].DeliverAs)
	}
	if !api.sentMsgOpts[0].TriggerTurn {
		t.Fatal("expected trigger_turn=true")
	}
}

func TestBridge_SendMessage_DefaultOpts(t *testing.T) {
	b, extCodec := pipePair(&InitResult{})
	api := newMockAPI()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, api) }()

	// No deliver_as or trigger_turn → opts should be empty (not nil)
	params := json.RawMessage(`{"custom_type":"info","content":"hello"}`)
	_ = extCodec.WriteRequest(2, "send_message", &params)

	_, _ = extCodec.ReadMessage() // response

	waitFor(t, func() bool {
		api.mu.Lock()
		defer api.mu.Unlock()
		return len(api.sentMessages) > 0
	}, "send_message not called")
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.sentMsgOpts[0] == nil {
		t.Fatal("expected non-nil SendMessageOptions even with defaults")
	}
	if api.sentMsgOpts[0].DeliverAs != "" {
		t.Fatalf("expected empty deliver_as, got %q", api.sentMsgOpts[0].DeliverAs)
	}
	if api.sentMsgOpts[0].TriggerTurn {
		t.Fatal("expected trigger_turn=false")
	}
}

func TestBridge_SendUserMessage_DeliverAs(t *testing.T) {
	b, extCodec := pipePair(&InitResult{})
	api := newMockAPI()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, api) }()

	params := json.RawMessage(`{"content":"steer me","deliver_as":"steer"}`)
	_ = extCodec.WriteRequest(3, "send_user_message", &params)

	msg, err := extCodec.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	resp := msg.(*Response)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	waitFor(t, func() bool {
		api.mu.Lock()
		defer api.mu.Unlock()
		return len(api.userMessages) > 0
	}, "send_user_message not called")
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.userMessages[0] != "steer me" {
		t.Fatalf("got content %q, want steer me", api.userMessages[0])
	}
	if api.userMsgOpts[0] == nil {
		t.Fatal("expected non-nil SendUserMessageOptions")
	}
	if api.userMsgOpts[0].DeliverAs != "steer" {
		t.Fatalf("got deliver_as %q, want steer", api.userMsgOpts[0].DeliverAs)
	}
}

// ---------------------------------------------------------------------------
// Session data RPC tests
// ---------------------------------------------------------------------------

func TestBridge_SetSessionData_RPC(t *testing.T) {
	b, extCodec := pipePair(&InitResult{})
	api := newMockAPI()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, api) }()

	params := json.RawMessage(`{"key":"foo","value":"bar"}`)
	_ = extCodec.WriteRequest(1, "set_session_data", &params)

	msg, err := extCodec.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	resp, ok := msg.(*Response)
	if !ok {
		t.Fatalf("expected Response, got %T", msg)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected RPC error: %v", resp.Error)
	}

	// The mock API should have recorded the value.
	waitFor(t, func() bool {
		api.mu.Lock()
		defer api.mu.Unlock()
		_, ok := api.sessionData["foo"]
		return ok
	}, "set_session_data not forwarded to API")

	api.mu.Lock()
	got := api.sessionData["foo"]
	api.mu.Unlock()
	if got != "bar" {
		t.Fatalf("got %q, want %q", got, "bar")
	}
}

func TestBridge_GetSessionData_RPC(t *testing.T) {
	b, extCodec := pipePair(&InitResult{})
	api := newMockAPI()

	// Pre-populate so get_session_data can return a value.
	api.SetSessionData("key1", "value1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, api) }()

	params := json.RawMessage(`{"key":"key1"}`)
	_ = extCodec.WriteRequest(2, "get_session_data", &params)

	msg, err := extCodec.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	resp, ok := msg.(*Response)
	if !ok {
		t.Fatalf("expected Response, got %T", msg)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected RPC error: %v", resp.Error)
	}

	var result map[string]any
	if err := json.Unmarshal(*resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result["value"] != "value1" {
		t.Errorf("got value %v, want value1", result["value"])
	}
	if result["ok"] != true {
		t.Errorf("got ok=%v, want true", result["ok"])
	}
}

func TestBridge_GetSessionData_RPC_Missing(t *testing.T) {
	b, extCodec := pipePair(&InitResult{})
	api := newMockAPI()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, api) }()

	params := json.RawMessage(`{"key":"nonexistent"}`)
	_ = extCodec.WriteRequest(3, "get_session_data", &params)

	msg, err := extCodec.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	resp := msg.(*Response)
	if resp.Error != nil {
		t.Fatalf("unexpected RPC error: %v", resp.Error)
	}

	var result map[string]any
	if err := json.Unmarshal(*resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result["ok"] != false {
		t.Errorf("got ok=%v, want false for missing key", result["ok"])
	}
}

// TestBridge_SessionDataStore exercises the Bridge's own key/value store
// methods (used by bridgeScopedAPI to route set/get_session_data).
func TestBridge_SessionDataStore(t *testing.T) {
	b, _ := pipePair(&InitResult{})

	// Empty store returns ("", false).
	v, ok := b.GetSessionData("x")
	if ok || v != "" {
		t.Fatalf("expected missing, got (%q, %v)", v, ok)
	}

	b.SetSessionData("alpha", "one")
	b.SetSessionData("beta", "two")

	if v, ok := b.GetSessionData("alpha"); !ok || v != "one" {
		t.Fatalf("alpha: got (%q, %v)", v, ok)
	}
	if v, ok := b.GetSessionData("beta"); !ok || v != "two" {
		t.Fatalf("beta: got (%q, %v)", v, ok)
	}

	all := b.GetAllSessionData()
	if len(all) != 2 || all["alpha"] != "one" || all["beta"] != "two" {
		t.Fatalf("GetAllSessionData: %v", all)
	}

	// SeedSessionData merges into existing data.
	b.SeedSessionData(map[string]string{"gamma": "three", "alpha": "overwritten"})
	if v, _ := b.GetSessionData("alpha"); v != "overwritten" {
		t.Fatalf("seed overwrite: got %q", v)
	}
	if v, _ := b.GetSessionData("gamma"); v != "three" {
		t.Fatalf("seed new key: got %q", v)
	}

	// GetAllSessionData snapshot is independent (no shared reference).
	snap := b.GetAllSessionData()
	snap["delta"] = "four"
	if _, ok := b.GetSessionData("delta"); ok {
		t.Fatal("snapshot modification leaked into store")
	}
}
