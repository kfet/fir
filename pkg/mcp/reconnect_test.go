package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kfet/fir/pkg/agent"
)

// shortenReconnectDelays speeds up the auto-reconnect loop so tests don't
// burn seconds on backoff sleeps. Restored via t.Cleanup.
func shortenReconnectDelays(t *testing.T) {
	t.Helper()
	origInitial := reconnectInitialDelay
	origMax := reconnectMaxDelay
	reconnectInitialDelay = 5 * time.Millisecond
	reconnectMaxDelay = 50 * time.Millisecond
	t.Cleanup(func() {
		reconnectInitialDelay = origInitial
		reconnectMaxDelay = origMax
	})
}

// closeServerSession closes the active server-side session, which causes the
// client's session.Wait() to return and triggers the auto-reconnect path.
func closeServerSession(t *testing.T, server *sdk.Server) {
	t.Helper()
	for s := range server.Sessions() {
		require.NoError(t, s.Close())
		return
	}
	t.Fatal("server has no active sessions")
}

func makePingServer() *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: "reconn-test", Version: "0"}, nil)
	s.AddTool(&sdk.Tool{Name: "ping", InputSchema: emptySchema},
		func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "pong"}}}, nil
		})
	return s
}

// TestManager_AutoReconnect_AfterServerDisconnect verifies that when the
// server closes the connection, the manager's auto-reconnect loop installs
// a fresh session without any external trigger, and onToolsChanged fires
// again with the (refreshed) tool list.
func TestManager_AutoReconnect_AfterServerDisconnect(t *testing.T) {
	shortenReconnectDelays(t)

	server := makePingServer()
	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)

	// Capture onToolsChanged invocations; signal each via a buffered chan.
	notifs := make(chan int, 16)
	mgr.SetOnToolsChanged(func(tools []agent.AgentTool) {
		notifs <- len(tools)
	})

	mgr.Start(context.Background())
	require.NoError(t, mgr.WaitReady(context.Background()))
	defer mgr.Close()

	require.True(t, mgr.hasSession("srv"))
	// Initial connect emitted at least one notification.
	requireNotifWithin(t, notifs, time.Second)

	// Force a server-side disconnect.
	closeServerSession(t, server)

	// Auto-reconnect should restore the session without /reload.
	require.Eventually(t, func() bool {
		return mgr.hasSession("srv")
	}, 3*time.Second, 10*time.Millisecond, "session should be restored by auto-reconnect")

	// We expect at least three notifications by now: initial install,
	// disconnect (tools cleared), and reconnect (tools restored).
	deadline := time.Now().Add(2 * time.Second)
	emptySeen := false
	restoredSeen := false
	for !emptySeen || !restoredSeen {
		if time.Now().After(deadline) {
			break
		}
		select {
		case n := <-notifs:
			if n == 0 {
				emptySeen = true
			} else if n > 0 && emptySeen {
				restoredSeen = true
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	assert.True(t, emptySeen, "should observe a disconnect notification with empty tools")
	assert.True(t, restoredSeen, "should observe a post-reconnect notification with tools")
}

func requireNotifWithin(t *testing.T, ch <-chan int, d time.Duration) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(d):
		t.Fatal("timed out waiting for tools notification")
	}
}

