package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kfet/fir/external/poe/internal/relay"
)

func startRelay(t *testing.T) (*relay.Hub, string) {
	t.Helper()
	hub := relay.NewHubNoGrace()
	ts := httptest.NewServer(http.HandlerFunc(hub.HandleAgentWS))
	t.Cleanup(func() {
		ts.Close()
		time.Sleep(50 * time.Millisecond)
	})
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	return hub, wsURL
}

func connectCtx(t *testing.T, url string, convID string) (*Agent, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	a, err := Connect(ctx, Config{RelayURL: url, ConvID: convID})
	if err != nil {
		cancel()
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		<-a.Done()
	})
	return a, cancel
}

func TestConnect_AutoRegister(t *testing.T) {
	hub, url := startRelay(t)
	a, _ := connectCtx(t, url, "c-1")
	_ = a

	time.Sleep(100 * time.Millisecond)
	if hub.RegistrationState("c-1") != relay.StateProvisional {
		t.Errorf("state: %q want provisional", hub.RegistrationState("c-1"))
	}
}

func TestAgent_ReceivesQuery(t *testing.T) {
	hub, url := startRelay(t)

	var received relay.RelayMsg
	var wg sync.WaitGroup
	wg.Add(1)

	a, _ := connectCtx(t, url, "c-1")
	a.OnQuery = func(msg relay.RelayMsg) {
		received = msg
		wg.Done()
	}

	time.Sleep(100 * time.Millisecond)
	hub.RouteQuery("c-1", "m-1", "u-1", "hello", nil)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("query not received")
	}

	if received.MessageID != "m-1" || received.Content != "hello" {
		t.Errorf("query: %+v", received)
	}
}

