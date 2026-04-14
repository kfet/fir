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
// Returns the hub and a stop func that closes all agents and shuts down.
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

// freePort returns a free TCP port on localhost.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestReconnect_AfterRelayRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr := freePort(t)
	url := "ws://" + addr + "/ws"

	_, stop1 := startRelayOnAddr(t, addr)

	a, err := ConnectWithDial(ctx, Config{RelayURL: url, ConvID: "c-1"}, DefaultDial)
	require.NoError(t, err)
	assert.True(t, a.Connected())

	// Kill relay.
	stop1()

	// Wait for disconnect.
	require.Eventually(t, func() bool { return !a.Connected() }, 10*time.Second, 50*time.Millisecond,
		"agent should detect disconnect")

	// Start new relay on same address.
	hub2, stop2 := startRelayOnAddr(t, addr)
	defer stop2()

	// Agent should reconnect and re-register.
	require.Eventually(t, func() bool { return a.Connected() }, 20*time.Second, 100*time.Millisecond,
		"agent should reconnect to new relay")

	require.Eventually(t, func() bool { return hub2.HasAgent("c-1") }, 5*time.Second, 50*time.Millisecond,
		"agent should re-register conv_id after reconnect")
}

func TestReconnect_ReplyDuringDisconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr := freePort(t)
	url := "ws://" + addr + "/ws"
	_, stop := startRelayOnAddr(t, addr)

	a, err := ConnectWithDial(ctx, Config{RelayURL: url, ConvID: "c-1"}, DefaultDial)
	require.NoError(t, err)
	assert.True(t, a.Connected())

	// Kill relay.
	stop()
	require.Eventually(t, func() bool { return !a.Connected() }, 10*time.Second, 50*time.Millisecond)

	// Reply should return ErrDisconnected immediately, not hang.
	done := make(chan error, 1)
	go func() { done <- a.Reply("m-1", "hello", true, false, false, "") }()

	select {
	case err := <-done:
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
	a, err := ConnectWithDial(ctx, Config{RelayURL: url, ConvID: "c-1"}, DefaultDial)
	require.NoError(t, err)
	a.OnQuery = func(msg relay.RelayMsg) { queryCh <- msg }

	// Kill and restart relay.
	stop()
	require.Eventually(t, func() bool { return !a.Connected() }, 10*time.Second, 50*time.Millisecond)

	hub2, stop2 := startRelayOnAddr(t, addr)
	defer stop2()

	require.Eventually(t, func() bool { return a.Connected() }, 20*time.Second, 100*time.Millisecond)
	require.Eventually(t, func() bool { return hub2.HasAgent("c-1") }, 5*time.Second, 50*time.Millisecond)

	// Send a query through new relay.
	hub2.InjectQuery("c-1", relay.RelayMsg{
		Type:      "query",
		ConvID:    "c-1",
		MessageID: "m-after",
		Content:   "hello after reconnect",
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
	failDial := func(ctx context.Context, url string) (*websocket.Conn, error) {
		dialTimes = append(dialTimes, time.Now())
		n := dialCount.Add(1)
		if n <= 4 {
			return nil, fmt.Errorf("refused")
		}
		return DefaultDial(ctx, url)
	}

	// First connect with real dial.
	a, err := ConnectWithDial(ctx, Config{RelayURL: url}, DefaultDial)
	require.NoError(t, err)

	// Swap to failing dial and force disconnect.
	a.dial = failDial
	a.closeWS()

	// Wait for reconnect to succeed (after 4 failures).
	require.Eventually(t, func() bool { return a.Connected() }, 30*time.Second, 100*time.Millisecond)
	require.GreaterOrEqual(t, int(dialCount.Load()), 5)

	// Verify backoff: gaps should generally increase.
	if len(dialTimes) >= 3 {
		gap1 := dialTimes[1].Sub(dialTimes[0])
		gap2 := dialTimes[2].Sub(dialTimes[1])
		// Second gap should be roughly >= first (with 200ms tolerance for scheduling).
		assert.GreaterOrEqual(t, gap2+200*time.Millisecond, gap1,
			"backoff should increase: gap1=%v gap2=%v", gap1, gap2)
	}
}

func TestReconnect_ContextCancelStopsLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	addr := freePort(t)
	url := "ws://" + addr + "/ws"
	_, stop := startRelayOnAddr(t, addr)

	a, err := ConnectWithDial(ctx, Config{RelayURL: url}, DefaultDial)
	require.NoError(t, err)

	// Kill relay so agent enters reconnect loop.
	stop()
	require.Eventually(t, func() bool { return !a.Connected() }, 10*time.Second, 50*time.Millisecond)

	// Cancel context — reconnect loop should exit.
	cancel()

	select {
	case <-a.Done():
		// Good — loop exited.
	case <-time.After(5 * time.Second):
		t.Fatal("reconnect loop did not exit after context cancel")
	}
}
