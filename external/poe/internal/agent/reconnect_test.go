package agent

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kfet/fir/external/poe/internal/relay"
)

// startRelayOnAddr starts a relay hub with ws handler on the given addr.
func startRelayOnAddr(t *testing.T, addr string) (*relay.Hub, func()) {
	t.Helper()
	hub := relay.NewHubNoGrace()
	srv, err := hub.StartOnAddr(addr)
	require.NoError(t, err)
	return hub, func() {
		hub.CloseAllAgents()
		srv.Close()
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// waitCh waits for a signal on ch or fails after timeout.
func waitCh(t *testing.T, ch <-chan struct{}, timeout time.Duration, msg string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(timeout):
		t.Fatal(msg)
	}
}

// connectWithCallbacks creates an agent with OnConnect/OnDisconnect wired to channels.
// Callbacks are set on the Config so they're in place before the reconnect goroutine starts.
func connectWithCallbacks(t *testing.T, ctx context.Context, url, convID string) (*Agent, <-chan struct{}, <-chan struct{}) {
	t.Helper()
	connectCh := make(chan struct{}, 8)
	disconnectCh := make(chan struct{}, 8)

	a, err := ConnectWithDial(ctx, Config{RelayURL: url, ConvID: convID}, DefaultDial)
	require.NoError(t, err)

	// Set callbacks under the agent's lock to avoid racing with reconnectLoop.
	a.mu.Lock()
	a.OnConnect = func() {
		select {
		case connectCh <- struct{}{}:
		default:
		}
	}
	a.OnDisconnect = func(err error) {
		select {
		case disconnectCh <- struct{}{}:
		default:
		}
	}
	a.mu.Unlock()

	return a, connectCh, disconnectCh
}

func TestReconnect_AfterRelayRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr := freePort(t)
	url := "ws://" + addr + "/ws"

	_, stop1 := startRelayOnAddr(t, addr)

	a, connectCh, disconnectCh := connectWithCallbacks(t, ctx, url, "c-1")
	_ = a

	// Kill relay → agent disconnects.
	stop1()
	waitCh(t, disconnectCh, 5*time.Second, "agent should detect disconnect")

	// Start new relay → agent reconnects.
	hub2, stop2 := startRelayOnAddr(t, addr)
	defer stop2()

	waitCh(t, connectCh, 20*time.Second, "agent should reconnect to new relay")

	// Verify re-registration via hub (small poll — hub state is set synchronously on register).
	require.Eventually(t, func() bool { return hub2.HasAgent("c-1") }, 3*time.Second, 10*time.Millisecond,
		"agent should re-register conv_id after reconnect")
}

func TestReconnect_ReplyDuringDisconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr := freePort(t)
	url := "ws://" + addr + "/ws"
	_, stop := startRelayOnAddr(t, addr)

	a, _, disconnectCh := connectWithCallbacks(t, ctx, url, "c-1")

	stop()
	waitCh(t, disconnectCh, 5*time.Second, "agent should detect disconnect")

	// Reply should return ErrDisconnected, not hang.
	errCh := make(chan error, 1)
	go func() { errCh <- a.Reply("m-1", "hello", true, false, false, "") }()

	select {
	case err := <-errCh:
		assert.ErrorIs(t, err, ErrDisconnected)
	case <-time.After(2 * time.Second):
		t.Fatal("Reply hung during disconnect")
	}
}

func TestReconnect_QueryDeliveryAfterReconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr := freePort(t)
	url := "ws://" + addr + "/ws"

	_, stop := startRelayOnAddr(t, addr)

	queryCh := make(chan relay.RelayMsg, 1)
	a, connectCh, disconnectCh := connectWithCallbacks(t, ctx, url, "c-1")
	a.OnQuery = func(msg relay.RelayMsg) { queryCh <- msg }

	stop()
	waitCh(t, disconnectCh, 5*time.Second, "agent should disconnect")

	hub2, stop2 := startRelayOnAddr(t, addr)
	defer stop2()

	waitCh(t, connectCh, 20*time.Second, "agent should reconnect")
	require.Eventually(t, func() bool { return hub2.HasAgent("c-1") }, 3*time.Second, 10*time.Millisecond)

	hub2.InjectQuery("c-1", relay.RelayMsg{
		Type: "query", ConvID: "c-1", MessageID: "m-after", Content: "hello after reconnect",
	})

	select {
	case msg := <-queryCh:
		assert.Equal(t, "m-after", msg.MessageID)
		assert.Equal(t, "hello after reconnect", msg.Content)
	case <-time.After(3 * time.Second):
		t.Fatal("query not delivered after reconnect")
	}
}

func TestReconnect_BackoffIncreases(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr := freePort(t)
	url := "ws://" + addr + "/ws"

	_, stop := startRelayOnAddr(t, addr)
	defer stop()

	var dialTimes []time.Time
	var dialCount atomic.Int32
	reconnectedCh := make(chan struct{}, 1)

	failDial := func(ctx context.Context, url string) (*websocket.Conn, error) {
		dialTimes = append(dialTimes, time.Now())
		n := dialCount.Add(1)
		if n <= 4 {
			return nil, fmt.Errorf("refused")
		}
		ws, err := DefaultDial(ctx, url)
		if err == nil {
			select {
			case reconnectedCh <- struct{}{}:
			default:
			}
		}
		return ws, err
	}

	a, err := ConnectWithDial(ctx, Config{RelayURL: url}, DefaultDial)
	require.NoError(t, err)

	a.dial = failDial
	a.closeWS()

	waitCh(t, reconnectedCh, 30*time.Second, "agent should reconnect after backoff failures")
	require.GreaterOrEqual(t, int(dialCount.Load()), 5)

	if len(dialTimes) >= 3 {
		gap1 := dialTimes[1].Sub(dialTimes[0])
		gap2 := dialTimes[2].Sub(dialTimes[1])
		assert.GreaterOrEqual(t, gap2+200*time.Millisecond, gap1,
			"backoff should increase: gap1=%v gap2=%v", gap1, gap2)
	}
}

func TestReconnect_ContextCancelStopsLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	addr := freePort(t)
	url := "ws://" + addr + "/ws"
	_, stop := startRelayOnAddr(t, addr)

	a, _, disconnectCh := connectWithCallbacks(t, ctx, url, "")

	stop()
	waitCh(t, disconnectCh, 5*time.Second, "agent should disconnect")

	cancel()

	select {
	case <-a.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("reconnect loop did not exit after context cancel")
	}
}
