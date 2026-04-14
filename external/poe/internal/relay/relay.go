// Package relay implements the dumb conversation router between Poe and
// N fir agent processes. See RELAY.md for the full design.
package relay

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Registration states.
const (
	StateProvisional = "provisional"
	StateActive      = "active"
)

// Timeouts.
const (
	PingInterval = 30 * time.Second
	PingTimeout  = 60 * time.Second
)

// --- Messages between relay and agents ---

// AgentMsg is a message from agent → relay.
type AgentMsg struct {
	Type      string `json:"type"`                 // register, reply, oauth_register, oauth_result
	ConvID    string `json:"conv_id,omitempty"`    // for register
	Claim     bool   `json:"claim,omitempty"`      // for register: true = race gate (no queries), false = real agent
	MessageID string `json:"message_id,omitempty"` // for reply
	Text      string `json:"text,omitempty"`       // for reply
	Final     bool   `json:"final,omitempty"`      // for reply
	Replace   bool   `json:"replace,omitempty"`    // for reply: replace_response instead of text
	IsError   bool   `json:"is_error,omitempty"`   // for reply: emit error event
	ErrorType string `json:"error_type,omitempty"` // for reply: error type
	SessionID string `json:"session_id,omitempty"` // for oauth_register
}

// RelayMsg is a message from relay → agent.
type RelayMsg struct {
	Type        string            `json:"type"` // query, pending, register_ok, register_rejected, oauth_callback
	ConvID      string            `json:"conv_id,omitempty"`
	MessageID   string            `json:"message_id,omitempty"`
	UserID      string            `json:"user_id,omitempty"`
	Content     string            `json:"content,omitempty"`
	Query       json.RawMessage   `json:"query,omitempty"`        // raw Poe query array
	Reason      string            `json:"reason,omitempty"`       // for register_rejected
	SessionID   string            `json:"session_id,omitempty"`   // for oauth_callback
	OAuthParams map[string]string `json:"oauth_params,omitempty"` // for oauth_callback
	CallbackURL string            `json:"callback_url,omitempty"` // for oauth_registered
}

// --- Registration map ---

type registration struct {
	conn    *agentConn
	state   string // StateProvisional or StateActive
	claimed bool   // true if this is a claim-only (no queries forwarded)
}

// --- Pending query waiting for an agent ---

type pendingQuery struct {
	convID    string
	messageID string
	userID    string
	content   string
	query     json.RawMessage
	replyCh   chan ReplyChunk // agent sends chunks here
	done      chan struct{}   // closed when query is served or timed out
}

// ReplyChunk is a chunk from an agent for an in-flight query.
type ReplyChunk struct {
	Text      string
	Final     bool
	Replace   bool
	IsError   bool
	ErrorType string
}

// pendingEntry tracks a reply channel and the agent conn serving it.
type pendingEntry struct {
	ch   chan ReplyChunk
	conn *agentConn // nil for lobby-queued (not yet assigned)
}

// --- Hub: the core relay state ---

// Grace period: after relay restart, wait for a quiet window (no new agent
// registrations) before broadcasting lobby items. This lets existing agents
// reconnect and re-register before the catch-all spawns duplicates.
const (
	// GraceQuietWindow is how long the relay waits after the last agent
	// registration before broadcasting unclaimed lobby items.
	GraceQuietWindow = 1 * time.Second

	// GraceHardTimeout is the maximum grace period regardless of activity.
	GraceHardTimeout = 5 * time.Second
)

// Hub manages agent connections, the registration map, and the lobby.
type Hub struct {
	mu            sync.Mutex
	registrations map[string]*registration // conv_id → registration
	agents        map[*agentConn]bool      // all connected agents
	lobby         map[string]*pendingQuery // conv_id → waiting query
	pending       map[string]*pendingEntry // message_id → reply entry (for active queries)
	oauthAgents   map[string]*agentConn    // oauth session_id → agent that registered it
	graceTimer    *time.Timer              // quiet-window timer, fires lobby broadcast
	graceDone     chan struct{}            // closed when grace period ends
}

// NewHub creates a ready-to-use Hub.
func NewHub() *Hub {
	h := &Hub{
		registrations: make(map[string]*registration),
		agents:        make(map[*agentConn]bool),
		lobby:         make(map[string]*pendingQuery),
		pending:       make(map[string]*pendingEntry),
		oauthAgents:   make(map[string]*agentConn),
		graceDone:     make(chan struct{}),
	}
	// Start quiet-window timer. Reset on each agent registration.
	h.graceTimer = time.AfterFunc(GraceQuietWindow, h.endGrace)
	// Hard timeout ensures grace ends even if agents keep reconnecting.
	time.AfterFunc(GraceHardTimeout, h.endGrace)
	return h
}

