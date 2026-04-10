package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// testEnv holds a shared httptest server and hub for a single test.
type testEnv struct {
	hub *Hub
	ts  *httptest.Server
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	hub := NewHub()
	ts := httptest.NewServer(http.HandlerFunc(hub.HandleAgentWS))
	t.Cleanup(func() {
		// Close all agent conns by shutting the server; give goroutines time to drain.
		ts.Close()
		time.Sleep(100 * time.Millisecond)
	})
	return &testEnv{hub: hub, ts: ts}
}

func (e *testEnv) connect(t *testing.T) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(e.ts.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { ws.Close() })
	// Wait for agent to appear.
	deadline := time.Now().Add(2 * time.Second)
	prev := e.hub.AgentCount()
	for time.Now().Before(deadline) {
		if e.hub.AgentCount() > prev {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return ws
}

func sendJSON(t *testing.T, ws *websocket.Conn, v any) {
	t.Helper()
	data, _ := json.Marshal(v)
	if err := ws.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("send: %v", err)
	}
}

func readMsg(t *testing.T, ws *websocket.Conn) RelayMsg {
	t.Helper()
	ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var msg RelayMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal: %v (data=%s)", err, data)
	}
	return msg
}

func TestRegister_OK(t *testing.T) {
	env := newTestEnv(t)
	ws := env.connect(t)

	sendJSON(t, ws, AgentMsg{Type: "register", ConvID: "c-1"})
	resp := readMsg(t, ws)

	if resp.Type != "register_ok" {
		t.Errorf("type: got %q want register_ok", resp.Type)
	}
	if env.hub.RegistrationState("c-1") != StateProvisional {
		t.Errorf("state: got %q want provisional", env.hub.RegistrationState("c-1"))
	}
}

func TestRegister_OverrideProvisional(t *testing.T) {
	env := newTestEnv(t)
	ws1 := env.connect(t)

	sendJSON(t, ws1, AgentMsg{Type: "register", ConvID: "c-1"})
	readMsg(t, ws1) // register_ok

	ws2 := env.connect(t)
	sendJSON(t, ws2, AgentMsg{Type: "register", ConvID: "c-1"})
	resp := readMsg(t, ws2)

	if resp.Type != "register_ok" {
		t.Errorf("type: got %q want register_ok", resp.Type)
	}
}

func TestRegister_RejectActive(t *testing.T) {
	env := newTestEnv(t)
	ws1 := env.connect(t)

	sendJSON(t, ws1, AgentMsg{Type: "register", ConvID: "c-1"})
	readMsg(t, ws1) // register_ok

	// Activate by routing a query and replying.
	replyCh, _ := env.hub.RouteQuery("c-1", "m-1", "u-1", "hi", nil)
	readMsg(t, ws1) // query

	sendJSON(t, ws1, AgentMsg{Type: "reply", MessageID: "m-1", Text: "hello", Final: true})
	for c := range replyCh {
		if c.Final {
			break
		}
	}
	time.Sleep(50 * time.Millisecond)

	if env.hub.RegistrationState("c-1") != StateActive {
		t.Fatalf("state: got %q want active", env.hub.RegistrationState("c-1"))
	}

	ws2 := env.connect(t)
	sendJSON(t, ws2, AgentMsg{Type: "register", ConvID: "c-1"})
	resp := readMsg(t, ws2)

	if resp.Type != "register_rejected" {
		t.Errorf("type: got %q want register_rejected", resp.Type)
	}
}

