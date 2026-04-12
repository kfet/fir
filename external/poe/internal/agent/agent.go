// Package agent implements the agent-side connector that bridges a fir MCP
// session to the relay via websocket. Each fir process spawns poe-bridge in
// agent mode; it connects to the relay, optionally registers a conv_id, and
// translates between relay JSON messages and MCP tools/notifications.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/kfet/fir/external/poe/internal/relay"
	"github.com/kfet/fir/external/poe/internal/router"
)

// Config for agent mode.
type Config struct {
	RelayURL string // ws://host:port
	ConvID   string // optional: auto-register on connect
}

// Agent holds the relay websocket connection and a local router for
// bridging reply tool calls back to the relay.
type Agent struct {
	cfg    Config
	ws     *websocket.Conn
	router *router.Router
	mu     sync.Mutex
	done   chan struct{} // closed when readPump exits (ws dies)

	// OnQuery is called when the relay delivers a query. The agent
	// should forward it to fir via MCP channel notification.
	OnQuery func(msg relay.RelayMsg)

	// OnPending is called when the relay broadcasts a pending conv_id.
	OnPending func(msg relay.RelayMsg)

	// OnReplyError is called when the relay rejects a reply (e.g. unknown message_id).
	OnReplyError func(msg relay.RelayMsg)

	// replyCallbacks maps message_id → channel for reply ack/error.
	replyCallbacks sync.Map // string → chan error

	// oauthCallbacks maps session_id → channel for OAuth callback results.
	oauthCallbacks sync.Map // string → chan relay.RelayMsg

	// registerCallbacks maps conv_id → channel for register responses.
	registerCallbacks sync.Map // string → chan relay.RelayMsg
}

// Connect dials the relay and starts the read pump. If cfg.ConvID is
// set, it auto-registers for that conversation.
func Connect(ctx context.Context, cfg Config) (*Agent, error) {
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}
	ws, _, err := dialer.DialContext(ctx, cfg.RelayURL, nil)
	if err != nil {
		return nil, fmt.Errorf("agent: dial %s: %w", cfg.RelayURL, err)
	}

	a := &Agent{
		cfg:    cfg,
		ws:     ws,
		router: router.New(),
		done:   make(chan struct{}),
	}

	go a.readPump()

	if cfg.ConvID != "" {
		if err := a.Register(cfg.ConvID, false); err != nil {
			ws.Close()
			return nil, fmt.Errorf("agent: auto-register %s: %w", cfg.ConvID, err)
		}
	}

	log.Printf("[agent] connected to %s", cfg.RelayURL)
	return a, nil
}

// Register sends a registration for a conv_id to the relay.
// If claim is true, this is a race gate (no queries delivered).
// If claim is false, this is the real agent taking ownership.
func (a *Agent) Register(convID string, claim bool) error {
	return a.sendJSON(relay.AgentMsg{Type: "register", ConvID: convID, Claim: claim})
}

// RegisterSync sends a registration and waits for the relay's response.
// Returns the relay response (register_ok or register_rejected).
// Times out after 5 seconds.
func (a *Agent) RegisterSync(convID string, claim bool) (relay.RelayMsg, error) {
	ch := make(chan relay.RelayMsg, 1)
	a.registerCallbacks.Store(convID, ch)
	defer a.registerCallbacks.Delete(convID)

	if err := a.sendJSON(relay.AgentMsg{Type: "register", ConvID: convID, Claim: claim}); err != nil {
		return relay.RelayMsg{}, err
	}

	select {
	case msg := <-ch:
		return msg, nil
	case <-time.After(5 * time.Second):
		return relay.RelayMsg{}, fmt.Errorf("register timeout for %s", convID)
	}
}

