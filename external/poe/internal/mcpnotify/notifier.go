// Package mcpnotify lets the bridge send arbitrary JSON-RPC notifications
// (in particular the `notifications/claude/channel` notification fir uses
// to deliver inbound channel messages) from inside an MCP server process.
//
// The official go-sdk does not currently expose a public API for sending
// custom server→client notifications. We work around this by wrapping the
// sdk.Transport we pass to Server.Run: the wrapper captures the returned
// Connection and hands it to a Notifier, which can then Write raw JSON-RPC
// notifications on the same connection the MCP server is using. Concurrent
// Write is explicitly permitted by the Connection interface contract.
//
// This is a focused, minimal-surface workaround; the day the sdk exposes a
// clean public API for custom notifications, we delete this package and
// replace both uses with one line at each call site.
package mcpnotify

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ChannelMethod is the JSON-RPC method name fir listens for to receive
// channel messages. Matches pkg/mcp/channel.go in the fir repo.
const ChannelMethod = "notifications/claude/channel"

// ChannelMessage is the payload shape fir expects in a channel notification.
// Matches the ChannelMessage struct in fir's pkg/mcp/channel.go.
type ChannelMessage struct {
	Content string         `json:"content"`
	Meta    map[string]any `json:"meta,omitempty"`
}

// ErrNotConnected is returned by Notifier.SendChannel when no transport
// connection has been captured yet (for example because the MCP server
// has not started running).
var ErrNotConnected = errors.New("mcpnotify: no active connection")

// Notifier holds a reference to the live MCP Connection and exposes a
// SendChannel method that writes a channel notification on it.
type Notifier struct {
	mu   sync.RWMutex
	conn mcp.Connection
}

// NewNotifier returns an empty Notifier. The connection is captured lazily
// when the MCP server connects via a Transport returned by Wrap.
func NewNotifier() *Notifier {
	return &Notifier{}
}

// setConn is called by the capturing Transport once the underlying
// Connection is available.
func (n *Notifier) setConn(c mcp.Connection) {
	n.mu.Lock()
	n.conn = c
	n.mu.Unlock()
}

// SendChannel writes a `notifications/claude/channel` JSON-RPC notification
// on the captured connection. Returns ErrNotConnected if Wrap's Transport
// has not yet been used to Connect, or any underlying Write error.
func (n *Notifier) SendChannel(ctx context.Context, msg ChannelMessage) error {
	n.mu.RLock()
	c := n.conn
	n.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	params, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return c.Write(ctx, &jsonrpc.Request{
		Method: ChannelMethod,
		Params: params,
	})
}

// Wrap returns a Transport that delegates Connect to inner and captures the
// resulting Connection into n. Use the returned transport when calling
// mcp.Server.Run (instead of the raw transport) so the Notifier can send
// notifications on the same connection the MCP server is reading/writing.
func (n *Notifier) Wrap(inner mcp.Transport) mcp.Transport {
	return &capturingTransport{inner: inner, notifier: n}
}

type capturingTransport struct {
	inner    mcp.Transport
	notifier *Notifier
}

func (t *capturingTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	c, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	t.notifier.setConn(c)
	return c, nil
}
