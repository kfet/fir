// Package poe implements the Poe server-bot protocol: request types, an SSE
// writer that satisfies the protocol's framing rules, and an HTTP handler
// that dispatches inbound POSTs to per-type handlers.
//
// Spec reference: https://creator.poe.com/docs/server-bots/poe-protocol-specification
package poe

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

// maxRequestBytes caps inbound request bodies. Poe queries are bounded by
// the 1000-message context window plus modest metadata, so 4 MiB is more
// than enough headroom and protects against oversize POSTs.
const maxRequestBytes = 4 << 20

// Identifier is a Poe protocol identifier of the form `^[a-z]{1,3}-[a-z0-9=]{32}$`.
// We accept it as a plain string and do not validate the form here — the
// bridge has no reason to refuse a structurally-odd id Poe might mint.
type Identifier = string

// BaseRequest is the common envelope shared by every Poe POST. The `Type`
// field is the discriminator used to dispatch to a typed unmarshal.
type BaseRequest struct {
	Version string `json:"version"`
	Type    string `json:"type"`
}

// ProtocolMessage is one entry in a query's conversation history.
type ProtocolMessage struct {
	Role        string     `json:"role"`
	Content     string     `json:"content"`
	ContentType string     `json:"content_type"`
	Timestamp   int64      `json:"timestamp"`
	MessageID   Identifier `json:"message_id"`
}

// QueryRequest is the body of a `type: query` POST.
type QueryRequest struct {
	BaseRequest
	Query          []ProtocolMessage `json:"query"`
	UserID         Identifier        `json:"user_id"`
	ConversationID Identifier        `json:"conversation_id"`
	MessageID      Identifier        `json:"message_id"`
	Metadata       string            `json:"metadata"`
}

// SettingsRequest is the body of a `type: settings` POST. The protocol
// allows additional fields but the bridge ignores them today.
type SettingsRequest struct {
	BaseRequest
}

// SettingsResponse is the JSON body returned for a settings request.
// Conservative defaults for M2; richer settings come later.
type SettingsResponse struct {
	AllowAttachments    bool   `json:"allow_attachments"`
	IntroductionMessage string `json:"introduction_message,omitempty"`
}

// ReportReactionRequest matches `type: report_reaction`.
type ReportReactionRequest struct {
	BaseRequest
	MessageID      Identifier `json:"message_id"`
	UserID         Identifier `json:"user_id"`
	ConversationID Identifier `json:"conversation_id"`
	Reaction       string     `json:"reaction"`
}

// ReportErrorRequest matches `type: report_error`.
type ReportErrorRequest struct {
	BaseRequest
	Message  string         `json:"message"`
	Metadata map[string]any `json:"metadata"`
}

// --- SSE writer ----------------------------------------------------------

// SSEWriter writes server-sent events to an http.ResponseWriter. Construct
// one with NewSSEWriter, then call WriteEvent for each event. The first
// WriteEvent call sets the response headers; callers must therefore not
// touch w.Header() after constructing an SSEWriter.
type SSEWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

// ErrFlushUnsupported is returned by NewSSEWriter when the underlying
// ResponseWriter does not implement http.Flusher (which would make
// streaming impossible).
var ErrFlushUnsupported = errors.New("poe: response writer does not support flushing")

// NewSSEWriter prepares w for SSE output. Sets Content-Type, Cache-Control
// and Connection headers. Returns ErrFlushUnsupported if w cannot be
// flushed (in which case the caller should respond with 500).
func NewSSEWriter(w http.ResponseWriter) (*SSEWriter, error) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, ErrFlushUnsupported
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // disable proxy buffering if any
	return &SSEWriter{w: w, f: f}, nil
}

// WriteEvent serialises data as JSON and writes one SSE event with the
// given name. Flushes after writing so the client receives the event
// immediately.
func (s *SSEWriter) WriteEvent(name string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("poe sse: marshal %s: %w", name, err)
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, b); err != nil {
		return fmt.Errorf("poe sse: write %s: %w", name, err)
	}
	s.f.Flush()
	return nil
}

// --- HTTP handler --------------------------------------------------------

