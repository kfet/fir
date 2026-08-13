package mcp

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// TestManager_Close_AfterSeveredTransport_NoLeak is the production-impact
// probe for upstream go-sdk issue #1160:
//
//	https://github.com/modelcontextprotocol/go-sdk/issues/1160
//
// #1160 is that sdk.ServerSession.Close deadlocks while a SEP-2575
// "subscriptions/listen" stream is in flight: Server.subscriptionsListen is a
// long-lived handler that parks on <-ctx.Done() while running as an ordinary
// in-flight incoming request, and jsonrpc2's Connection.Close waits for
// in-flight requests to drain.
//
// fir opens such a stream on EVERY connect: Manager installs both a
// ToolListChangedHandler and a PromptListChangedHandler (client.go), and
// go-sdk's Client.Connect issues one subscriptions/listen when any
// list-changed handler is set. So the question this test answers is whether
// the deadlock reaches fir's own shutdown path.
//
// It exercises the worst case for a fir CLIENT: sever the transport mid-flight
// (a vanished peer — not a clean close, which would give the SDK a chance to
// unwind tidily) and then call Manager.Close under a deadline, verifying with
// goleak that no client goroutine is left parked.
//
// RESULT (v1.7.0): Close returns promptly and no client goroutine leaks.
// fir is NOT bitten by #1160 in production, because fir is only ever an MCP
// client. ClientSession.Close cancels its listenCtx BEFORE closing the
// connection (go-sdk mcp/client.go), so the client half of the listen stream
// always unwinds. The deadlock lives exclusively in ServerSession.Close, and
// fir's only server constructor (NewToolServer) has no production caller — it
// is used by tests alone. See the package tests' breakableTransport for the
// test-side workaround.
func TestManager_Close_AfterSeveredTransport_NoLeak(t *testing.T) {
	// Snapshot pre-existing goroutines (testing/other packages' background
	// workers) so we only assert on what THIS test creates.
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(&sdk.Tool{Name: "ping", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		})

	// Observe the SEP-2575 listen stream directly, so this probe cannot
	// silently degrade into a no-op if fir ever stops opening one.
	// subscriptionsListen blocks for the life of the stream, so a request that
	// has entered the handler but not returned is one that is genuinely in
	// flight — exactly the state #1160 deadlocks on.
	var listenEntered, listenReturned atomic.Int32
	server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			if method != "subscriptions/listen" {
				return next(ctx, method, req)
			}
			listenEntered.Add(1)
			defer listenReturned.Add(1)
			return next(ctx, method, req)
		}
	})

	// Run the server under a cancellable context so the test can tear down the
	// SERVER side without calling ServerSession.Close (which is what #1160
	// deadlocks). Cancelling Run's context is the supported escape hatch.
	serverCtx, cancelServer := context.WithCancel(context.Background())

	conns := &breakableConns{}
	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	mgr.dialFn = func(_ string, _ ServerConfig) (sdk.Transport, error) {
		serverTransport, clientTransport := sdk.NewInMemoryTransports()
		go func() { _ = server.Run(serverCtx, serverTransport) }()
		return &breakableTransport{inner: clientTransport, conns: conns}, nil
	}

	startAndWait(t, mgr, context.Background())

	// The client must genuinely have an open subscriptions/listen stream,
	// otherwise this probe proves nothing about #1160.
	require.Eventually(t, func() bool {
		return listenEntered.Load() > 0
	}, 15*time.Second, 10*time.Millisecond,
		"fir must open a subscriptions/listen stream on connect, else this probe is vacuous")
	require.Zero(t, listenReturned.Load(),
		"the listen handler must still be parked (in flight) when the transport is severed")

	// Sever the transport: the peer vanishes without a protocol-level goodbye.
	conns.breakAll()

	// Now close the manager under a hard deadline. If #1160 reached the client
	// this is where it would hang forever.
	done := make(chan error, 1)
	go func() { done <- mgr.Close() }()

	select {
	case err := <-done:
		// A severed transport may surface a write/closed error; the contract
		// under test is that Close RETURNS, not that it returns nil.
		t.Logf("Manager.Close() returned after severed transport: err=%v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Manager.Close() HUNG for 5s after a severed transport — " +
			"go-sdk #1160 bites fir in production")
	}

	// Tear the server down the only way that cannot deadlock under #1160.
	cancelServer()

	// Give the server's own goroutines a moment to unwind before goleak runs;
	// their teardown is asynchronous and is not what this test asserts on.
	// A failure here that names only mcp.(*Server) frames is #1160's
	// server-side half and does not implicate fir.
	waitForServerTeardown(t, server)
}

