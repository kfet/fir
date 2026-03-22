package mcp

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeConnection is a minimal Connection for testing.
type fakeConnection struct {
	msgs   []jsonrpc.Message
	idx    int
	mu     sync.Mutex
	closed bool
}

func (c *fakeConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	c.mu.Lock()
	if c.idx >= len(c.msgs) {
		c.mu.Unlock()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	msg := c.msgs[c.idx]
	c.idx++
	c.mu.Unlock()
	return msg, nil
}

func (c *fakeConnection) Write(_ context.Context, _ jsonrpc.Message) error { return nil }
func (c *fakeConnection) Close() error                                     { c.closed = true; return nil }
func (c *fakeConnection) SessionID() string                                { return "test" }

// fakeTransport wraps a fakeConnection.
type fakeTransport struct {
	conn *fakeConnection
}

func (t *fakeTransport) Connect(context.Context) (sdk.Connection, error) {
	return t.conn, nil
}

func makeNotification(method string, params any) *jsonrpc.Request {
	p, _ := json.Marshal(params)
	// A notification has no ID — use jsonrpc.NewNotification equivalent.
	return &jsonrpc.Request{
		Method: method,
		Params: p,
	}
}

func TestChannelConnectionInterceptsChannelNotifications(t *testing.T) {
	channelParams := map[string]any{
		"source":  "telegram",
		"message": "hello from telegram",
		"metadata": map[string]any{
			"chat_id": "123",
		},
	}
	// A normal notification that should pass through.
	normalNotif := makeNotification("notifications/tools/list_changed", nil)
	channelNotif := makeNotification(channelNotificationMethod, channelParams)

	conn := &fakeConnection{
		msgs: []jsonrpc.Message{channelNotif, normalNotif},
	}

	var received []ChannelMessage
	var mu sync.Mutex
	wrapped := &channelConnection{
		inner:      conn,
		serverName: "test-server",
		onMessage: func(cm ChannelMessage) {
			mu.Lock()
			received = append(received, cm)
			mu.Unlock()
		},
	}

	ctx := context.Background()

	// First Read should skip the channel notification and return the normal one.
	msg, err := wrapped.Read(ctx)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	req, ok := msg.(*jsonrpc.Request)
	if !ok {
		t.Fatalf("expected *jsonrpc.Request, got %T", msg)
	}
	if req.Method != "notifications/tools/list_changed" {
		t.Fatalf("expected tools/list_changed, got %s", req.Method)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("expected 1 channel message, got %d", len(received))
	}
	if received[0].ServerName != "test-server" {
		t.Errorf("ServerName = %q, want %q", received[0].ServerName, "test-server")
	}
	if received[0].Source != "telegram" {
		t.Errorf("Source = %q, want %q", received[0].Source, "telegram")
	}
	if received[0].Message != "hello from telegram" {
		t.Errorf("Message = %q, want %q", received[0].Message, "hello from telegram")
	}
}

func TestChannelTransportWrapsConnection(t *testing.T) {
	channelParams := map[string]any{
		"source":  "discord",
		"message": "hello from discord",
	}
	normalNotif := makeNotification("notifications/tools/list_changed", nil)
	conn := &fakeConnection{
		msgs: []jsonrpc.Message{
			makeNotification(channelNotificationMethod, channelParams),
			normalNotif,
		},
	}
	inner := &fakeTransport{conn: conn}

	var got ChannelMessage
	wrapped := wrapTransportForChannels(inner, "discord-server", func(cm ChannelMessage) {
		got = cm
	})

	ctx := context.Background()

	sdkConn, err := wrapped.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Read should intercept the channel notification and return the normal one.
	msg, err := sdkConn.Read(ctx)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	req := msg.(*jsonrpc.Request)
	if req.Method != "notifications/tools/list_changed" {
		t.Fatalf("expected tools/list_changed, got %s", req.Method)
	}

	if got.Source != "discord" {
		t.Errorf("Source = %q, want %q", got.Source, "discord")
	}
	if got.ServerName != "discord-server" {
		t.Errorf("ServerName = %q, want %q", got.ServerName, "discord-server")
	}
}

func TestWrapTransportNilCallbackReturnsInner(t *testing.T) {
	inner := &fakeTransport{}
	wrapped := wrapTransportForChannels(inner, "test", nil)
	if wrapped != inner {
		t.Error("expected nil callback to return inner transport unchanged")
	}
}

func TestChannelConnectionMalformedParams(t *testing.T) {
	// Malformed channel notification should be skipped, not crash.
	badNotif := &jsonrpc.Request{
		Method: channelNotificationMethod,
		Params: json.RawMessage(`{invalid`),
	}
	normalNotif := makeNotification("notifications/tools/list_changed", nil)

	conn := &fakeConnection{
		msgs: []jsonrpc.Message{badNotif, normalNotif},
	}

	wrapped := &channelConnection{
		inner:      conn,
		serverName: "test",
		onMessage:  func(cm ChannelMessage) {},
	}

	msg, err := wrapped.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	req := msg.(*jsonrpc.Request)
	if req.Method != "notifications/tools/list_changed" {
		t.Fatalf("expected normal notification to pass through after malformed one")
	}
}
