package mcp

import (
	"context"
	"errors"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// errTransportBroken is returned by a breakableConnection once it has been
// broken, simulating a peer that vanished.
var errTransportBroken = errors.New("transport broken by test")

// breakableTransport wraps a transport so a test can sever the connection at
// an arbitrary point, as if the peer had gone away.
//
// This exists because sdk.ServerSession.Close deadlocks while a
// subscriptions/listen request is in flight (go-sdk v1.7.0), and every fir
// client opens one during connect — see
// https://github.com/modelcontextprotocol/go-sdk/issues/1160. Severing the
// client-side connection is an equivalent stimulus for the reconnect paths
// under test and does not depend on the server shutting down cleanly.
type breakableTransport struct {
	inner sdk.Transport
	conns *breakableConns
}

type breakableConns struct {
	mu    sync.Mutex
	conns []*breakableConnection
}

// breakAll severs every connection handed out by the transport. Connections
// dialled afterwards are unaffected — that is what lets the reconnect tests
// sever a live connection and still watch the client come back.
func (b *breakableConns) breakAll() {
	b.mu.Lock()
	conns := append([]*breakableConnection(nil), b.conns...)
	b.conns = nil
	b.mu.Unlock()
	for _, c := range conns {
		c.markBroken()
		_ = c.inner.Close()
	}
}

func (t *breakableTransport) Connect(ctx context.Context) (sdk.Connection, error) {
	conn, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	bc := &breakableConnection{inner: conn}
	t.conns.mu.Lock()
	t.conns.conns = append(t.conns.conns, bc)
	t.conns.mu.Unlock()
	return bc, nil
}

type breakableConnection struct {
	inner  sdk.Connection
	mu     sync.Mutex
	broken chan struct{}
	once   sync.Once
}

func (c *breakableConnection) brokenCh() chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken == nil {
		c.broken = make(chan struct{})
	}
	return c.broken
}

func (c *breakableConnection) markBroken() {
	ch := c.brokenCh()
	c.once.Do(func() { close(ch) })
}

func (c *breakableConnection) isBroken() bool {
	select {
	case <-c.brokenCh():
		return true
	default:
		return false
	}
}

func (c *breakableConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	type res struct {
		msg jsonrpc.Message
		err error
	}
	ch := make(chan res, 1)
	go func() {
		msg, err := c.inner.Read(ctx)
		ch <- res{msg, err}
	}()
	select {
	case r := <-ch:
		if c.isBroken() {
			return nil, errTransportBroken
		}
		return r.msg, r.err
	case <-c.brokenCh():
		return nil, errTransportBroken
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *breakableConnection) Write(ctx context.Context, msg jsonrpc.Message) error {
	if c.isBroken() {
		return errTransportBroken
	}
	return c.inner.Write(ctx, msg)
}

func (c *breakableConnection) Close() error {
	c.markBroken()
	return c.inner.Close()
}

func (c *breakableConnection) SessionID() string { return c.inner.SessionID() }

// severRegistry maps a test server to the breakable connections that have been
// dialed against it, so a test helper can sever a server's connections given
// only the *sdk.Server — no plumbing of a conns handle through call sites.
//
// Registration happens in inMemoryDial; severing in severServerConnections.
var severRegistry = struct {
	mu sync.Mutex
	m  map[*sdk.Server]*breakableConns
}{m: make(map[*sdk.Server]*breakableConns)}

// connsForServer returns the breakableConns registered for server, creating
// the entry on first use.
func connsForServer(server *sdk.Server) *breakableConns {
	severRegistry.mu.Lock()
	defer severRegistry.mu.Unlock()
	c, ok := severRegistry.m[server]
	if !ok {
		c = &breakableConns{}
		severRegistry.m[server] = c
	}
	return c
}

// forgetServerConns drops a server's registry entry, so the map does not
// retain every server the test binary ever built.
func forgetServerConns(server *sdk.Server) {
	severRegistry.mu.Lock()
	defer severRegistry.mu.Unlock()
	delete(severRegistry.m, server)
}
