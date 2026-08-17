package firext

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// harness wires an App to in-memory pipes and drives it like fir would:
// it writes requests to the App's stdin and reads responses from its stdout.
type harness struct {
	t       *testing.T
	app     *App
	toApp   *io.PipeWriter // we write requests here
	fromApp *bufio.Reader  // we read responses here
	mu      sync.Mutex
	nextID  int64
	done    chan struct{}
}

func newHarness(t *testing.T, build func(a *App)) *harness {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	app := New("test-ext")
	app.in = inR
	app.out = outW
	build(app)

	h := &harness{t: t, app: app, toApp: inW, fromApp: bufio.NewReader(outR), done: make(chan struct{})}
	go func() {
		_ = app.Run()
		close(h.done)
	}()
	return h
}

func (h *harness) send(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		h.t.Fatalf("marshal: %v", err)
	}
	if _, err := h.toApp.Write(append(data, '\n')); err != nil {
		h.t.Fatalf("write: %v", err)
	}
}

// readMsg reads one JSON line emitted by the App.
func (h *harness) readMsg() rpcMessage {
	line, err := h.fromApp.ReadBytes('\n')
	if err != nil {
		h.t.Fatalf("read: %v", err)
	}
	var m rpcMessage
	if err := json.Unmarshal(line, &m); err != nil {
		h.t.Fatalf("unmarshal %q: %v", line, err)
	}
	return m
}

func (h *harness) close() {
	_ = h.toApp.Close()
	select {
	case <-h.done:
	case <-time.After(2 * time.Second):
		h.t.Fatal("App.Run did not exit")
	}
}

func i64(v int64) *int64 { return &v }

// --- tests ---

func TestInitHandshake(t *testing.T) {
	h := newHarness(t, func(a *App) {
		a.Tool(ToolSpec{
			Name:        "greet",
			Description: "Greet",
			Parameters:  Object(Props{"name": Str("who")}, "name"),
		}, func(p json.RawMessage, ctx *Context) (*ToolResult, error) {
			return Text("hi"), nil
		})
		a.Command("hello", "say hi", func(args []string, ctx *Context) (*CommandResult, error) {
			return &CommandResult{Message: "hi"}, nil
		})
		a.On("session_start", func(p json.RawMessage, ctx *Context) {})
		a.Hook("tool_call", func(p json.RawMessage, ctx *Context) (*HookDecision, error) {
			return nil, nil
		})
	})
	defer h.close()

	h.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "init", "params": map[string]any{"version": "1"}})
	resp := h.readMsg()
	if resp.ID == nil || *resp.ID != 1 {
		t.Fatalf("init id mismatch: %+v", resp)
	}
	var res struct {
		Name     string        `json:"name"`
		Tools    []ToolSpec    `json:"tools"`
		Commands []CommandSpec `json:"commands"`
		Events   []string      `json:"events"`
	}
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("init result: %v", err)
	}
	if res.Name != "test-ext" {
		t.Errorf("name = %q", res.Name)
	}
	if len(res.Tools) != 1 || res.Tools[0].Name != "greet" {
		t.Errorf("tools = %+v", res.Tools)
	}
	if len(res.Commands) != 1 || res.Commands[0].Name != "hello" {
		t.Errorf("commands = %+v", res.Commands)
	}
	// events should contain session_start and hook/tool_call (order is map-driven).
	joined := strings.Join(res.Events, ",")
	if !strings.Contains(joined, "session_start") || !strings.Contains(joined, "hook/tool_call") {
		t.Errorf("events = %v", res.Events)
	}
}