func TestRouteQuery_Registered(t *testing.T) {
	env := newTestEnv(t)
	ws := env.connect(t)

	sendJSON(t, ws, AgentMsg{Type: "register", ConvID: "c-1"})
	readMsg(t, ws)

	replyCh, _ := env.hub.RouteQuery("c-1", "m-1", "u-1", "hello", nil)

	qmsg := readMsg(t, ws)
	if qmsg.Type != "query" || qmsg.MessageID != "m-1" || qmsg.Content != "hello" {
		t.Errorf("query: %+v", qmsg)
	}

	sendJSON(t, ws, AgentMsg{Type: "reply", MessageID: "m-1", Text: "world", Final: true})

	select {
	case c := <-replyCh:
		if c.Text != "world" || !c.Final {
			t.Errorf("chunk: %+v", c)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no reply")
	}
}

func TestRouteQuery_Unregistered_BroadcastsPending(t *testing.T) {
	env := newTestEnv(t)
	ws := env.connect(t)

	sendJSON(t, ws, AgentMsg{Type: "register", ConvID: "c-other"})
	readMsg(t, ws)

	env.hub.RouteQuery("c-new", "m-new", "u-1", "anyone?", nil)

	msg := readMsg(t, ws)
	if msg.Type != "pending" || msg.ConvID != "c-new" {
		t.Errorf("pending: %+v", msg)
	}
	if env.hub.LobbyCount() != 1 {
		t.Errorf("lobby: %d", env.hub.LobbyCount())
	}
}

func TestRouteQuery_Unregistered_ThenAgentRegisters(t *testing.T) {
	env := newTestEnv(t)
	ws := env.connect(t)

	replyCh, _ := env.hub.RouteQuery("c-new", "m-new", "u-1", "hi", nil)

	pmsg := readMsg(t, ws)
	if pmsg.Type != "pending" {
		t.Fatalf("expected pending, got %q", pmsg.Type)
	}

	sendJSON(t, ws, AgentMsg{Type: "register", ConvID: "c-new"})
	readMsg(t, ws) // register_ok
	qmsg := readMsg(t, ws)
	if qmsg.Type != "query" {
		t.Fatalf("expected query, got %q", qmsg.Type)
	}

	sendJSON(t, ws, AgentMsg{Type: "reply", MessageID: "m-new", Text: "got it", Final: true})

	select {
	case c := <-replyCh:
		if c.Text != "got it" || !c.Final {
			t.Errorf("chunk: %+v", c)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no reply")
	}
}

func TestAgentDisconnect_FreesRegistrations(t *testing.T) {
	env := newTestEnv(t)
	ws := env.connect(t)

	sendJSON(t, ws, AgentMsg{Type: "register", ConvID: "c-1"})
	readMsg(t, ws)

	ws.Close()
	time.Sleep(200 * time.Millisecond)

	if env.hub.RegistrationState("c-1") != "" {
		t.Errorf("still registered: %q", env.hub.RegistrationState("c-1"))
	}
}

func TestMultiAgent_Routing(t *testing.T) {
	env := newTestEnv(t)

	wsA := env.connect(t)
	sendJSON(t, wsA, AgentMsg{Type: "register", ConvID: "c-alpha"})
	readMsg(t, wsA)

	wsB := env.connect(t)
	sendJSON(t, wsB, AgentMsg{Type: "register", ConvID: "c-beta"})
	readMsg(t, wsB)

	replyA, _ := env.hub.RouteQuery("c-alpha", "m-a", "u-1", "to alpha", nil)
	qA := readMsg(t, wsA)
	if qA.ConvID != "c-alpha" {
		t.Errorf("A got: %s", qA.ConvID)
	}

	replyB, _ := env.hub.RouteQuery("c-beta", "m-b", "u-1", "to beta", nil)
	qB := readMsg(t, wsB)
	if qB.ConvID != "c-beta" {
		t.Errorf("B got: %s", qB.ConvID)
	}

	sendJSON(t, wsA, AgentMsg{Type: "reply", MessageID: "m-a", Text: "from alpha", Final: true})
	sendJSON(t, wsB, AgentMsg{Type: "reply", MessageID: "m-b", Text: "from beta", Final: true})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		c := <-replyA
		if c.Text != "from alpha" {
			t.Errorf("alpha: %q", c.Text)
		}
	}()
	go func() {
		defer wg.Done()
		c := <-replyB
		if c.Text != "from beta" {
			t.Errorf("beta: %q", c.Text)
		}
	}()
	wg.Wait()
}

func TestRegister_SameAgentTwice(t *testing.T) {
	env := newTestEnv(t)
	ws := env.connect(t)

	sendJSON(t, ws, AgentMsg{Type: "register", ConvID: "c-1"})
	r1 := readMsg(t, ws)
	if r1.Type != "register_ok" {
		t.Errorf("first: %q", r1.Type)
	}

	// Same agent re-registers same conv — should succeed (override self, still provisional).
	sendJSON(t, ws, AgentMsg{Type: "register", ConvID: "c-1"})
	r2 := readMsg(t, ws)
	if r2.Type != "register_ok" {
		t.Errorf("second: %q", r2.Type)
	}
}

func TestReply_UnknownMessageID(t *testing.T) {
	env := newTestEnv(t)
	ws := env.connect(t)

	sendJSON(t, ws, AgentMsg{Type: "register", ConvID: "c-1"})
	readMsg(t, ws)

	// Reply for a message_id that was never routed — should not panic.
	sendJSON(t, ws, AgentMsg{Type: "reply", MessageID: "m-nonexistent", Text: "hi", Final: true})
	// No crash = pass. Give time for the relay to process.
	time.Sleep(100 * time.Millisecond)
}

func TestMultipleQueriesSameConv(t *testing.T) {
	env := newTestEnv(t)
	ws := env.connect(t)

	sendJSON(t, ws, AgentMsg{Type: "register", ConvID: "c-1"})
	readMsg(t, ws)

	// First query.
	reply1, _ := env.hub.RouteQuery("c-1", "m-1", "u-1", "first", nil)
	q1 := readMsg(t, ws)
	if q1.MessageID != "m-1" {
		t.Errorf("q1: %s", q1.MessageID)
	}
	sendJSON(t, ws, AgentMsg{Type: "reply", MessageID: "m-1", Text: "r1", Final: true})
	c1 := <-reply1
	if c1.Text != "r1" {
		t.Errorf("reply1: %q", c1.Text)
	}

	// Second query on same conv.
	reply2, _ := env.hub.RouteQuery("c-1", "m-2", "u-1", "second", nil)
	q2 := readMsg(t, ws)
	if q2.MessageID != "m-2" {
		t.Errorf("q2: %s", q2.MessageID)
	}
	sendJSON(t, ws, AgentMsg{Type: "reply", MessageID: "m-2", Text: "r2", Final: true})
	c2 := <-reply2
	if c2.Text != "r2" {
		t.Errorf("reply2: %q", c2.Text)
	}
}

func TestStreamingReply_MultipleChunks(t *testing.T) {
	env := newTestEnv(t)
	ws := env.connect(t)

	sendJSON(t, ws, AgentMsg{Type: "register", ConvID: "c-1"})
	readMsg(t, ws)

	replyCh, _ := env.hub.RouteQuery("c-1", "m-1", "u-1", "hi", nil)
	readMsg(t, ws) // query

	// Stream 3 chunks then final.
	sendJSON(t, ws, AgentMsg{Type: "reply", MessageID: "m-1", Text: "a"})
	sendJSON(t, ws, AgentMsg{Type: "reply", MessageID: "m-1", Text: "b"})
	sendJSON(t, ws, AgentMsg{Type: "reply", MessageID: "m-1", Text: "c", Final: true})

	var texts []string
	for c := range replyCh {
		texts = append(texts, c.Text)
		if c.Final {
			break
		}
	}
	if len(texts) != 3 || texts[0] != "a" || texts[1] != "b" || texts[2] != "c" {
		t.Errorf("chunks: %v", texts)
	}
}