// Reply sends a reply chunk to the relay and waits for ack.
// Returns nil on success, error on relay rejection (e.g. unknown message_id).
func (a *Agent) Reply(messageID, text string, final bool) error {
	ch := make(chan error, 1)
	a.replyCallbacks.Store(messageID, ch)

	err := a.sendJSON(relay.AgentMsg{
		Type:      "reply",
		MessageID: messageID,
		Text:      text,
		Final:     final,
	})
	if err != nil {
		a.replyCallbacks.Delete(messageID)
		return err
	}

	select {
	case err := <-ch:
		return err
	case <-a.done:
		a.replyCallbacks.Delete(messageID)
		return fmt.Errorf("agent disconnected")
	}
}

// Router returns the agent's local router (for bridging the MCP reply
// tool in v1-compatible mode if needed).
func (a *Agent) Router() *router.Router {
	return a.router
}

// Close shuts down the websocket connection.
func (a *Agent) Close() error {
	return a.ws.Close()
}

func (a *Agent) sendJSON(v any) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return a.ws.WriteMessage(websocket.TextMessage, data)
}

func (a *Agent) readPump() {
	defer a.ws.Close()
	defer close(a.done)

	for {
		_, data, err := a.ws.ReadMessage()
		if err != nil {
			log.Printf("[agent] read error: %v", err)
			return
		}

		var msg relay.RelayMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("[agent] bad msg: %v", err)
			continue
		}

		switch msg.Type {
		case "query":
			log.Printf("[agent] query: conv=%s msg=%s", msg.ConvID, msg.MessageID)
			if a.OnQuery != nil {
				a.OnQuery(msg)
			}
		case "pending":
			log.Printf("[agent] pending: conv=%s", msg.ConvID)
			if a.OnPending != nil {
				go a.OnPending(msg)
			}
		case "register_ok":
			log.Printf("[agent] registered for conv=%s", msg.ConvID)
			if ch, ok := a.registerCallbacks.LoadAndDelete(msg.ConvID); ok {
				ch.(chan relay.RelayMsg) <- msg
			}
		case "register_rejected":
			log.Printf("[agent] register rejected for conv=%s: %s", msg.ConvID, msg.Reason)
			if ch, ok := a.registerCallbacks.LoadAndDelete(msg.ConvID); ok {
				ch.(chan relay.RelayMsg) <- msg
			}
		case "oauth_callback":
			log.Printf("[agent] oauth callback for session=%s", msg.SessionID)
			if ch, ok := a.oauthCallbacks.LoadAndDelete(msg.SessionID); ok {
				ch.(chan relay.RelayMsg) <- msg
			}
		case "reply_error":
			log.Printf("[agent] reply error for msg=%s: %s", msg.MessageID, msg.Reason)
			if ch, ok := a.replyCallbacks.LoadAndDelete(msg.MessageID); ok {
				ch.(chan error) <- fmt.Errorf("%s", msg.Reason)
			}
			if a.OnReplyError != nil {
				a.OnReplyError(msg)
			}
		case "reply_ok":
			if ch, ok := a.replyCallbacks.LoadAndDelete(msg.MessageID); ok {
				ch.(chan error) <- nil
			}
		default:
			log.Printf("[agent] unknown msg type: %s", msg.Type)
		}
	}
}

// ListPending returns pending conv_ids. Currently a stub — the relay
// pushes pending notifications rather than supporting a pull query.
// This is here as a placeholder for the MCP tool interface.
func (a *Agent) ListPending() []string {
	// In the current design, pending notifications are pushed by the
	// relay. An agent tracks them locally if it wants a list.
	return nil
}

// OAuthRegister tells the relay to register an OAuth session and returns
// a channel that will receive the callback result.
func (a *Agent) OAuthRegister(sessionID string) (chan relay.RelayMsg, error) {
	ch := make(chan relay.RelayMsg, 1)
	a.oauthCallbacks.Store(sessionID, ch)
	err := a.sendJSON(relay.AgentMsg{Type: "oauth_register", SessionID: sessionID})
	if err != nil {
		a.oauthCallbacks.Delete(sessionID)
		return nil, err
	}
	return ch, nil
}