func TestToolCall(t *testing.T) {
	h := newHarness(t, func(a *App) {
		a.Tool(ToolSpec{Name: "echo"}, func(p json.RawMessage, ctx *Context) (*ToolResult, error) {
			var in struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(p, &in)
			return Text("echo: " + in.Text), nil
		})
	})
	defer h.close()

	h.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "init"})
	h.readMsg() // init result

	h.send(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tool_call",
		"params": map[string]any{"tool_call_id": "x", "name": "echo", "params": map[string]any{"text": "hi"}},
	})
	resp := h.readMsg()
	if *resp.ID != 2 {
		t.Fatalf("id = %v", resp.ID)
	}
	var tr ToolResult
	if err := json.Unmarshal(resp.Result, &tr); err != nil {
		t.Fatalf("result: %v", err)
	}
	if len(tr.Content) != 1 || tr.Content[0].Text != "echo: hi" {
		t.Errorf("content = %+v", tr.Content)
	}
}

func TestToolCallError(t *testing.T) {
	h := newHarness(t, func(a *App) {
		a.Tool(ToolSpec{Name: "boom"}, func(p json.RawMessage, ctx *Context) (*ToolResult, error) {
			return nil, &testErr{"kaboom"}
		})
	})
	defer h.close()
	h.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "init"})
	h.readMsg()
	h.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tool_call",
		"params": map[string]any{"name": "boom", "params": map[string]any{}}})
	resp := h.readMsg()
	if resp.Error == nil || resp.Error.Message != "kaboom" {
		t.Fatalf("expected error, got %+v", resp)
	}
}

func TestUnknownTool(t *testing.T) {
	h := newHarness(t, func(a *App) {})
	defer h.close()
	h.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "init"})
	h.readMsg()
	h.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tool_call",
		"params": map[string]any{"name": "nope", "params": map[string]any{}}})
	resp := h.readMsg()
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Fatalf("expected method-not-found, got %+v", resp)
	}
}

func TestHookBlock(t *testing.T) {
	h := newHarness(t, func(a *App) {
		a.Hook("tool_call", func(p json.RawMessage, ctx *Context) (*HookDecision, error) {
			var in struct {
				ToolName string `json:"tool_name"`
			}
			_ = json.Unmarshal(p, &in)
			if in.ToolName == "bash" {
				return &HookDecision{Block: true, Reason: "no bash"}, nil
			}
			return nil, nil
		})
	})
	defer h.close()
	h.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "init"})
	h.readMsg()

	// blocked
	h.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "hook/tool_call",
		"params": map[string]any{"tool_name": "bash", "params": map[string]any{}}})
	resp := h.readMsg()
	var dec HookDecision
	_ = json.Unmarshal(resp.Result, &dec)
	if !dec.Block || dec.Reason != "no bash" {
		t.Errorf("expected block, got %+v (raw %s)", dec, resp.Result)
	}

	// allowed (result null)
	h.send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "hook/tool_call",
		"params": map[string]any{"tool_name": "read", "params": map[string]any{}}})
	resp = h.readMsg()
	if string(resp.Result) != "null" {
		t.Errorf("expected null result, got %s", resp.Result)
	}
}

func TestCommandHook(t *testing.T) {
	h := newHarness(t, func(a *App) {
		a.Command("greet", "greet", func(args []string, ctx *Context) (*CommandResult, error) {
			return &CommandResult{Message: "args=" + strings.Join(args, "+"), PrintResponse: true}, nil
		})
	})
	defer h.close()
	h.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "init"})
	h.readMsg()
	h.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "hook/command",
		"params": map[string]any{"name": "greet", "args": []string{"a", "b"}}})
	resp := h.readMsg()
	var cr CommandResult
	_ = json.Unmarshal(resp.Result, &cr)
	if cr.Message != "args=a+b" || !cr.PrintResponse {
		t.Errorf("cmd result = %+v", cr)
	}
}

