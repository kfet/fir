// Package relay implements the dumb conversation router between Poe and
// N fir agent processes. See RELAY.md for the full design.
package relay

import (
	"encoding/json"
	"fmt"
	"log"
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
	Text  string
	Final bool
}

// --- Hub: the core relay state ---

// Hub manages agent connections, the registration map, and the lobby.
type Hub struct {
	mu            sync.Mutex
	registrations map[string]*registration   // conv_id → registration
	agents        map[*agentConn]bool        // all connected agents
	lobby         map[string]*pendingQuery   // conv_id → waiting query
	pending       map[string]chan ReplyChunk // message_id → reply channel (for active queries)
	oauthAgents   map[string]*agentConn      // oauth session_id → agent that registered it
}

// NewHub creates a ready-to-use Hub.
func NewHub() *Hub {
	return &Hub{
		registrations: make(map[string]*registration),
		agents:        make(map[*agentConn]bool),
		lobby:         make(map[string]*pendingQuery),
		pending:       make(map[string]chan ReplyChunk),
		oauthAgents:   make(map[string]*agentConn),
	}
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
		h.pending[messageID] = replyCh
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
		h.pending[messageID] = replyCh
		h.mu.Unlock()

		log.Printf("[relay] conv %s: queued in lobby", convID)
		h.broadcastPending(convID, userID, content)
	}

	return replyCh, done
}

// HandleReply processes a reply chunk from an agent.
func (h *Hub) HandleReply(messageID, text string, final bool) error {
	h.mu.Lock()
	ch, ok := h.pending[messageID]
	if final && ok {
		delete(h.pending, messageID)
	}
	h.mu.Unlock()

	if !ok {
		return fmt.Errorf("unknown message_id %s", messageID)
	}

	ch <- ReplyChunk{Text: text, Final: final}
	return nil
}

// RemoveAgent removes an agent and all its registrations.
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
			if err := c.hub.HandleReply(msg.MessageID, msg.Text, msg.Final); err != nil {
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