// TestManager_InstallReconnectedSession_AfterClose_DoesNotLeak covers the
// shutdown handoff in installReconnectedSession.
//
// Manager.Close snapshots each entry's session and closes it, and only then
// cancels the reconnect loops. A dial that completes inside that window calls
// installReconnectedSession and would store a session nobody ever closes —
// stranding its SEP-2575 subscriptions/listen goroutine for the life of the
// process (see TestManager_ReconnectCycles_DoNotLeakListenGoroutines for why
// only ClientSession.Close releases it).
//
// The window is too narrow to hit reliably through the reconnect loop, so the
// guard is exercised directly: install a freshly connected session into a
// manager that has already been closed. It must be refused and closed, not
// retained.
func TestManager_InstallReconnectedSession_AfterClose_DoesNotLeak(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(&sdk.Tool{Name: "ping", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		})
	serverCtx, cancelServer := context.WithCancel(context.Background())

	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	mgr.dialFn = func(_ string, _ ServerConfig) (sdk.Transport, error) {
		serverTransport, clientTransport := sdk.NewInMemoryTransports()
		go func() { _ = server.Run(serverCtx, serverTransport) }()
		return clientTransport, nil
	}

	startAndWait(t, mgr, context.Background())
	require.NoError(t, mgr.Close())

	// Dial a session the way a reconnect in flight during Close would have,
	// so it carries a real subscriptions/listen stream.
	sess, tools, caps, err := mgr.dialAndInitialize(context.Background(), "srv", ServerConfig{})
	require.NoError(t, err)

	mgr.installReconnectedSession(context.Background(), "srv", sess, tools, caps)

	mgr.withEntry("srv", func(e *serverEntry) {
		assert.Nil(t, e.session, "a session installed after Close must be refused, not retained")
	})

	cancelServer()
	waitForServerTeardown(t, server)
}

// TestManager_ReconnectCycles_DoNotLeakListenGoroutines is the regression test
// for the production bug this branch's goleak work uncovered.
//
// Under protocol 2026-07-28 every fir connect opens a SEP-2575
// "subscriptions/listen" stream, and go-sdk derives that stream's context from
// context.Background() inside Client.Connect — so it does NOT unwind when the
// connection dies. ClientSession.Close is the only thing that cancels it.
// fir's handleSessionEnd used to drop the dead session without closing it,
// which stranded one mcp.callSubscriptionsListen goroutine per reconnect for
// the life of the process. A long-running fir talking to a flaky MCP server
// would accumulate them indefinitely.
//
// Three disconnect/reconnect cycles previously leaked exactly three
// goroutines; goleak must now find none.
func TestManager_ReconnectCycles_DoNotLeakListenGoroutines(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	shortenReconnectDelays(t)

	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(&sdk.Tool{Name: "ping", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		})
	serverCtx, cancelServer := context.WithCancel(context.Background())

	conns := &breakableConns{}
	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	mgr.dialFn = func(_ string, _ ServerConfig) (sdk.Transport, error) {
		serverTransport, clientTransport := sdk.NewInMemoryTransports()
		go func() { _ = server.Run(serverCtx, serverTransport) }()
		return &breakableTransport{inner: clientTransport, conns: conns}, nil
	}

	_, readyCh, _ := drainServerEvents(mgr)
	startAndWait(t, mgr, context.Background())

	select {
	case <-readyCh:
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for initial ServerReady")
	}

	const cycles = 3
	for i := range cycles {
		conns.breakAll()
		select {
		case <-readyCh:
		case <-time.After(15 * time.Second):
			t.Fatalf("cycle %d: timeout waiting for reconnect", i)
		}
	}

	require.NoError(t, mgr.Close())
	cancelServer()
	waitForServerTeardown(t, server)
}