// endGrace ends the grace period and broadcasts all unclaimed lobby items.
func (h *Hub) endGrace() {
	h.mu.Lock()
	select {
	case <-h.graceDone:
		h.mu.Unlock()
		return // already ended
	default:
		close(h.graceDone)
	}

	// Collect unclaimed lobby items.
	var unclaimed []pendingQuery
	for convID, pq := range h.lobby {
		if _, claimed := h.registrations[convID]; !claimed {
			unclaimed = append(unclaimed, *pq)
		}
	}
	agents := make([]*agentConn, 0, len(h.agents))
	for c := range h.agents {
		agents = append(agents, c)
	}
	h.mu.Unlock()

	if len(unclaimed) == 0 {
		log.Printf("[relay] grace period ended, no unclaimed lobby items")
		return
	}

	log.Printf("[relay] grace period ended, broadcasting %d unclaimed lobby items", len(unclaimed))
	for _, pq := range unclaimed {
		msg := RelayMsg{Type: "pending", ConvID: pq.convID, UserID: pq.userID, Content: pq.content}
		for _, c := range agents {
			_ = c.sendMsg(msg)
		}
	}
}

// resetGraceTimer resets the quiet-window timer. Called on each agent registration.
func (h *Hub) resetGraceTimer() {
	select {
	case <-h.graceDone:
		return // grace already ended
	default:
	}
	h.graceTimer.Reset(GraceQuietWindow)
}

// InGracePeriod returns true if the grace period has not yet ended.
func (h *Hub) InGracePeriod() bool {
	select {
	case <-h.graceDone:
		return false
	default:
		return true
	}
}

// NewHubNoGrace creates a Hub with grace period already expired. For tests.
func NewHubNoGrace() *Hub {
	h := NewHub()
	// Force grace to end immediately.
	h.graceTimer.Stop()
	h.endGrace()
	return h
}

// RegisterOAuth records which agent owns an OAuth session so the relay
// can route the callback result back to the right agent.
func (h *Hub) RegisterOAuth(conn *agentConn, sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.oauthAgents[sessionID] = conn
}

// DeliverOAuthCallback sends an OAuth callback result to the agent that
// registered the session. Returns false if the session is unknown.
func (h *Hub) DeliverOAuthCallback(sessionID string, params map[string]string) bool {
	h.mu.Lock()
	conn, ok := h.oauthAgents[sessionID]
	if ok {
		delete(h.oauthAgents, sessionID)
	}
	h.mu.Unlock()
	if !ok {
		return false
	}
	_ = conn.sendMsg(RelayMsg{
		Type:        "oauth_callback",
		SessionID:   sessionID,
		OAuthParams: params,
	})
	return true
}

// --- Agent connection ---

type agentConn struct {
	ws       *websocket.Conn
	hub      *Hub
	send     chan []byte
	lastSeen time.Time
	mu       sync.Mutex
}

func (c *agentConn) sendMsg(msg RelayMsg) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	select {
	case c.send <- data:
		return nil
	default:
		return fmt.Errorf("agent send buffer full")
	}
}

// --- Hub methods ---

// Register handles an agent's registration for a conv_id.
// claim=true is a race gate: creates provisional but does NOT deliver lobby queries.
// claim=false is a real agent taking ownership: overrides provisional, delivers lobby queries.
func (h *Hub) Register(conn *agentConn, convID string, claim bool) RelayMsg {
	h.mu.Lock()
	defer h.mu.Unlock()

	// An agent registered — reset the grace quiet-window timer.
	h.resetGraceTimer()

	existing, ok := h.registrations[convID]

	if claim {
		// Race gate: first claim wins, rest rejected.
		if ok {
			if existing.state == StateActive {
				return RelayMsg{Type: "register_rejected", ConvID: convID, Reason: "active"}
			}
			// Already claimed (provisional).
			return RelayMsg{Type: "register_rejected", ConvID: convID, Reason: "claimed"}
		}
		// No existing — claim it. No lobby delivery.
		h.registrations[convID] = &registration{conn: conn, state: StateProvisional, claimed: true}
		log.Printf("[relay] conv %s: claimed (provisional, no queries)", convID)
		return RelayMsg{Type: "register_ok", ConvID: convID}
	}

	// Real agent (claim=false): takes ownership, gets queries.
	if ok && existing.state == StateActive {
		return RelayMsg{Type: "register_rejected", ConvID: convID, Reason: "active"}
	}

	// Override provisional (claimed) or fresh registration.
	h.registrations[convID] = &registration{conn: conn, state: StateProvisional}
	if ok {
		log.Printf("[relay] conv %s: override claim → real agent", convID)
	} else {
		log.Printf("[relay] conv %s: registered (real agent)", convID)
	}

	// Deliver lobby queries to the real agent.
	if pq, ok := h.lobby[convID]; ok {
		delete(h.lobby, convID)
		// Update pending entry's conn so RemoveAgent can orphan it on crash.
		if pe, ok := h.pending[pq.messageID]; ok {
			pe.conn = conn
		}
		go h.deliverToAgent(conn, pq)
	}

	return RelayMsg{Type: "register_ok", ConvID: convID}
}