// TestManager_OnDemandReconnect_KicksLoop verifies that calling CallTool
// while the reconnect loop is sleeping wakes it immediately and the call
// proceeds without waiting out the full backoff.
func TestManager_OnDemandReconnect_KicksLoop(t *testing.T) {
	shortenReconnectDelays(t)
	// Make initial backoff long so the kick is observable.
	reconnectInitialDelay = 500 * time.Millisecond
	reconnectMaxDelay = 500 * time.Millisecond

	server := makePingServer()
	// Counting dial that fails the first reconnect attempt, then succeeds.
	var dialCount atomic.Int32
	realDial := inMemoryDial(t, server)
	dial := func(cfg ServerConfig) (sdk.Transport, error) {
		n := dialCount.Add(1)
		// First (initial) connect succeeds. First reconnect fails so we
		// enter a backoff sleep that the kick will interrupt. Subsequent
		// attempts succeed.
		if n == 2 {
			return nil, errors.New("simulated reconnect dial failure")
		}
		return realDial(cfg)
	}
	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	mgr.dialFn = dial

	mgr.Start(context.Background())
	require.NoError(t, mgr.WaitReady(context.Background()))
	defer mgr.Close()
	require.True(t, mgr.hasSession("srv"))

	// Force disconnect; loop will dial (fails), then sleep ~500ms.
	closeServerSession(t, server)

	// Wait until the loop is in the backoff sleep (session has been
	// cleared and dial #2 has happened).
	require.Eventually(t, func() bool {
		return dialCount.Load() >= 2 && !mgr.hasSession("srv")
	}, 2*time.Second, 5*time.Millisecond, "loop should reach backoff sleep")

	// Call CallTool — this should kick the loop and reconnect within
	// well under 500ms, far faster than the natural backoff would allow.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	result, err := mgr.CallTool(ctx, "srv", "ping", nil)
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Less(t, elapsed, 400*time.Millisecond, "kick should beat the 500ms backoff")
}

