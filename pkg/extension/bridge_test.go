package extension

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
)

// mockBridgeAPI implements BridgeAPI for testing.
type mockBridgeAPI struct {
	mu              sync.Mutex
	execCalled      bool
	execCmd         string
	sessionName     string
	sessionFile     string
	sentMessages    []CustomMessageSpec
	sentMsgOpts     []*SendMessageOptions
	userMessages    []string
	userMsgOpts     []*SendUserMessageOptions
	labels          map[string]string
	modelSet        *ai.Model
	availableModels []*ai.Model
	toolsRegistered []ToolDefinition
	sessionData     map[string]string
	restartPrompts  []string
	restartPrepends []string
	restartErr      error
	reloadNames     []string
	reloadErr       error
	reloadFn        func(name string) error
	reloadMCPFn     func() (ReloadMCPResult, error)
	reloadMCPCalls  int
	reloadMCPResult ReloadMCPResult
	reloadMCPErr    error
	observableStore *store.ObservableStore
	// captures of the most recent SideQuery call
	sideQueryQuestion string
	sideQueryOpts     *session.SideQueryOptions
}

func newMockAPI() *mockBridgeAPI {
	return &mockBridgeAPI{labels: make(map[string]string)}
}

func (m *mockBridgeAPI) toolCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.toolsRegistered)
}

// toolNameSet returns the set of currently-registered tool names.
func (m *mockBridgeAPI) toolNameSet() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	set := make(map[string]bool, len(m.toolsRegistered))
	for _, d := range m.toolsRegistered {
		set[d.Name] = true
	}
	return set
}

