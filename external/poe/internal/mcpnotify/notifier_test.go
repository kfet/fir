package mcpnotify

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// recordingConn implements mcp.Connection, capturing every Write in a slice
// for assertion. Read and Close block / no-op respectively.
type recordingConn struct {
	mu     sync.Mutex
	writes []jsonrpc.Message
	closed bool
	readCh chan struct{} // blocks forever
}

func newRecordingConn() *recordingConn {
	return &recordingConn{readCh: make(chan struct{})}
}

func (c *recordingConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.readCh:
		return nil, errors.New("closed")
	}
}

func (c *recordingConn) Write(_ context.Context, msg jsonrpc.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("closed")
	}
	c.writes = append(c.writes, msg)
	return nil
}

func (c *recordingConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		close(c.readCh)
	}
	return nil
}

func (c *recordingConn) SessionID() string { return "test-session" }

func (c *recordingConn) got() []jsonrpc.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]jsonrpc.Message, len(c.writes))
	copy(out, c.writes)
	return out
}

// fakeTransport returns a pre-built Connection on Connect.
type fakeTransport struct{ conn mcp.Connection }

func (t *fakeTransport) Connect(context.Context) (mcp.Connection, error) {
	return t.conn, nil
}

// --- tests ---

func TestNotifier_NotConnected(t *testing.T) {
	n := NewNotifier()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancelled — SendChannel should return ctx.Err()
	err := n.SendChannel(ctx, ChannelMessage{Content: "hi"})
	if err != context.Canceled {
		t.Errorf("err: got %v, want context.Canceled", err)
	}
}

func TestNotifier_WrapCapturesConn(t *testing.T) {
	n := NewNotifier()
	rc := newRecordingConn()
	defer rc.Close()

	wrapped := n.Wrap(&fakeTransport{conn: rc})
	c, err := wrapped.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if c != rc {
		t.Errorf("Connect: got %v, want recording conn", c)
	}

	// After Connect, SendChannel should write through to rc.
	if err := n.SendChannel(context.Background(), ChannelMessage{
		Content: "hello from poe",
		Meta: map[string]any{
			"user":            "u-x",
			"conversation_id": "c-y",
			"message_id":      "m-z",
		},
	}); err != nil {
		t.Fatalf("SendChannel: %v", err)
	}

	writes := rc.got()
	if len(writes) != 1 {
		t.Fatalf("writes: got %d, want 1", len(writes))
	}
	req, ok := writes[0].(*jsonrpc.Request)
	if !ok {
		t.Fatalf("write type: got %T, want *jsonrpc.Request", writes[0])
	}
	if req.Method != ChannelMethod {
		t.Errorf("method: got %q, want %q", req.Method, ChannelMethod)
	}
	// A notification has no ID.
	if req.ID.IsValid() {
		t.Errorf("ID: notification must have no id, got %v", req.ID)
	}
	var payload ChannelMessage
	if err := json.Unmarshal(req.Params, &payload); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if payload.Content != "hello from poe" {
		t.Errorf("content: got %q", payload.Content)
	}
	if payload.Meta["message_id"] != "m-z" {
		t.Errorf("meta.message_id: got %v", payload.Meta["message_id"])
	}
}

func TestNotifier_WrapConnectError(t *testing.T) {
	n := NewNotifier()
	wrapped := n.Wrap(&errorTransport{})
	if _, err := wrapped.Connect(context.Background()); err == nil {
		t.Fatal("expected Connect error")
	}
	// Still not connected → SendChannel must return ctx error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := n.SendChannel(ctx, ChannelMessage{}); err != context.Canceled {
		t.Errorf("err: got %v, want context.Canceled", err)
	}
}

type errorTransport struct{}

func (errorTransport) Connect(context.Context) (mcp.Connection, error) {
	return nil, errors.New("boom")
}

func TestNotifier_ConcurrentSend(t *testing.T) {
	n := NewNotifier()
	rc := newRecordingConn()
	defer rc.Close()
	_, err := n.Wrap(&fakeTransport{conn: rc}).Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_ = n.SendChannel(context.Background(), ChannelMessage{Content: "x"})
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent sends did not complete within 3s")
	}

	if got := len(rc.got()); got != N {
		t.Errorf("writes: got %d, want %d", got, N)
	}
}