// TestManager_OnDemand_RespectsContext verifies that ensureConnected returns
// ctx.Err() when the caller's context expires while a reconnect is pending.
func TestManager_OnDemand_RespectsContext(t *testing.T) {
	shortenReconnectDelays(t)
	server := makePingServer()
	// Dial that succeeds once then fails forever, so the reconnect loop
	// will be stuck retrying.
	var dialCount atomic.Int32
	realDial := inMemoryDial(t, server)
	dial := func(cfg ServerConfig) (sdk.Transport, error) {
		if dialCount.Add(1) == 1 {
			return realDial(cfg)
		}
		return nil, errors.New("permanent dial failure")
	}
	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	mgr.dialFn = dial

	mgr.Start(context.Background())
	require.NoError(t, mgr.WaitReady(context.Background()))
	defer mgr.Close()

	closeServerSession(t, server)

	// Wait for session to drop.
	require.Eventually(t, func() bool { return !mgr.hasSession("srv") },
		2*time.Second, 5*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := mgr.CallTool(ctx, "srv", "ping", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

// TestManager_OnDemand_NoLoopReturnsNotConnected verifies that CallTool on
// an entry whose initial connect failed (no loop running) returns "not
// connected" rather than blocking forever.
func TestManager_OnDemand_NoLoopReturnsNotConnected(t *testing.T) {
	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	mgr.dialFn = func(_ ServerConfig) (sdk.Transport, error) {
		return nil, errors.New("initial dial fails")
	}
	mgr.Start(context.Background())
	require.NoError(t, mgr.WaitReady(context.Background()))
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := mgr.CallTool(ctx, "srv", "ping", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not connected")
}

// TestManager_ConcurrentCallTool_SingleFlight verifies that many concurrent
// CallTool callers during a reconnect window share a single dial: dialCount
// increases by exactly 1 (the actual reconnect).
func TestManager_ConcurrentCallTool_SingleFlight(t *testing.T) {
	shortenReconnectDelays(t)
	reconnectInitialDelay = 200 * time.Millisecond
	reconnectMaxDelay = 200 * time.Millisecond

	server := makePingServer()
	var dialCount atomic.Int32
	realDial := inMemoryDial(t, server)
	dial := func(cfg ServerConfig) (sdk.Transport, error) {
		dialCount.Add(1)
		return realDial(cfg)
	}
	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	mgr.dialFn = dial

	mgr.Start(context.Background())
	require.NoError(t, mgr.WaitReady(context.Background()))
	defer mgr.Close()
	require.Equal(t, int32(1), dialCount.Load())

	closeServerSession(t, server)
	require.Eventually(t, func() bool { return !mgr.hasSession("srv") },
		2*time.Second, 5*time.Millisecond)

	// Fire many concurrent CallTools. They should all converge on a single
	// reconnect dial.
	const N = 20
	var wg sync.WaitGroup
	errs := make([]error, N)
	for i := range N {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, errs[i] = mgr.CallTool(ctx, "srv", "ping", nil)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		assert.NoErrorf(t, err, "call %d", i)
	}
	// Expect 2 dials total (initial + 1 reconnect). Allow up to 3 in case
	// the loop made a brief retry before all callers landed.
	assert.LessOrEqual(t, dialCount.Load(), int32(3),
		"single-flight: ~one dial for the reconnect (dial count = %d)", dialCount.Load())
}

// TestManager_Close_CancelsReconnect verifies that Close cancels an in-flight
// reconnect loop without leaking goroutines or hanging.
func TestManager_Close_CancelsReconnect(t *testing.T) {
	shortenReconnectDelays(t)
	server := makePingServer()
	// Dial succeeds first, fails forever after — loop will be stuck retrying.
	var dialCount atomic.Int32
	realDial := inMemoryDial(t, server)
	dial := func(cfg ServerConfig) (sdk.Transport, error) {
		if dialCount.Add(1) == 1 {
			return realDial(cfg)
		}
		return nil, errors.New("permanent")
	}
	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	mgr.dialFn = dial
	mgr.Start(context.Background())
	require.NoError(t, mgr.WaitReady(context.Background()))

	closeServerSession(t, server)
	require.Eventually(t, func() bool { return !mgr.hasSession("srv") },
		2*time.Second, 5*time.Millisecond)

	// Close should return promptly even though the loop is mid-retry.
	done := make(chan struct{})
	go func() {
		_ = mgr.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return within 2s — reconnect loop leak?")
	}

	// CallTool after Close must fail fast.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := mgr.CallTool(ctx, "srv", "ping", nil)
	require.Error(t, err)
}

// TestManager_ReconnectBackoff_SurfacesErrAfterThreshold verifies that
// after reconnectErrSurfaceThreshold consecutive dial failures, the entry's
// err becomes visible via Status().
func TestManager_ReconnectBackoff_SurfacesErrAfterThreshold(t *testing.T) {
	shortenReconnectDelays(t)
	server := makePingServer()
	var dialCount atomic.Int32
	realDial := inMemoryDial(t, server)
	dial := func(cfg ServerConfig) (sdk.Transport, error) {
		if dialCount.Add(1) == 1 {
			return realDial(cfg)
		}
		return nil, errors.New("permanent dial failure")
	}
	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	mgr.dialFn = dial
	mgr.Start(context.Background())
	require.NoError(t, mgr.WaitReady(context.Background()))
	defer mgr.Close()

	closeServerSession(t, server)

	// After enough failed reconnects, the err should surface in Status().
	require.Eventually(t, func() bool {
		st := mgr.Status()
		if len(st) != 1 {
			return false
		}
		return st[0].Error != nil && !st[0].Connected
	}, 2*time.Second, 10*time.Millisecond,
		"err should surface after %d failed reconnects", reconnectErrSurfaceThreshold)
}

// TestReconnectBackoff verifies the backoff schedule: monotonically growing
// (modulo jitter) up to the cap, and stays at the cap thereafter.
func TestReconnectBackoff(t *testing.T) {
	// Save & restore globals.
	origInit, origMax := reconnectInitialDelay, reconnectMaxDelay
	t.Cleanup(func() {
		reconnectInitialDelay = origInit
		reconnectMaxDelay = origMax
	})
	reconnectInitialDelay = 100 * time.Millisecond
	reconnectMaxDelay = 800 * time.Millisecond

	// Each delay must lie within ±20% of the nominal exponential value,
	// capped at reconnectMaxDelay.
	cases := []struct {
		attempt int
		nominal time.Duration
	}{
		{1, 100 * time.Millisecond},
		{2, 200 * time.Millisecond},
		{3, 400 * time.Millisecond},
		{4, 800 * time.Millisecond},
		{5, 800 * time.Millisecond}, // capped
		{10, 800 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("attempt=%d", tc.attempt), func(t *testing.T) {
			d := reconnectBackoff(tc.attempt)
			low := tc.nominal - tc.nominal/5
			high := tc.nominal + tc.nominal/5
			assert.GreaterOrEqual(t, d, low, "below jitter range")
			assert.LessOrEqual(t, d, high, "above jitter range")
		})
	}
	// attempt=0 should be treated as 1 (no negative shift).
	assert.NotPanics(t, func() { reconnectBackoff(0) })
}

// TestEnsureConnected_FastPath verifies that when the session is already
// installed, ensureConnected returns immediately without going near the
// kick/ready dance.
func TestEnsureConnected_FastPath(t *testing.T) {
	server := makePingServer()
	mgr := NewManager(map[string]ServerConfig{"srv": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)
	mgr.Start(context.Background())
	require.NoError(t, mgr.WaitReady(context.Background()))
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	sess, err := mgr.ensureConnected(ctx, "srv")
	require.NoError(t, err)
	require.NotNil(t, sess)
}

// TestManager_StreamableTransport_AutoReconnect is the wire-level e2e for
// the original bug: a streamable HTTP MCP server (the same transport that
// grafana uses) terminates its session, and fir's manager transparently
// reconnects so subsequent tool calls succeed without /reload.
//
// Unlike the in-memory tests above, this exercises the real
// StreamableClientTransport: HTTP requests, SSE event streams, the DELETE
// session-terminate handshake, and reconnect of a fresh session ID. It is
// the regression guard for the disconnect/reconnect path that the
// pkg/mcp/client.go fix was about.
func TestManager_StreamableTransport_AutoReconnect(t *testing.T) {
	shortenReconnectDelays(t)

	server := makePingServer()
	handler := sdk.NewStreamableHTTPHandler(
		func(*http.Request) *sdk.Server { return server }, nil)
	httpSrv := httptest.NewServer(handler)
	t.Cleanup(httpSrv.Close)

	cfg := ServerConfig{Transport: "streamable", URL: httpSrv.URL}
	mgr := NewManager(map[string]ServerConfig{"http-srv": cfg}, false)

	// Capture tool-list notifications so we can assert that onToolsChanged
	// fires across the disconnect/reconnect cycle (the contract we're
	// fulfilling: tool list stays accurate after reconnect).
	var notifs atomic.Int32
	mgr.SetOnToolsChanged(func(_ []agent.AgentTool) { notifs.Add(1) })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	mgr.Start(ctx)
	require.NoError(t, mgr.WaitReady(ctx))
	defer mgr.Close()
	require.True(t, mgr.hasSession("http-srv"))
	notifsAtConnect := notifs.Load()
	require.GreaterOrEqual(t, notifsAtConnect, int32(1))

	// First tool call over the streamable wire.
	result, err := mgr.CallTool(ctx, "http-srv", "ping", nil)
	require.NoError(t, err)
	require.Equal(t, "pong", result.Content[0].(*sdk.TextContent).Text)

	// Force the server to terminate its current session. The client's
	// session.Wait() returns and the auto-reconnect loop kicks in.
	for s := range server.Sessions() {
		require.NoError(t, s.Close())
		break
	}

	// Auto-reconnect should restore the session over a fresh streamable
	// HTTP session ID. Verify by calling the tool again — ensureConnected
	// will block briefly on the ready chan if the loop hasn't installed
	// the new session yet, then proceed.
	require.Eventually(t, func() bool {
		ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel2()
		_, err := mgr.CallTool(ctx2, "http-srv", "ping", nil)
		return err == nil && mgr.hasSession("http-srv")
	}, 5*time.Second, 50*time.Millisecond,
		"streamable transport should auto-reconnect after server-side session.Close()")

	// onToolsChanged must have fired again (post-reconnect notification),
	// so any UI listener picks up the refreshed tool list.
	assert.Greater(t, notifs.Load(), notifsAtConnect,
		"onToolsChanged must re-fire after auto-reconnect")
}
