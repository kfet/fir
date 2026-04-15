// Package agent implements the agent-side connector that bridges a fir MCP
// session to the relay via websocket. Each fir process spawns poe-bridge in
// agent mode; it connects to the relay, optionally registers a conv_id, and
// translates between relay JSON messages and MCP tools/notifications.
//
// The agent maintains a persistent reconnect loop: if the relay websocket
// drops (relay restart, network blip), the agent automatically redials with
// exponential backoff and re-registers its conv_id. The MCP stdio connection
// to fir is unaffected — tool calls simply return transient errors while
// disconnected.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/kfet/fir/external/poe/internal/relay"
)

// Backoff parameters for reconnect.
const (
	backoffMin = 100 * time.Millisecond
	backoffMax = 15 * time.Second
	backoffMul = 2
)

// ErrDisconnected is returned by Reply/Register/sendJSON when the agent
// has no active websocket connection to the relay.
var ErrDisconnected = errors.New("agent: relay disconnected, reconnecting")

// Config for agent mode.
type Config struct {
	RelayURL string // ws://host:port/ws
	ConvID   string // optional: auto-register on connect

	// Callbacks — set before Connect. Safe to leave nil.
	OnConnect          func()
	OnDisconnect       func(err error)
	OnRegisterRejected func(convID, reason string)
}

// DialFunc dials a websocket. Replaceable in tests.
type DialFunc func(ctx context.Context, url string) (*websocket.Conn, error)

// DefaultDial is the production dialer.
func DefaultDial(ctx context.Context, url string) (*websocket.Conn, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	ws, _, err := dialer.DialContext(ctx, url, nil)
	return ws, err
}

// Agent holds the relay websocket connection for bridging MCP tool
// calls back to the relay.
type Agent struct {
	cfg  Config
	dial DialFunc

	// ws and connected are protected by mu.
	mu        sync.Mutex
	ws        *websocket.Conn
	connected atomic.Bool

	// done is closed when the reconnect loop exits (ctx cancelled).
	done chan struct{}

	// wsDone is closed when the current readPump exits, signalling
	// the reconnect loop to redial. Recreated on each new connection.
	wsDone chan struct{}

	// OnQuery is called when the relay delivers a query.
	OnQuery func(msg relay.RelayMsg)

	// OnPending is called when the relay broadcasts a pending conv_id.
	OnPending func(msg relay.RelayMsg)

	// OnReplyError is called when the relay rejects a reply.
	OnReplyError func(msg relay.RelayMsg)

	// OnConnect is called each time the agent (re)connects to the relay.
	// Useful for logging or status updates.
	OnConnect func()

	// OnDisconnect is called each time the ws connection drops.
	OnDisconnect func(err error)

	// OnRegisterRejected is called when re-registration after reconnect
	// is rejected (e.g. another agent claimed the conv during grace).
	OnRegisterRejected func(convID, reason string)

	// replyCallbacks maps message_id → channel for reply ack/error.
	replyCallbacks sync.Map // string → chan error

	// oauthCallbacks maps session_id → channel for OAuth callback results.
	oauthCallbacks sync.Map // string → chan relay.RelayMsg

	// registerCallbacks maps conv_id → channel for register responses.
	registerCallbacks sync.Map // string → chan relay.RelayMsg
}

// Connect dials the relay, starts the reconnect loop, and returns after
// the first successful connection. If the initial dial fails, it retries
// in the background and returns immediately with a disconnected agent.
func Connect(ctx context.Context, cfg Config) (*Agent, error) {
	return ConnectWithDial(ctx, cfg, DefaultDial)
}

// ConnectWithDial is like Connect but accepts a custom dialer (for testing).
func ConnectWithDial(ctx context.Context, cfg Config, dial DialFunc) (*Agent, error) {
	a := &Agent{
		cfg:                cfg,
		dial:               dial,
		done:               make(chan struct{}),
		wsDone:             make(chan struct{}),
		OnConnect:          cfg.OnConnect,
		OnDisconnect:       cfg.OnDisconnect,
		OnRegisterRejected: cfg.OnRegisterRejected,
	}

	// Try first connection synchronously so callers know it worked.
	err := a.dialOnce(ctx)
	if err != nil {
		log.Printf("[agent] initial dial failed: %v (will retry in background)", err)
	}

	// Start reconnect loop in background.
	go a.reconnectLoop(ctx)

	return a, nil
}

// Connected returns true if the agent has an active ws connection.
func (a *Agent) Connected() bool {
	return a.connected.Load()
}

// dialOnce attempts a single ws dial + register. Returns nil on success.
func (a *Agent) dialOnce(ctx context.Context) error {
	ws, err := a.dial(ctx, a.cfg.RelayURL)
	if err != nil {
		return fmt.Errorf("dial %s: %w", a.cfg.RelayURL, err)
	}

	a.mu.Lock()
	a.ws = ws
	a.wsDone = make(chan struct{})
	a.mu.Unlock()
	a.connected.Store(true)

	go a.readPump(ws, a.wsDone)

	// Re-register conv_id on every (re)connect.
	if a.cfg.ConvID != "" {
		if err := a.Register(a.cfg.ConvID, false); err != nil {
			ws.Close()
			a.connected.Store(false)
			return fmt.Errorf("auto-register %s: %w", a.cfg.ConvID, err)
		}
	}

	log.Printf("[agent] connected to %s", a.cfg.RelayURL)
	if a.OnConnect != nil {
		a.OnConnect()
	}
	return nil
}