// waitForServerTeardown blocks until the in-memory test server has no live
// sessions, so goleak does not race the server's asynchronous unwind.
func waitForServerTeardown(t *testing.T, server *sdk.Server) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		live := false
		for ss := range server.Sessions() {
			_ = ss
			live = true
			break
		}
		if !live {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Log("server still reports a live session after 15s (go-sdk #1160 server-side half)")
}

// TestManager_InstallReconnectedSession_RetiredLoop_DoesNotLeak covers the two
// non-Close ways a reconnect loop can be retired while its dial is still in
// flight. In both, installReconnectedSession must refuse the session and close
// it rather than strand it (and its subscriptions/listen goroutine).
//
//   - "config changed": Reload cancels the loop's ctx and starts a fresh one,
//     so the in-flight dial belongs to a stale loop.
//   - "server removed": Reload deletes the entry outright, so there is nothing
//     left to install into. This case additionally used to panic with
//     "close of nil channel", because the install closed an entry ready chan
//     it had never been handed.
func TestManager_InstallReconnectedSession_RetiredLoop_DoesNotLeak(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(&sdk.Tool{Name: "ping", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		})
	serverCtx, cancelServer := context.WithCancel(context.Background())
	defer cancelServer()

	newMgr := func() *Manager {
		mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
		mgr.dialFn = func(_ string, _ ServerConfig) (sdk.Transport, error) {
			serverTransport, clientTransport := sdk.NewInMemoryTransports()
			go func() { _ = server.Run(serverCtx, serverTransport) }()
			return clientTransport, nil
		}
		return mgr
	}

	t.Run("stale loop ctx", func(t *testing.T) {
		mgr := newMgr()
		startAndWait(t, mgr, context.Background())
		defer mgr.Close()

		sess, tools, caps, err := mgr.dialAndInitialize(context.Background(), "srv", ServerConfig{})
		require.NoError(t, err)

		// The loop that started this dial has been retired.
		retired, cancelRetired := context.WithCancel(context.Background())
		cancelRetired()

		before := sessionOf(t, mgr, "srv")
		mgr.installReconnectedSession(retired, "srv", sess, tools, caps)
		assert.Same(t, before, sessionOf(t, mgr, "srv"),
			"a stale loop's session must not replace the live one")
	})

	t.Run("entry removed", func(t *testing.T) {
		mgr := newMgr()
		startAndWait(t, mgr, context.Background())

		sess, tools, caps, err := mgr.dialAndInitialize(context.Background(), "srv", ServerConfig{})
		require.NoError(t, err)

		// Reload removing the server cancels the entry's reconnect loop, closes
		// its session, and only then deletes the entry. Mirror all three, or
		// Manager.Close would wait forever on a loop that nothing can stop.
		orig := sessionOf(t, mgr, "srv")
		require.NotNil(t, orig)
		var cancelLoop context.CancelFunc
		mgr.withEntry("srv", func(e *serverEntry) {
			cancelLoop = e.reconnectCancel
			e.reconnectCancel = nil
		})
		require.NotNil(t, cancelLoop)
		cancelLoop()
		require.NoError(t, orig.Close())
		mgr.servers.Delete("srv")
		defer mgr.Close()

		assert.NotPanics(t, func() {
			mgr.installReconnectedSession(context.Background(), "srv", sess, tools, caps)
		}, "installing into a removed entry must not panic on a nil ready chan")
	})
}

// sessionOf returns the session currently installed for a server, or nil.
func sessionOf(t *testing.T, mgr *Manager, name string) *sdk.ClientSession {
	t.Helper()
	var s *sdk.ClientSession
	mgr.withEntry(name, func(e *serverEntry) { s = e.session })
	return s
}
