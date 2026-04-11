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

// TestE2E_HistoryReplayOnFirstQuery verifies that when a Poe query carries
// conversation history (multiple messages in query[]), the relay forwards
// the full query JSON to the agent, and the history package correctly
// parses it into a preamble + latest message.
func TestE2E_HistoryReplayOnFirstQuery(t *testing.T) {
	hub := relay.NewHub()

	// Poe handler that passes full queryJSON through the relay.
	poeHandler := &poe.Handler{
		AccessKey: "test-key",
		OnQuery: func(ctx context.Context, q *poe.QueryRequest, sse *poe.SSEWriter) error {
			userText := ""
			if len(q.Query) > 0 {
				userText = q.Query[len(q.Query)-1].Content
			}
			queryJSON, _ := json.Marshal(q.Query)
			replyCh, _ := hub.RouteQuery(q.ConversationID, q.MessageID, q.UserID, userText, queryJSON)
			for c := range replyCh {
				if c.Text != "" {
					_ = sse.WriteEvent("text", map[string]any{"text": c.Text})
				}
				if c.Final {
					return sse.WriteEvent("done", map[string]any{})
				}
			}
			return nil
		},
	}

	poeSrv := httptest.NewServer(poeHandler)
	defer poeSrv.Close()

	agentSrv := httptest.NewServer(http.HandlerFunc(hub.HandleAgentWS))
	defer agentSrv.Close()

	// Connect agent.
	wsURL := "ws" + strings.TrimPrefix(agentSrv.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	queryCh := make(chan relay.RelayMsg, 1)
	go func() {
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var msg relay.RelayMsg
			json.Unmarshal(data, &msg)
			if msg.Type == "query" {
				queryCh <- msg
			}
		}
	}()

	// Register.
	regMsg, _ := json.Marshal(relay.AgentMsg{Type: "register", ConvID: "c-hist"})
	ws.WriteMessage(websocket.TextMessage, regMsg)
	time.Sleep(100 * time.Millisecond)

	// POST a query with 3 prior messages + 1 new.
	go func() {
		body := `{
			"version":"1.0","type":"query",
			"query":[
				{"role":"user","content":"what is 2+2?","message_id":"m-1"},
				{"role":"bot","content":"4","message_id":"m-2"},
				{"role":"user","content":"what about 3+3?","message_id":"m-3"},
				{"role":"bot","content":"6","message_id":"m-4"},
				{"role":"user","content":"and 10+10?","message_id":"m-5"}
			],
			"user_id":"u-hist","conversation_id":"c-hist","message_id":"m-hist"
		}`
		req, _ := http.NewRequest(http.MethodPost, poeSrv.URL, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-key")
		req.Header.Set("Content-Type", "application/json")
		resp, _ := http.DefaultClient.Do(req)
		if resp != nil {
			io.ReadAll(resp.Body)
			resp.Body.Close()
		}
	}()

	// Wait for query at agent.
	var q relay.RelayMsg
	select {
	case q = <-queryCh:
	case <-time.After(5 * time.Second):
		t.Fatal("agent never received query")
	}

	// Verify the relay forwarded the full query JSON.
	if len(q.Query) == 0 {
		t.Fatal("query JSON not forwarded to agent")
	}

	// Parse the raw query JSON and verify all messages came through.
	var msgs []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(q.Query, &msgs); err != nil {
		t.Fatalf("unmarshal query: %v", err)
	}
	if len(msgs) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(msgs))
	}
	if msgs[0].Content != "what is 2+2?" {
		t.Errorf("msg[0]: %q", msgs[0].Content)
	}
	if msgs[1].Content != "4" || msgs[1].Role != "bot" {
		t.Errorf("msg[1]: %+v", msgs[1])
	}
	if msgs[4].Content != "and 10+10?" {
		t.Errorf("msg[4] (latest): %q", msgs[4].Content)
	}
	// Verify latest user message matches msg.Content.
	if q.Content != "and 10+10?" {
		t.Errorf("msg.Content: got %q, want %q", q.Content, "and 10+10?")
	}

	// Reply to unblock the HTTP handler.
	replyMsg, _ := json.Marshal(relay.AgentMsg{Type: "reply", MessageID: "m-hist", Text: "20", Final: true})
	ws.WriteMessage(websocket.TextMessage, replyMsg)
}