// TestOutboundCallDuringHandler verifies a handler can call back into fir
// (ctx.Notify) while the run loop continues to receive the response — i.e. no
// deadlock from the concurrent-dispatch design.
func TestOutboundCallDuringHandler(t *testing.T) {
	var gotNotify string
	var wg sync.WaitGroup
	wg.Add(1)

	h := newHarness(t, func(a *App) {
		a.Tool(ToolSpec{Name: "ping"}, func(p json.RawMessage, ctx *Context) (*ToolResult, error) {
			// During the tool call, make an outbound notify call.
			if err := ctx.Notify("hello from handler", "info"); err != nil {
				t.Errorf("notify: %v", err)
			}
			return Text("pong"), nil
		})
	})
	defer h.close()

	h.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "init"})
	h.readMsg()

	// Reader goroutine: the App will emit an outbound `notify` request; we must
	// answer it, then read the tool_call response.
	go func() {
		defer wg.Done()
		for {
			msg := h.readMsg()
			if msg.Method == "notify" {
				// Capture and respond {"ok":true}.
				var np struct {
					Message string `json:"message"`
				}
				_ = json.Unmarshal(msg.Params, &np)
				gotNotify = np.Message
				h.send(map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": map[string]any{"ok": true}})
				continue
			}
			if msg.Method == "" && msg.Result != nil {
				// tool_call response
				var tr ToolResult
				_ = json.Unmarshal(msg.Result, &tr)
				if len(tr.Content) == 0 || tr.Content[0].Text != "pong" {
					t.Errorf("tool result = %+v", tr)
				}
				return
			}
		}
	}()

	h.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tool_call",
		"params": map[string]any{"name": "ping", "params": map[string]any{}}})

	wg.Wait()
	if gotNotify != "hello from handler" {
		t.Errorf("notify message = %q", gotNotify)
	}
}

func TestEventNoResponse(t *testing.T) {
	got := make(chan string, 1)
	h := newHarness(t, func(a *App) {
		a.On("session_start", func(p json.RawMessage, ctx *Context) {
			var in struct {
				SessionID string `json:"session_id"`
			}
			_ = json.Unmarshal(p, &in)
			got <- in.SessionID
		})
	})
	defer h.close()
	h.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "init"})
	h.readMsg()
	// Notification: no id.
	h.send(map[string]any{"jsonrpc": "2.0", "method": "event/session_start",
		"params": map[string]any{"session_id": "s-123"}})
	select {
	case sid := <-got:
		if sid != "s-123" {
			t.Errorf("session_id = %q", sid)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event handler not invoked")
	}
}

// An event carrying an id is a request: the host is waiting to learn that the
// handler finished (fir does this for shutdown events, then tears the extension
// down as soon as it acks). The reply must come after the handler returns.
func TestEventWithIDIsAcked(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	h := newHarness(t, func(a *App) {
		a.On("session_shutdown", func(p json.RawMessage, ctx *Context) {
			close(entered)
			<-release
		})
	})
	defer h.close()
	h.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "init"})
	h.readMsg()

	h.send(map[string]any{"jsonrpc": "2.0", "id": 7, "method": "event/session_shutdown"})

	// The ack must not arrive before the handler has finished.
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("event handler not invoked")
	}
	close(release)

	msg := h.readMsg()
	if msg.ID == nil || *msg.ID != 7 {
		t.Fatalf("ack id = %v, want 7", msg.ID)
	}
	if msg.Error != nil {
		t.Fatalf("ack carried an error: %v", msg.Error)
	}
}

// An unhandled event still has to be acked, or an awaiting host waits out its
// whole timeout for a handler that was never going to run.
func TestEventWithIDAckedWhenUnhandled(t *testing.T) {
	h := newHarness(t, func(a *App) {})
	defer h.close()
	h.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "init"})
	h.readMsg()

	h.send(map[string]any{"jsonrpc": "2.0", "id": 8, "method": "event/session_shutdown"})
	msg := h.readMsg()
	if msg.ID == nil || *msg.ID != 8 {
		t.Fatalf("ack id = %v, want 8", msg.ID)
	}
}

// testErr is a trivial error type.
type testErr struct{ msg string }

func (e *testErr) Error() string { return e.msg }
