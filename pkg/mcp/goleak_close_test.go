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

// TestManager_Close_AfterSeveredTransport_NoLeak verifies that fir's shutdown
// path cannot be wedged by a peer that vanished.
//
// Under protocol 2026-07-28 fir opens a SEP-2575 "subscriptions/listen" stream
// on EVERY connect: Manager installs both a ToolListChangedHandler and a
// PromptListChangedHandler (client.go), and go-sdk's Client.Connect issues one
// subscriptions/listen when any list-changed handler is set. That stream is a
// long-lived in-flight request, and a connection teardown that waits for
// in-flight requests to drain is exactly the shape that can deadlock.
//
// It exercises the worst case for a fir CLIENT: sever the transport mid-flight
// (a vanished peer — not a clean close, which would give the SDK a chance to
// unwind tidily) and then call Manager.Close under a deadline, verifying with
// goleak that no client goroutine is left parked.
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
	// flight — exactly the state this test needs to set up.
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

	// Run the server under a cancellable context so the test controls server
	// teardown independently of the (severed) client connection.
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
	// otherwise this test proves nothing: go-sdk only issues one when the
	// server advertises the capability, so a silent upstream change here would
	// turn the whole test into a no-op.
	require.Eventually(t, func() bool {
		return listenEntered.Load() > 0
	}, 15*time.Second, 10*time.Millisecond,
		"fir must open a subscriptions/listen stream on connect, else this probe is vacuous")
	require.Zero(t, listenReturned.Load(),
		"the listen handler must still be parked (in flight) when the transport is severed")

	// Sever the transport: the peer vanishes without a protocol-level goodbye.
	conns.breakAll()

	// Now close the manager under a hard deadline. A teardown that waits on the
	// still-parked listen stream would hang here forever.
	done := make(chan error, 1)
	go func() { done <- mgr.Close() }()

	select {
	case err := <-done:
		// A severed transport may surface a write/closed error; the contract
		// under test is that Close RETURNS, not that it returns nil.
		t.Logf("Manager.Close() returned after severed transport: err=%v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("Manager.Close() HUNG for 20s after a severed transport")
	}

	cancelServer()

	// Give the server's own goroutines a moment to unwind before goleak runs;
	// their teardown is asynchronous and is not what this test asserts on.
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

	assert.False(t, mgr.installReconnectedSession(context.Background(), "srv", sess, tools, caps),
		"an install after Close must report refusal")

	mgr.withEntry("srv", func(e *serverEntry) {
		assert.Nil(t, e.session, "a session installed after Close must be refused, not retained")
	})

	cancelServer()
	waitForServerTeardown(t, server)
}

// TestManager_StartServer_AfterClose_DoesNotLeak is the initial-connect twin of
// TestManager_InstallReconnectedSession_AfterClose_DoesNotLeak.
//
// Start (and Reload's toStart batch) dials in flight can complete after Close
// has already snapshotted the entries' sessions. Installing then would strand a
// session nobody ever closes — and it strands silently, because the install's
// defensive ready re-open absorbs the chan Close had closed. startServer must
// refuse instead, and report the refusal to its caller.
func TestManager_StartServer_AfterClose_DoesNotLeak(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	server.AddTool(&sdk.Tool{Name: "ping", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
		})
	serverCtx, cancelServer := context.WithCancel(context.Background())
	defer func() {
		cancelServer()
		waitForServerTeardown(t, server)
	}()

	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	mgr.dialFn = func(_ string, _ ServerConfig) (sdk.Transport, error) {
		serverTransport, clientTransport := sdk.NewInMemoryTransports()
		go func() { _ = server.Run(serverCtx, serverTransport) }()
		return clientTransport, nil
	}

	startAndWait(t, mgr, context.Background())
	require.NoError(t, mgr.Close())

	// A connect that lands after Close, as an in-flight Start would.
	_, err := mgr.startServer(context.Background(), "srv", ServerConfig{})
	require.Error(t, err, "a connect completing after Close must be refused, not installed")

	mgr.withEntry("srv", func(e *serverEntry) {
		assert.Nil(t, e.session, "a session installed after Close must be refused, not retained")
	})
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
//
// This is the sanctioned poll-external-state exception: the SDK exposes no
// signal to subscribe to, only a Sessions() iterator. Session removal is
// asynchronous with respect to the close/cancel that triggered it, hence the
// poll; the deadline is deliberately generous. Expiry is a hard failure — a
// server that never lets go of a session is a teardown hang, and this helper
// is the only place fir would notice one.
func waitForServerTeardown(t *testing.T, server *sdk.Server) {
	t.Helper()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(15 * time.Second)
	for {
		live := 0
		for range server.Sessions() {
			live++
		}
		if live == 0 {
			return
		}
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatalf("server still reports %d live session(s) after 15s: teardown hang", live)
		}
	}
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
	defer func() {
		cancelServer()
		// Let the server unwind before goleak inspects the stacks; its
		// teardown is asynchronous and is not what this test asserts on.
		waitForServerTeardown(t, server)
	}()

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
		assert.False(t, mgr.installReconnectedSession(retired, "srv", sess, tools, caps),
			"a stale loop's install must report refusal")
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
			assert.False(t, mgr.installReconnectedSession(context.Background(), "srv", sess, tools, caps),
				"installing into a removed entry must report refusal")
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