// Handler is the http.Handler for the /poe endpoint. It verifies the bearer
// access key, parses the request envelope, dispatches to a typed handler,
// and writes the appropriate response (SSE for query, JSON for settings,
// 200 for report_*, 501 for unknown types).
type Handler struct {
	// AccessKey is the bearer secret configured in the Poe bot dashboard.
	// If empty, authentication is skipped (dev mode only — never deploy).
	AccessKey string

	// BotName is included in the fallback canned reply. Optional.
	BotName string

	// OnQuery, if non-nil, is called with a pre-configured SSEWriter that
	// has already emitted the mandatory `meta` event. The hook owns the
	// rest of the response lifecycle and must emit `done` itself before
	// returning. If OnQuery is nil the handler falls back to the M2
	// canned reply that echoes the request identifiers.
	OnQuery func(ctx context.Context, q *QueryRequest, sse *SSEWriter) error
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.checkAuth(r) {
		log.Printf("[poe] auth failed from %s", r.RemoteAddr)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
	if err != nil {
		log.Printf("[poe] read body error: %v", err)
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	var base BaseRequest
	if err := json.Unmarshal(body, &base); err != nil {
		log.Printf("[poe] bad json: %v", err)
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("[poe] %s request from %s", base.Type, r.RemoteAddr)

	switch base.Type {
	case "query":
		var q QueryRequest
		if err := json.Unmarshal(body, &q); err != nil {
			http.Error(w, "bad query json: "+err.Error(), http.StatusBadRequest)
			return
		}
		h.handleQuery(w, r, &q)
	case "settings":
		h.handleSettings(w)
	case "report_reaction", "report_error", "report_feedback":
		// Acknowledge with 200 OK; M2 doesn't act on these yet.
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "not implemented", http.StatusNotImplemented)
	}
}

// checkAuth returns true if the request carries the expected bearer token.
// Constant-time comparison to prevent timing leaks on the access key.
func (h *Handler) checkAuth(r *http.Request) bool {
	if h.AccessKey == "" {
		return true
	}
	got := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(got, prefix) {
		return false
	}
	gotKey := got[len(prefix):]
	return subtle.ConstantTimeCompare([]byte(gotKey), []byte(h.AccessKey)) == 1
}

// handleQuery emits the mandatory `meta` event immediately (satisfying the
// 5-second first-event rule), then either delegates the rest of the
// response to h.OnQuery (the normal production path) or falls back to a
// hard-coded echo (the M2 stub, retained for smoke testing without a wired
// fir session).
func (h *Handler) handleQuery(w http.ResponseWriter, r *http.Request, q *QueryRequest) {
	log.Printf("[poe] query: user=%s conv=%s msg=%s queries=%d", q.UserID, q.ConversationID, q.MessageID, len(q.Query))
	sse, err := NewSSEWriter(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Meta MUST be first per the spec.
	if err := sse.WriteEvent("meta", map[string]any{
		"content_type": "text/markdown",
	}); err != nil {
		return
	}

	if h.OnQuery != nil {
		if err := h.OnQuery(r.Context(), q, sse); err != nil {
			// Best-effort error event; the stream may already be partly
			// written, so we can't change status.
			_ = sse.WriteEvent("error", map[string]any{
				"allow_retry": false,
				"text":        err.Error(),
			})
			_ = sse.WriteEvent("done", map[string]any{})
		}
		return
	}

	// --- Fallback: canned echo (M2 stub) ---
	bot := h.BotName
	if bot == "" {
		bot = "poe-bridge"
	}
	body := fmt.Sprintf(
		"hello from %s (stub)\n\n- received %d query message(s)\n- user: `%s`\n- conversation: `%s`\n- message: `%s`",
		bot, len(q.Query), q.UserID, q.ConversationID, q.MessageID,
	)
	if err := sse.WriteEvent("text", map[string]any{"text": body}); err != nil {
		return
	}
	_ = sse.WriteEvent("done", map[string]any{})
}

// handleSettings returns the bot's settings as a JSON object. Not SSE.
func (h *Handler) handleSettings(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(SettingsResponse{
		AllowAttachments:    false,
		IntroductionMessage: "",
	})
}