// ActivateOnReply transitions a registration to active on first reply write.
func (h *Hub) ActivateOnReply(convID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if reg, ok := h.registrations[convID]; ok && reg.state == StateProvisional {
		reg.state = StateActive
		log.Printf("[relay] conv %s: activated", convID)
	}
}

// RouteQuery routes a Poe query to the right agent or queues it.
// Returns a channel that will receive reply chunks, and a done channel.
func (h *Hub) RouteQuery(convID, messageID, userID, content string, query json.RawMessage) (chan ReplyChunk, chan struct{}) {
	replyCh := make(chan ReplyChunk, 16)
	done := make(chan struct{})

	h.mu.Lock()
	reg, ok := h.registrations[convID]

	if ok && !reg.claimed {
		// Registered with a real agent — deliver to agent.
		h.pending[messageID] = &pendingEntry{ch: replyCh, conn: reg.conn}
		h.mu.Unlock()

		pq := &pendingQuery{
			convID: convID, messageID: messageID, userID: userID,
			content: content, query: query, replyCh: replyCh, done: done,
		}
		go h.deliverToAgent(reg.conn, pq)
	} else {
		// Not registered — queue in lobby and notify all agents.
		pq := &pendingQuery{
			convID: convID, messageID: messageID, userID: userID,
			content: content, query: query, replyCh: replyCh, done: done,
		}
		h.lobby[convID] = pq
		h.pending[messageID] = &pendingEntry{ch: replyCh} // conn assigned later
		h.mu.Unlock()

		log.Printf("[relay] conv %s: queued in lobby", convID)
		h.broadcastPending(convID, userID, content)
	}

	return replyCh, done
}

// HandleReply processes a reply chunk from an agent.
func (h *Hub) HandleReply(msg AgentMsg) error {
	h.mu.Lock()
	pe, ok := h.pending[msg.MessageID]
	if msg.Final && ok {
		delete(h.pending, msg.MessageID)
	}
	h.mu.Unlock()

	if !ok {
		return fmt.Errorf("unknown message_id %s", msg.MessageID)
	}

	pe.ch <- ReplyChunk{Text: msg.Text, Final: msg.Final, Replace: msg.Replace, IsError: msg.IsError, ErrorType: msg.ErrorType}
	return nil
}

// RemoveAgent removes an agent and all its registrations.
// Any in-flight queries for conversations owned by this agent receive an
// error reply so the Poe SSE handler doesn't hang.
func (h *Hub) RemoveAgent(conn *agentConn) {
	h.mu.Lock()
	delete(h.agents, conn)

	// Find and remove all registrations for this conn.
	freedConvs := []string{}
	for convID, reg := range h.registrations {
		if reg.conn == conn {
			delete(h.registrations, convID)
			freedConvs = append(freedConvs, convID)
		}
	}

	// Find pending reply channels owned by this agent.
	var orphaned []chan ReplyChunk
	for msgID, pe := range h.pending {
		if pe.conn == conn {
			orphaned = append(orphaned, pe.ch)
			delete(h.pending, msgID)
		}
	}

	// Clean up OAuth sessions owned by this agent.
	for sid, ac := range h.oauthAgents {
		if ac == conn {
			delete(h.oauthAgents, sid)
		}
	}
	h.mu.Unlock()

	for _, convID := range freedConvs {
		log.Printf("[relay] conv %s: freed (agent disconnected)", convID)
	}

	// Send error to orphaned reply channels so Poe gets a response.
	for _, ch := range orphaned {
		select {
		case ch <- ReplyChunk{
			Text:    "⚠️ Agent crashed or disconnected. Please try again.",
			Final:   true,
			IsError: true,
		}:
		default:
		}
	}
	if len(orphaned) > 0 {
		log.Printf("[relay] sent crash error for %d orphaned queries", len(orphaned))
	}
}

func (h *Hub) deliverToAgent(conn *agentConn, pq *pendingQuery) {
	msg := RelayMsg{
		Type:      "query",
		ConvID:    pq.convID,
		MessageID: pq.messageID,
		UserID:    pq.userID,
		Content:   pq.content,
		Query:     pq.query,
	}
	if err := conn.sendMsg(msg); err != nil {
		log.Printf("[relay] deliver to agent failed: %v", err)
	}
}

