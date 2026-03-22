package mcp

import (
	"context"
	"encoding/json"

	firlog "github.com/kfet/fir/pkg/log"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// channelNotificationMethod is the JSON-RPC method for channel messages
	// sent by MCP servers that advertise the "claude/channel" experimental capability.
	channelNotificationMethod = "notifications/claude/channel"
)

// ChannelMessage represents an inbound message from a channel-capable MCP server.
type ChannelMessage struct {
	// ServerName is the Manager key for the server that sent the message.
	ServerName string

	// Content is the text content of the inbound message.
	Content string `json:"content"`

	// Meta carries structured metadata from the channel server
	// (chat_id, message_id, user, ts, file_path, etc.)
	Meta map[string]any `json:"meta,omitempty"`
}

// Text returns the message text.
func (cm *ChannelMessage) Text() string {
	return cm.Content
}

// SourceName returns the sender identity from meta.user, or "unknown".
func (cm *ChannelMessage) SourceName() string {
	if cm.Meta != nil {
		if u, ok := cm.Meta["user"].(string); ok && u != "" {
			return u
		}
	}
	return "unknown"
}

// channelTransport wraps an sdk.Transport to intercept channel notifications
// before they reach the SDK's method dispatcher (which would reject them as
// unknown methods).
type channelTransport struct {
	inner      sdk.Transport
	serverName string
	onMessage  func(ChannelMessage)
}

// Connect delegates to the inner transport, then wraps the returned connection.
func (t *channelTransport) Connect(ctx context.Context) (sdk.Connection, error) {
	conn, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &channelConnection{
		inner:      conn,
		serverName: t.serverName,
		onMessage:  t.onMessage,
	}, nil
}

// channelConnection wraps an sdk.Connection, intercepting Read() to capture
// channel notifications and suppress them from the SDK.
type channelConnection struct {
	inner      sdk.Connection
	serverName string
	onMessage  func(ChannelMessage)
}

func (c *channelConnection) Write(ctx context.Context, msg jsonrpc.Message) error {
	return c.inner.Write(ctx, msg)
}

func (c *channelConnection) Close() error {
	return c.inner.Close()
}

func (c *channelConnection) SessionID() string {
	return c.inner.SessionID()
}

// Read reads the next message, intercepting channel notifications.
// When a channel notification is received, the callback is fired and Read
// loops to fetch the next message (the SDK never sees the notification).
func (c *channelConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	for {
		msg, err := c.inner.Read(ctx)
		if err != nil {
			return nil, err
		}
		req, ok := msg.(*jsonrpc.Request)
		if !ok || req.IsCall() || req.Method != channelNotificationMethod {
			return msg, nil
		}
		// Parse the channel notification params.
		var cm ChannelMessage
		if err := json.Unmarshal(req.Params, &cm); err != nil {
			firlog.Warn("mcp channel: failed to parse notification params",
				"server", c.serverName, "err", err)
			continue
		}
		cm.ServerName = c.serverName
		firlog.Info("mcp channel message received",
			"server", c.serverName, "source", cm.SourceName())
		if c.onMessage != nil {
			c.onMessage(cm)
		}
	}
}

// wrapTransportForChannels wraps a transport to intercept channel notifications.
// The onMessage callback is invoked from the Read goroutine for each
// notifications/claude/channel notification received.
func wrapTransportForChannels(inner sdk.Transport, serverName string, onMessage func(ChannelMessage)) sdk.Transport {
	if onMessage == nil {
		return inner
	}
	return &channelTransport{
		inner:      inner,
		serverName: serverName,
		onMessage:  onMessage,
	}
}

// Ensure channelTransport implements sdk.Transport.
var _ sdk.Transport = (*channelTransport)(nil)

// Ensure channelConnection implements sdk.Connection.
var _ sdk.Connection = (*channelConnection)(nil)
