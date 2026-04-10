package relay_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/kfet/fir/external/poe/internal/poe"
	"github.com/kfet/fir/external/poe/internal/relay"
)

// TestE2E_PoeQueryThroughRelayToAgent is a full end-to-end test:
// 1. Start a relay hub with both a Poe HTTP handler and an agent ws handler
// 2. Connect an agent via ws, register for conv c-e2e
// 3. POST a Poe query to the HTTP handler
// 4. Agent receives the query, replies with chunks
// 5. Assert the SSE response contains meta → text chunks → done
func TestE2E_PoeQueryThroughRelayToAgent(t *testing.T) {
	hub := relay.NewHub()

	// Poe HTTP handler — uses the relay's RouteQuery to bridge.
	poeHandler := &poe.Handler{
		AccessKey: "test-key",
		OnQuery: func(ctx context.Context, q *poe.QueryRequest, sse *poe.SSEWriter) error {
			replyCh, _ := hub.RouteQuery(q.ConversationID, q.MessageID, q.UserID, q.Query[len(q.Query)-1].Content, nil)
			for c := range replyCh {
				if c.Text != "" {
					if err := sse.WriteEvent("text", map[string]any{"text": c.Text}); err != nil {
						return err
					}
				}
				if c.Final {
					return sse.WriteEvent("done", map[string]any{})
				}
			}
			return nil
		},
	}

	// Two HTTP servers: one for Poe, one for agent ws.
	poeSrv := httptest.NewServer(poeHandler)
	defer poeSrv.Close()

	agentSrv := httptest.NewServer(http.HandlerFunc(hub.HandleAgentWS))
	defer agentSrv.Close()

	// Connect agent and register.
	wsURL := "ws" + strings.TrimPrefix(agentSrv.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	// Start agent read pump in background.
	queryCh := make(chan relay.RelayMsg, 1)
	go func() {
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var msg relay.RelayMsg
			json.Unmarshal(data, &msg)
			switch msg.Type {
			case "query":
				queryCh <- msg
			}
		}
	}()

	// Register for c-e2e.
	regMsg, _ := json.Marshal(relay.AgentMsg{Type: "register", ConvID: "c-e2e"})
	ws.WriteMessage(websocket.TextMessage, regMsg)
	time.Sleep(100 * time.Millisecond)

	// POST Poe query in a goroutine (it blocks on SSE).
	type result struct {
		body string
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		body := `{"version":"1.0","type":"query","query":[{"role":"user","content":"e2e test","message_id":"m-in"}],"user_id":"u-e2e","conversation_id":"c-e2e","message_id":"m-e2e"}`
		req, _ := http.NewRequest(http.MethodPost, poeSrv.URL, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-key")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			resultCh <- result{err: err}
			return
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		resultCh <- result{body: string(out)}
	}()

	// Wait for query to reach agent.
	select {
	case q := <-queryCh:
		if q.MessageID != "m-e2e" {
			t.Errorf("query msg_id: %s", q.MessageID)
		}
		if q.Content != "e2e test" {
			t.Errorf("query content: %s", q.Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("agent never received query")
	}

	// Agent replies with two chunks + final.
	for _, msg := range []relay.AgentMsg{
		{Type: "reply", MessageID: "m-e2e", Text: "part-1 "},
		{Type: "reply", MessageID: "m-e2e", Text: "part-2"},
		{Type: "reply", MessageID: "m-e2e", Text: "", Final: true},
	} {
		data, _ := json.Marshal(msg)
		ws.WriteMessage(websocket.TextMessage, data)
	}

	// Collect SSE response.
	select {
	case r := <-resultCh:
		if r.err != nil {
			t.Fatalf("http: %v", r.err)
		}
		s := r.body
		metaIdx := strings.Index(s, "event: meta")
		p1Idx := strings.Index(s, "part-1")
		p2Idx := strings.Index(s, "part-2")
		doneIdx := strings.Index(s, "event: done")
		if metaIdx < 0 || p1Idx < 0 || p2Idx < 0 || doneIdx < 0 {
			t.Fatalf("missing markers in SSE:\n%s", s)
		}
		if !(metaIdx < p1Idx && p1Idx < p2Idx && p2Idx < doneIdx) {
			t.Errorf("ordering: meta=%d p1=%d p2=%d done=%d", metaIdx, p1Idx, p2Idx, doneIdx)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SSE response timed out")
	}
}