// removeExtensionTools removes the named tools from toolsRegistered. It
// satisfies the unexported interface that Manager.ReloadOne type-asserts
// against the BridgeAPI so the manager can drop only one extension's tools.
func (m *mockBridgeAPI) removeExtensionTools(names []string) {
	remove := make(map[string]bool, len(names))
	for _, n := range names {
		remove[n] = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.toolsRegistered[:0]
	for _, d := range m.toolsRegistered {
		if !remove[d.Name] {
			kept = append(kept, d)
		}
	}
	m.toolsRegistered = kept
}

// SetReloadFn captures the manager-wired reload callback so tests can drive
// the closure that delegates into Manager.ReloadOne.
func (m *mockBridgeAPI) SetReloadFn(fn func(name string) error) {
	m.mu.Lock()
	m.reloadFn = fn
	m.mu.Unlock()
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
func (m *mockBridgeAPI) GetSessionFile() string        { return m.sessionFile }
func (m *mockBridgeAPI) GetSessionID() string          { return "" }
func (m *mockBridgeAPI) SetLabel(id, label string)     { m.labels[id] = label }
func (m *mockBridgeAPI) ClearLabel(id string)          { delete(m.labels, id) }
func (m *mockBridgeAPI) SetModel(model *ai.Model) bool { m.modelSet = model; return true }
func (m *mockBridgeAPI) GetAvailableModels() []*ai.Model {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.availableModels
}
func (m *mockBridgeAPI) ContinueSession() error { return nil }
func (m *mockBridgeAPI) SideQuery(question string, opts *session.SideQueryOptions) (string, error) {
	m.mu.Lock()
	m.sideQueryQuestion = question
	m.sideQueryOpts = opts
	m.mu.Unlock()
	return "mock response", nil
}

func (m *mockBridgeAPI) SideQueryStream(question string, opts *session.SideQueryOptions, onDelta func(session.SideQueryDelta)) (session.SideQueryResult, error) {
	m.mu.Lock()
	m.sideQueryQuestion = question
	m.sideQueryOpts = opts
	m.mu.Unlock()
	// Emit a couple of deltas so streaming tests can observe wire shape.
	if onDelta != nil {
		onDelta(session.SideQueryDelta{Type: "thinking", Text: "thinking…"})
		onDelta(session.SideQueryDelta{Type: "text", Text: "mock "})
		onDelta(session.SideQueryDelta{Type: "text", Text: "response"})
		onDelta(session.SideQueryDelta{Type: "usage", TokensOut: 42})
	}
	return session.SideQueryResult{
		Text:         "mock response",
		FinishReason: "stop",
	}, nil
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
func (m *mockBridgeAPI) CallTool(_ context.Context, name string, params map[string]any) (ToolResult, error) {
	return ToolResult{
		Content: []ai.ToolResultContent{{Text: "mock tool result for " + name}},
	}, nil
}
func (m *mockBridgeAPI) PrependContext(_ string) {}
func (m *mockBridgeAPI) ListTools() []ToolInfo   { return nil }
func (m *mockBridgeAPI) ReportProgress(_ string) {}
func (m *mockBridgeAPI) Introspect() session.Introspection {
	return session.Introspection{}
}

// GetObservableStore returns the per-mock cards store. Tests exercising
// observable-card RPC handlers set m.observableStore directly.
func (m *mockBridgeAPI) GetObservableStore() *store.ObservableStore {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.observableStore
}

// restartCalls / restartErr are used by TestBridge_RestartSession.
func (m *mockBridgeAPI) RestartSession(prompt, prependContext string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.restartPrompts = append(m.restartPrompts, prompt)
	m.restartPrepends = append(m.restartPrepends, prependContext)
	return m.restartErr
}

func (m *mockBridgeAPI) ReloadExtension(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reloadNames = append(m.reloadNames, name)
	return m.reloadErr
}

func (m *mockBridgeAPI) SetReloadMCPFn(fn func() (ReloadMCPResult, error)) {
	m.mu.Lock()
	m.reloadMCPFn = fn
	m.mu.Unlock()
}

func (m *mockBridgeAPI) ReloadMCP() (ReloadMCPResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reloadMCPCalls++
	if m.reloadMCPFn != nil {
		return m.reloadMCPFn()
	}
	return m.reloadMCPResult, m.reloadMCPErr
}

// Verify mockBridgeAPI satisfies BridgeAPI at compile time.
var _ BridgeAPI = (*mockBridgeAPI)(nil)

// slowSideQueryAPI wraps mockBridgeAPI with a configurable delay on SideQuery.
type slowSideQueryAPI struct {
	*mockBridgeAPI
	delay time.Duration
}

func (s *slowSideQueryAPI) SideQuery(question string, opts *session.SideQueryOptions) (string, error) {
	time.Sleep(s.delay)
	return s.mockBridgeAPI.SideQuery(question, opts)
}

func (s *slowSideQueryAPI) SideQueryStream(question string, opts *session.SideQueryOptions, onDelta func(session.SideQueryDelta)) (session.SideQueryResult, error) {
	time.Sleep(s.delay)
	return s.mockBridgeAPI.SideQueryStream(question, opts, onDelta)
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

	_, err := b.CallHook(context.Background(), "hook/test", nil, 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestBridge_CallHook_ContextCancellation(t *testing.T) {
	b, extCodec := pipePair(&InitResult{})

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	go func() { _ = b.Run(runCtx, newMockAPI()) }()

	// Drain messages but never respond — the hook will block.
	go func() {
		for {
			_, err := extCodec.ReadMessage()
			if err != nil {
				return
			}
		}
	}()

	// Cancel the hook's context after a short delay.
	hookCtx, hookCancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		hookCancel()
	}()

	start := time.Now()
	_, err := b.CallHook(hookCtx, "hook/test", nil, 5*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected context cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
	if elapsed > 1*time.Second {
		t.Fatalf("cancellation took too long: %v", elapsed)
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

	raw, err := b.CallHook(context.Background(), "hook/test", nil, 100*time.Millisecond)
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

	_, err := b.CallHook(context.Background(), "hook/test", nil, 60*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error after activity stopped")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got: %v", err)
	}
}

func TestBridge_SideQuery_StreamEmitsDeltasAndResponse(t *testing.T) {
	b, extCodec := pipePair(&InitResult{})
	api := newMockAPI()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, api) }()

	// Send a streaming side_query (stream:true). The mock API will emit
	// thinking/text/text/usage deltas and a final stop result.
	params := json.RawMessage(`{"question":"q","stream":true}`)
	if err := extCodec.WriteRequest(42, "side_query", &params); err != nil {
		t.Fatal(err)
	}

	// Expect a sequence of notifications followed by a response on id=42.
	var deltas []*Notification
	var resp *Response
	for {
		msg, err := extCodec.ReadMessage()
		if err != nil {
			t.Fatalf("ReadMessage: %v", err)
		}
		switch m := msg.(type) {
		case *Notification:
			deltas = append(deltas, m)
		case *Response:
			resp = m
		}
		if resp != nil {
			break
		}
		if len(deltas) > 16 {
			t.Fatalf("got %d deltas without a response", len(deltas))
		}
	}

	if resp.Error != nil {
		t.Fatalf("expected ok response, got error: %v", resp.Error)
	}
	if len(deltas) == 0 {
		t.Fatal("expected at least one side_query/delta notification")
	}

	// Every delta must be method=side_query/delta, params.request_id=42,
	// and seq must be monotonically increasing from 0.
	wantSeq := 0
	sawText := false
	sawUsage := false
	for _, n := range deltas {
		if n.Method != "side_query/delta" {
			t.Errorf("delta method = %q, want side_query/delta", n.Method)
			continue
		}
		var p SideQueryDeltaParams
		if err := json.Unmarshal(*n.Params, &p); err != nil {
			t.Fatalf("delta params: %v", err)
		}
		if p.RequestID != 42 {
			t.Errorf("request_id = %d, want 42", p.RequestID)
		}
		if p.Seq != wantSeq {
			t.Errorf("seq = %d, want %d", p.Seq, wantSeq)
		}
		wantSeq++
		switch p.Type {
		case "text":
			sawText = true
		case "usage":
			sawUsage = true
		}
	}
	if !sawText {
		t.Error("expected at least one text delta")
	}
	if !sawUsage {
		t.Error("expected a usage delta")
	}

	// Terminal response carries text + finish_reason in our extended shape.
	var r SideQueryResult
	if err := json.Unmarshal(*resp.Result, &r); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !r.Ok {
		t.Error("result.ok should be true")
	}
	if r.Text != "mock response" {
		t.Errorf("result.text = %q", r.Text)
	}
	if r.FinishReason != "stop" {
		t.Errorf("result.finish_reason = %q", r.FinishReason)
	}
}

func TestBridge_SideQuery_NonStreamMatchesLegacyShape(t *testing.T) {
	b, extCodec := pipePair(&InitResult{})
	api := newMockAPI()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, api) }()

	// stream:false (absent) — must produce zero notifications and a
	// response body whose JSON is the legacy {ok, text} shape only.
	params := json.RawMessage(`{"question":"q"}`)
	if err := extCodec.WriteRequest(9, "side_query", &params); err != nil {
		t.Fatal(err)
	}

	msg, err := extCodec.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	resp, ok := msg.(*Response)
	if !ok || resp.Error != nil {
		t.Fatalf("expected ok Response, got %+v", msg)
	}

	// The raw JSON of the legacy response must not introduce blocks /
	// finish_reason fields when the caller didn't ask for streaming.
	raw := string(*resp.Result)
	if strings.Contains(raw, "blocks") || strings.Contains(raw, "finish_reason") {
		t.Errorf("non-streaming response leaked streaming fields: %s", raw)
	}
	if !strings.Contains(raw, `"text":"mock response"`) {
		t.Errorf("non-streaming response missing legacy text field: %s", raw)
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

	// Poll for a recent keepAlive tick — under race detector / CI load the
	// goroutine can be delayed well past a single tick interval.
	deadline := time.Now().Add(20 * time.Second)
	var age time.Duration
	for time.Now().Before(deadline) {
		activity := time.Unix(0, b.lastActivity.Load())
		age = time.Since(activity)
		if age < 50*time.Millisecond {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if age >= 50*time.Millisecond {
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

func TestBridge_SideQuery_PassesOverridesThrough(t *testing.T) {
	b, extCodec := pipePair(&InitResult{})
	api := newMockAPI()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, api) }()

	// Send side_query with full override params.
	params := json.RawMessage(`{"question":"q","model":"claude-opus-4-x","provider":"anthropic","effort":"high"}`)
	if err := extCodec.WriteRequest(7, "side_query", &params); err != nil {
		t.Fatal(err)
	}

	msg, err := extCodec.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	resp, ok := msg.(*Response)
	if !ok || resp.Error != nil {
		t.Fatalf("expected ok response, got %+v", msg)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	if api.sideQueryQuestion != "q" {
		t.Errorf("question = %q, want %q", api.sideQueryQuestion, "q")
	}
	if api.sideQueryOpts == nil {
		t.Fatal("expected SideQueryOptions to be passed, got nil")
	}
	if api.sideQueryOpts.Model != "claude-opus-4-x" {
		t.Errorf("model = %q", api.sideQueryOpts.Model)
	}
	if api.sideQueryOpts.Provider != "anthropic" {
		t.Errorf("provider = %q", api.sideQueryOpts.Provider)
	}
	if api.sideQueryOpts.Effort != ai.ThinkingHigh {
		t.Errorf("effort = %q, want %q", api.sideQueryOpts.Effort, ai.ThinkingHigh)
	}
}

func TestBridge_SideQuery_NilOptsWhenAllUnset(t *testing.T) {
	b, extCodec := pipePair(&InitResult{})
	api := newMockAPI()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, api) }()

	params := json.RawMessage(`{"question":"q"}`)
	if err := extCodec.WriteRequest(8, "side_query", &params); err != nil {
		t.Fatal(err)
	}
	msg, err := extCodec.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if resp, ok := msg.(*Response); !ok || resp.Error != nil {
		t.Fatalf("expected ok response, got %+v", msg)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	if api.sideQueryOpts != nil {
		t.Errorf("expected nil opts when no overrides set, got %+v", api.sideQueryOpts)
	}
}

func TestBridge_KeepAlive_UpdatesLastActivity(t *testing.T) {
	old := keepAliveInterval
	keepAliveInterval = 10 * time.Millisecond
	defer func() { keepAliveInterval = old }()

	b, _ := pipePair(&InitResult{})
	b.lastActivity.Store(0) // clear

	stop := b.keepAlive()
	defer stop()

	// Poll instead of sleeping — under race detector / CI load the goroutine
	// can take well over 50ms to get its first tick in.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if b.lastActivity.Load() != 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected keepAlive to update lastActivity")
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

	raw, err := b.CallHook(context.Background(), "hook/tool_call", map[string]string{"x": "y"}, 2*time.Second)
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

func TestBridge_InboundAvailableModels(t *testing.T) {
	b, extCodec := pipePair(&InitResult{})
	api := newMockAPI()
	api.availableModels = []*ai.Model{
		{Provider: "anthropic", ID: "claude-opus-4-8", Name: "Claude Opus 4.8"},
		{Provider: "anthropic", ID: "claude-haiku-4-5", Name: "Claude Haiku 4.5"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, api) }()

	_ = extCodec.WriteRequest(1, "available_models", nil)

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
	var got AvailableModelsResult
	if err := json.Unmarshal(*resp.Result, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(got.Models) != 2 {
		t.Fatalf("expected 2 models, got %d: %+v", len(got.Models), got.Models)
	}
	if got.Models[0].Provider != "anthropic" || got.Models[0].ID != "claude-opus-4-8" {
		t.Fatalf("unexpected first model: %+v", got.Models[0])
	}
	if got.Models[1].ID != "claude-haiku-4-5" || got.Models[1].Name != "Claude Haiku 4.5" {
		t.Fatalf("unexpected second model: %+v", got.Models[1])
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
		Context:    context.Background(),
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
	deadline := time.Now().Add(20 * time.Second)
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

func TestBridge_RestartSession_RPC(t *testing.T) {
	b, extCodec := pipePair(&InitResult{})
	api := newMockAPI()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, api) }()

	params := json.RawMessage(`{"prompt":"read /tmp/handoff.md","prepend_context":"briefing-body"}`)
	_ = extCodec.WriteRequest(7, "restart_session", &params)

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
		return len(api.restartPrompts) > 0
	}, "restart_session not delivered to BridgeAPI")
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.restartPrompts[0] != "read /tmp/handoff.md" {
		t.Fatalf("got prompt %q, want %q", api.restartPrompts[0], "read /tmp/handoff.md")
	}
	if len(api.restartPrepends) == 0 || api.restartPrepends[0] != "briefing-body" {
		t.Fatalf("got prepend %v, want [briefing-body]", api.restartPrepends)
	}
}

func TestBridge_RestartSession_PropagatesError(t *testing.T) {
	b, extCodec := pipePair(&InitResult{})
	api := newMockAPI()
	api.mu.Lock()
	api.restartErr = errors.New("not supported in this mode")
	api.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, api) }()

	params := json.RawMessage(`{"prompt":"x"}`)
	_ = extCodec.WriteRequest(8, "restart_session", &params)

	msg, err := extCodec.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	resp := msg.(*Response)
	if resp.Error == nil {
		t.Fatal("expected error response when RestartSession returns error")
	}
	if !strings.Contains(resp.Error.Message, "not supported") {
		t.Fatalf("got error %q, want containing 'not supported'", resp.Error.Message)
	}
}

func TestSessionBridge_RestartSession_NoCallback(t *testing.T) {
	// Without a registered RestartFn, RestartSession must error.
	sb := &SessionBridge{}
	if err := sb.RestartSession("x", ""); err == nil {
		t.Fatal("expected error when no RestartFn is registered")
	}
}

func TestSessionBridge_RestartSession_InvokesCallback(t *testing.T) {
	sb := &SessionBridge{}
	type call struct {
		prompt, prepend string
	}
	got := make(chan call, 1)
	sb.SetRestartFn(func(prompt, prependContext string) error {
		got <- call{prompt, prependContext}
		return nil
	})
	if err := sb.RestartSession("hello", "briefing"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	select {
	case c := <-got:
		if c.prompt != "hello" {
			t.Fatalf("got prompt %q, want hello", c.prompt)
		}
		if c.prepend != "briefing" {
			t.Fatalf("got prepend %q, want briefing", c.prepend)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RestartFn was not invoked")
	}
}

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

	// The bridge should have stored the value directly.
	waitFor(t, func() bool {
		_, ok := b.GetSessionData("foo")
		return ok
	}, "set_session_data not stored on bridge")

	got, _ := b.GetSessionData("foo")
	if got != "bar" {
		t.Fatalf("got %q, want %q", got, "bar")
	}
}

func TestBridge_GetSessionData_RPC(t *testing.T) {
	b, extCodec := pipePair(&InitResult{})
	api := newMockAPI()

	// Pre-populate on the bridge so get_session_data can return a value.
	b.SetSessionData("key1", "value1")

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
// methods (used by handleInbound to route set/get_session_data).
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

func TestBridge_ReportProgress_RPC(t *testing.T) {
	b, extCodec := pipePair(&InitResult{})
	api := newMockAPI()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, api) }()

	params := json.RawMessage(`{"message":"Calling Read..."}`)
	_ = extCodec.WriteRequest(1, "report_progress", &params)

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
}

func TestSessionBridge_TakePendingRestart(t *testing.T) {
	sb := &SessionBridge{}

	// Nothing pending yet.
	if _, _, ok := sb.TakePendingRestart(); ok {
		t.Fatal("expected ok=false with no pending restart")
	}

	// RestartSession records the request synchronously (before any async
	// callback runs), so it is observable immediately afterwards.
	sb.SetRestartFn(func(_, _ string) error { return nil })
	if err := sb.RestartSession("go", "briefing"); err != nil {
		t.Fatalf("RestartSession: %v", err)
	}
	prompt, prepend, ok := sb.TakePendingRestart()
	if !ok || prompt != "go" || prepend != "briefing" {
		t.Fatalf("TakePendingRestart = (%q,%q,%v), want (go,briefing,true)", prompt, prepend, ok)
	}

	// Consumed — a second take returns ok=false.
	if _, _, ok := sb.TakePendingRestart(); ok {
		t.Fatal("expected ok=false after consuming pending restart")
	}
}