func (h *Hub) broadcastPending(convID, userID, content string) {
	// During grace period, don't broadcast — endGrace() will broadcast
	// all unclaimed lobby items when the quiet window expires.
	if h.InGracePeriod() {
		log.Printf("[relay] grace period active, deferring broadcast for %s", convID)
		return
	}

	h.mu.Lock()
	agents := make([]*agentConn, 0, len(h.agents))
	for c := range h.agents {
		agents = append(agents, c)
	}
	h.mu.Unlock()

	msg := RelayMsg{
		Type:    "pending",
		ConvID:  convID,
		UserID:  userID,
		Content: content,
	}
	for _, c := range agents {
		_ = c.sendMsg(msg)
	}
}

// --- Websocket handler ---

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// HandleAgentWS is the HTTP handler for agent websocket connections.
func (h *Hub) HandleAgentWS(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[relay] ws upgrade: %v", err)
		return
	}

	conn := &agentConn{
		ws:       ws,
		hub:      h,
		send:     make(chan []byte, 64),
		lastSeen: time.Now(),
	}

	h.mu.Lock()
	h.agents[conn] = true
	h.mu.Unlock()

	log.Printf("[relay] agent connected (%d total)", h.AgentCount())

	go conn.writePump()
	conn.readPump() // blocks

	h.RemoveAgent(conn)
	ws.Close()
	log.Printf("[relay] agent disconnected (%d remaining)", h.AgentCount())
}

func (c *agentConn) readPump() {
	c.ws.SetReadDeadline(time.Now().Add(PingTimeout))
	c.ws.SetPongHandler(func(string) error {
		c.mu.Lock()
		c.lastSeen = time.Now()
		c.mu.Unlock()
		c.ws.SetReadDeadline(time.Now().Add(PingTimeout))
		return nil
	})

	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			return
		}

		var msg AgentMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("[relay] bad agent msg: %v", err)
			continue
		}

		switch msg.Type {
		case "register":
			resp := c.hub.Register(c, msg.ConvID, msg.Claim)
			_ = c.sendMsg(resp)

		case "reply":
			// Find which conv_id this message_id belongs to and activate.
			c.hub.mu.Lock()
			for convID, reg := range c.hub.registrations {
				if reg.conn == c {
					c.hub.mu.Unlock()
					c.hub.ActivateOnReply(convID)
					goto handleReply
				}
			}
			c.hub.mu.Unlock()

		handleReply:
			if err := c.hub.HandleReply(msg); err != nil {
				log.Printf("[relay] reply error: %v", err)
				_ = c.sendMsg(RelayMsg{Type: "reply_error", MessageID: msg.MessageID, Reason: err.Error()})
			} else {
				_ = c.sendMsg(RelayMsg{Type: "reply_ok", MessageID: msg.MessageID})
			}

		case "oauth_register":
			if msg.SessionID != "" {
				c.hub.RegisterOAuth(c, msg.SessionID)
				log.Printf("[relay] oauth session %s registered by agent", msg.SessionID)
			}

		default:
			log.Printf("[relay] unknown agent msg type: %s", msg.Type)
		}
	}
}

func (c *agentConn) writePump() {
	ticker := time.NewTicker(PingInterval)
	defer ticker.Stop()

	for {
		select {
		case msg, ok := <-c.send:
			if !ok {
				return
			}
			c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.ws.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}

		case <-ticker.C:
			c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// AgentCount returns the number of connected agents.
func (h *Hub) AgentCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.agents)
}

// RegistrationState returns the state of a conv_id registration, or "" if unregistered.
func (h *Hub) RegistrationState(convID string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if reg, ok := h.registrations[convID]; ok {
		return reg.state
	}
	return ""
}

// LobbyCount returns the number of queued conversations.
func (h *Hub) LobbyCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.lobby)
}

// HasAgent returns true if a conv_id has a registered agent.
func (h *Hub) HasAgent(convID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.registrations[convID]
	return ok
}

// CloseAllAgents forcibly closes all agent websocket connections.
// Used during shutdown to ensure agents detect disconnect promptly.
func (h *Hub) CloseAllAgents() {
	h.mu.Lock()
	agents := make([]*agentConn, 0, len(h.agents))
	for c := range h.agents {
		agents = append(agents, c)
	}
	h.mu.Unlock()
	for _, c := range agents {
		c.ws.Close()
	}
}

// InjectQuery delivers a query message directly to the agent registered
// for convID. Used in tests to simulate relay-originated queries.
func (h *Hub) InjectQuery(convID string, msg RelayMsg) bool {
	h.mu.Lock()
	reg, ok := h.registrations[convID]
	if !ok {
		h.mu.Unlock()
		return false
	}
	conn := reg.conn
	h.mu.Unlock()
	return conn.sendMsg(msg) == nil
}

// StartOnAddr starts the hub's ws handler on a specific address.
// Returns the listener address and an http.Server for shutdown.
func (h *Hub) StartOnAddr(addr string) (*http.Server, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.HandleAgentWS)
	srv := &http.Server{Addr: addr, Handler: mux}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	go srv.Serve(ln)
	return srv, nil
}
