package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// channelMCPServer is a fake MCP server that advertises claude/channel
// capability and sends channel notifications.
type channelMCPServer struct {
	conn      *fakeServerConnection
	transport *fakeServerTransport
}

type fakeServerConnection struct {
	incoming chan jsonrpc.Message // messages TO the client (from server)
	outgoing chan jsonrpc.Message // messages FROM the client (to server)
	closed   bool
	mu       sync.Mutex
}

func newFakeServerConnection() *fakeServerConnection {
	return &fakeServerConnection{
		incoming: make(chan jsonrpc.Message, 100),
		outgoing: make(chan jsonrpc.Message, 100),
	}
}

func (c *fakeServerConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	select {
	case msg, ok := <-c.incoming:
		if !ok {
			return nil, fmt.Errorf("connection closed")
		}
		return msg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *fakeServerConnection) Write(_ context.Context, msg jsonrpc.Message) error {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return fmt.Errorf("connection closed")
	}
	c.outgoing <- msg
	return nil
}

func (c *fakeServerConnection) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		close(c.incoming)
	}
	return nil
}

func (c *fakeServerConnection) SessionID() string { return "test-channel-server" }

type fakeServerTransport struct {
	conn *fakeServerConnection
}

func (t *fakeServerTransport) Connect(context.Context) (sdk.Connection, error) {
	return t.conn, nil
}

func newChannelMCPServer() *channelMCPServer {
	conn := newFakeServerConnection()
	return &channelMCPServer{
		conn:      conn,
		transport: &fakeServerTransport{conn: conn},
	}
}

// handleRequest responds to a single request.
func (s *channelMCPServer) handleRequest(msg jsonrpc.Message) {
	req, ok := msg.(*jsonrpc.Request)
	if !ok || !req.IsCall() {
		return
	}
	switch req.Method {
	case "initialize":
		resp := &jsonrpc.Response{
			ID: req.ID,
			Result: mustMarshal(sdk.InitializeResult{
				ProtocolVersion: "2025-03-26",
				Capabilities: &sdk.ServerCapabilities{
					Tools:        &sdk.ToolCapabilities{},
					Experimental: map[string]any{"claude/channel": map[string]any{}},
				},
				ServerInfo: &sdk.Implementation{Name: "test-channel", Version: "0.1"},
			}),
		}
		s.conn.incoming <- resp
	case "tools/list":
		resp := &jsonrpc.Response{
			ID:     req.ID,
			Result: mustMarshal(sdk.ListToolsResult{}),
		}
		s.conn.incoming <- resp
	case "logging/setLevel":
		resp := &jsonrpc.Response{
			ID:     req.ID,
			Result: mustMarshal(struct{}{}),
		}
		s.conn.incoming <- resp
	case "resources/list":
		resp := &jsonrpc.Response{
			ID:     req.ID,
			Result: mustMarshal(map[string]any{"resources": []any{}}),
		}
		s.conn.incoming <- resp
	case "ping":
		resp := &jsonrpc.Response{
			ID:     req.ID,
			Result: mustMarshal(struct{}{}),
		}
		s.conn.incoming <- resp
	default:
		// Return empty result for unknown methods
		resp := &jsonrpc.Response{
			ID:     req.ID,
			Result: mustMarshal(struct{}{}),
		}
		s.conn.incoming <- resp
	}
}

// sendChannelNotification sends a notifications/claude/channel message.
func (s *channelMCPServer) sendChannelNotification(user, content string) {
	params := mustMarshal(map[string]any{
		"content": content,
		"meta":    map[string]any{"user": user},
	})
	notif := &jsonrpc.Request{
		Method: channelNotificationMethod,
		Params: params,
	}
	s.conn.incoming <- notif
}

func mustMarshal(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

// TestManagerChannelNotificationE2E tests that a Manager with a channel-capable
// server correctly intercepts notifications and fires OnChannelMessage.
func TestManagerChannelNotificationE2E(t *testing.T) {
	server := newChannelMCPServer()

	mgr := NewManager(map[string]ServerConfig{
		"test-channel": {Command: "unused"},
	}, false)
	mgr.dialFn = func(_ string, cfg ServerConfig) (sdk.Transport, error) {
		return server.transport, nil
	}

	var received []ChannelMessage
	var mu sync.Mutex
	done := make(chan struct{}, 1)
	mgr.SetOnChannelMessage(func(cm ChannelMessage) {
		t.Logf("OnChannelMessage: source=%s content=%s", cm.SourceName(), cm.Content)
		mu.Lock()
		received = append(received, cm)
		count := len(received)
		mu.Unlock()
		if count >= 2 {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})

	// Start request handler in background. Use a context to stop it.
	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()
	go func() {
		for {
			select {
			case <-serverCtx.Done():
				return
			case msg := <-server.conn.outgoing:
				server.handleRequest(msg)
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tools := startAndWait(t, mgr, ctx)
	t.Logf("Tools: %d", len(tools))

	// Send channel notifications — the SDK's internal read loop starts
	// during startAndWait, so notifications can be sent immediately.
	t.Log("Sending channel notifications...")
	server.sendChannelNotification("telegram", "Hello from Telegram!")
	server.sendChannelNotification("discord", "Hello from Discord!")
	t.Log("Notifications sent")

	select {
	case <-done:
		t.Log("Both notifications received!")
	case <-ctx.Done():
		mu.Lock()
		n := len(received)
		mu.Unlock()
		t.Fatalf("timed out waiting for channel notifications (received %d)", n)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) < 2 {
		t.Fatalf("expected 2 channel messages, got %d", len(received))
	}
	// Order is non-deterministic (async dispatch), so check both are present.
	sources := map[string]string{}
	for _, cm := range received {
		sources[cm.SourceName()] = cm.Content
		if cm.ServerName != "test-channel" {
			t.Errorf("server = %q, want test-channel", cm.ServerName)
		}
	}
	if sources["telegram"] != "Hello from Telegram!" {
		t.Errorf("telegram message = %q", sources["telegram"])
	}
	if sources["discord"] != "Hello from Discord!" {
		t.Errorf("discord message = %q", sources["discord"])
	}

	_ = mgr.Close()
}