// reconnectLoop runs until ctx is cancelled. It waits for the current
// ws to die, then redials with backoff.
func (a *Agent) reconnectLoop(ctx context.Context) {
	defer close(a.done)

	backoff := backoffMin

	for {
		// Wait for current connection to drop (or if never connected, proceed immediately).
		a.mu.Lock()
		wsDone := a.wsDone
		a.mu.Unlock()

		select {
		case <-ctx.Done():
			a.closeWS()
			return
		case <-wsDone:
			// ws died — reconnect.
		}

		// Drain any pending reply callbacks with errors.
		a.failPendingCallbacks()

		if a.OnDisconnect != nil {
			a.OnDisconnect(nil)
		}

		// Backoff-retry loop.
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}

			err := a.dialOnce(ctx)
			if err == nil {
				backoff = backoffMin // reset on success
				break
			}

			log.Printf("[agent] reconnect failed: %v (retry in %v)", err, backoff)
			backoff = min(backoff*backoffMul, backoffMax)
		}
	}
}

// failPendingCallbacks sends ErrDisconnected to all waiting reply callbacks.
func (a *Agent) failPendingCallbacks() {
	a.replyCallbacks.Range(func(key, value any) bool {
		ch := value.(chan error)
		select {
		case ch <- ErrDisconnected:
		default:
		}
		a.replyCallbacks.Delete(key)
		return true
	})
}

// Register sends a registration for a conv_id to the relay.
func (a *Agent) Register(convID string, claim bool) error {
	return a.sendJSON(relay.AgentMsg{Type: "register", ConvID: convID, Claim: claim})
}

// RegisterSync sends a registration and waits for the relay's response.
func (a *Agent) RegisterSync(convID string, claim bool) (relay.RelayMsg, error) {
	ch := make(chan relay.RelayMsg, 1)
	a.registerCallbacks.Store(convID, ch)
	defer a.registerCallbacks.Delete(convID)

	if err := a.sendJSON(relay.AgentMsg{Type: "register", ConvID: convID, Claim: claim}); err != nil {
		return relay.RelayMsg{}, err
	}

	a.mu.Lock()
	wsDone := a.wsDone
	a.mu.Unlock()

	select {
	case msg := <-ch:
		return msg, nil
	case <-wsDone:
		return relay.RelayMsg{}, ErrDisconnected
	case <-time.After(5 * time.Second):
		return relay.RelayMsg{}, fmt.Errorf("register timeout for %s", convID)
	}
}

// Reply sends a reply chunk to the relay and waits for ack.
func (a *Agent) Reply(messageID, text string, final, replace, isError bool, errorType string) error {
	if !a.connected.Load() {
		return ErrDisconnected
	}

	ch := make(chan error, 1)
	a.replyCallbacks.Store(messageID, ch)

	err := a.sendJSON(relay.AgentMsg{
		Type:      "reply",
		MessageID: messageID,
		Text:      text,
		Final:     final,
		Replace:   replace,
		IsError:   isError,
		ErrorType: errorType,
	})
	if err != nil {
		a.replyCallbacks.Delete(messageID)
		return err
	}

	a.mu.Lock()
	wsDone := a.wsDone
	a.mu.Unlock()

	select {
	case err := <-ch:
		return err
	case <-wsDone:
		a.replyCallbacks.Delete(messageID)
		return ErrDisconnected
	}
}

// Done returns a channel that's closed when the reconnect loop exits.
func (a *Agent) Done() <-chan struct{} {
	return a.done
}

// Close cancels the agent. Callers should cancel the context passed to Connect instead.
func (a *Agent) Close() error {
	a.closeWS()
	return nil
}

func (a *Agent) closeWS() {
	a.mu.Lock()
	ws := a.ws
	a.ws = nil
	a.mu.Unlock()
	a.connected.Store(false)
	if ws != nil {
		ws.Close()
	}
}

func (a *Agent) sendJSON(v any) error {
	a.mu.Lock()
	ws := a.ws
	a.mu.Unlock()

	if ws == nil {
		return ErrDisconnected
	}

	data, err := json.Marshal(v)
	if err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ws == nil {
		return ErrDisconnected
	}
	return a.ws.WriteMessage(websocket.TextMessage, data)
}

// readPump reads from ws until error, then closes wsDone to signal reconnect.
func (a *Agent) readPump(ws *websocket.Conn, wsDone chan struct{}) {
	defer func() {
		ws.Close()
		a.connected.Store(false)
		close(wsDone)
	}()

	for {
		_, data, err := ws.ReadMessage()
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
			} else if a.OnRegisterRejected != nil {
				a.OnRegisterRejected(msg.ConvID, msg.Reason)
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