func TestAgent_ReplyRoutesToRelay(t *testing.T) {
	hub, url := startRelay(t)

	a, _ := connectCtx(t, url, "c-1")
	a.OnQuery = func(msg relay.RelayMsg) {}

	time.Sleep(100 * time.Millisecond)
	replyCh, _ := hub.RouteQuery("c-1", "m-1", "u-1", "hi", nil)
	time.Sleep(50 * time.Millisecond)

	if err := a.Reply("m-1", "world", true, false, false, ""); err != nil {
		t.Fatalf("Reply: %v", err)
	}

	select {
	case c := <-replyCh:
		if c.Text != "world" || !c.Final {
			t.Errorf("chunk: %+v", c)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no reply on relay side")
	}
}

func TestAgent_ReceivesPending(t *testing.T) {
	t.Skip("pending broadcast test requires refactor — covered by relay_test.go")
}

func TestTwoAgents_IsolatedRouting(t *testing.T) {
	hub, url := startRelay(t)

	var muA, muB sync.Mutex
	var queryA, queryB relay.RelayMsg

	aA, _ := connectCtx(t, url, "c-a")
	aA.OnQuery = func(msg relay.RelayMsg) {
		muA.Lock()
		queryA = msg
		muA.Unlock()
	}

	aB, _ := connectCtx(t, url, "c-b")
	aB.OnQuery = func(msg relay.RelayMsg) {
		muB.Lock()
		queryB = msg
		muB.Unlock()
	}

	time.Sleep(200 * time.Millisecond)

	replyCh1, _ := hub.RouteQuery("c-a", "m-a", "u-1", "for A", nil)
	replyCh2, _ := hub.RouteQuery("c-b", "m-b", "u-1", "for B", nil)

	time.Sleep(100 * time.Millisecond)

	_ = aA.Reply("m-a", "from A", true, false, false, "")
	_ = aB.Reply("m-b", "from B", true, false, false, "")

	select {
	case c := <-replyCh1:
		if c.Text != "from A" {
			t.Errorf("A reply: %q", c.Text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no reply from A")
	}

	select {
	case c := <-replyCh2:
		if c.Text != "from B" {
			t.Errorf("B reply: %q", c.Text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no reply from B")
	}

	muA.Lock()
	muB.Lock()
	if queryA.Content != "for A" {
		t.Errorf("A got: %q", queryA.Content)
	}
	if queryB.Content != "for B" {
		t.Errorf("B got: %q", queryB.Content)
	}
	muB.Unlock()
	muA.Unlock()
}

func TestAgent_RegisterDynamically(t *testing.T) {
	hub, url := startRelay(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a, err := Connect(ctx, Config{RelayURL: url})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { cancel(); <-a.Done() })

	time.Sleep(100 * time.Millisecond)
	if hub.RegistrationState("c-dyn") != "" {
		t.Fatal("should not be registered yet")
	}

	if err := a.Register("c-dyn", false); err != nil {
		t.Fatalf("Register: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	if hub.RegistrationState("c-dyn") != relay.StateProvisional {
		t.Errorf("state: %q", hub.RegistrationState("c-dyn"))
	}
}

func TestAgent_StreamingReply(t *testing.T) {
	hub, url := startRelay(t)

	a, _ := connectCtx(t, url, "c-1")
	a.OnQuery = func(msg relay.RelayMsg) {}

	time.Sleep(100 * time.Millisecond)
	replyCh, _ := hub.RouteQuery("c-1", "m-1", "u-1", "hi", nil)
	time.Sleep(50 * time.Millisecond)

	_ = a.Reply("m-1", "part1 ", false, false, false, "")
	_ = a.Reply("m-1", "part2", true, false, false, "")

	var texts []string
	for c := range replyCh {
		texts = append(texts, c.Text)
		if c.Final {
			break
		}
	}
	if len(texts) != 2 || texts[0] != "part1 " || texts[1] != "part2" {
		t.Errorf("chunks: %v", texts)
	}
}

func TestAgent_DisconnectCleansUp(t *testing.T) {
	hub, url := startRelay(t)

	ctx, cancel := context.WithCancel(context.Background())
	a, _ := Connect(ctx, Config{RelayURL: url, ConvID: "c-1"})
	time.Sleep(100 * time.Millisecond)

	if hub.AgentCount() != 1 {
		t.Fatalf("agents: %d", hub.AgentCount())
	}

	cancel()
	<-a.Done()
	time.Sleep(200 * time.Millisecond)

	if hub.RegistrationState("c-1") != "" {
		t.Errorf("still registered: %q", hub.RegistrationState("c-1"))
	}
}

func TestOnPending_RegisterSyncDoesNotDeadlock(t *testing.T) {
	hub, url := startRelay(t)

	a, _ := connectCtx(t, url, "")
	time.Sleep(100 * time.Millisecond)

	claimed := make(chan string, 1)
	a.OnPending = func(msg relay.RelayMsg) {
		resp, err := a.RegisterSync(msg.ConvID, true)
		if err != nil {
			t.Errorf("RegisterSync: %v", err)
			return
		}
		claimed <- resp.Type
	}

	hub.RouteQuery("c-pending1", "m-1", "u-1", "hello", nil)

	select {
	case typ := <-claimed:
		if typ != "register_ok" {
			t.Errorf("expected register_ok, got %s", typ)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("OnPending + RegisterSync deadlocked (timed out)")
	}

	if hub.RegistrationState("c-pending1") != relay.StateProvisional {
		t.Errorf("state: %q", hub.RegistrationState("c-pending1"))
	}
}

func TestOnPending_SendChannelAfterClaim(t *testing.T) {
	hub, url := startRelay(t)

	a, _ := connectCtx(t, url, "")
	time.Sleep(100 * time.Millisecond)

	done := make(chan error, 1)
	a.OnPending = func(msg relay.RelayMsg) {
		resp, err := a.RegisterSync(msg.ConvID, true)
		if err != nil {
			done <- err
			return
		}
		if resp.Type != "register_ok" {
			done <- fmt.Errorf("register: %s %s", resp.Type, resp.Reason)
			return
		}
		done <- nil
	}

	hub.RouteQuery("c-pending2", "m-2", "u-2", "world", nil)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("OnPending flow: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock: OnPending never completed")
	}
}
