// Package oauthproxy implements a generic OAuth callback proxy for the relay.
// It holds pending OAuth sessions and forwards callback results to agents
// over the relay websocket. Provider-agnostic — works with any OAuth flow.
package oauthproxy

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// CallbackResult holds the query parameters from an OAuth callback.
type CallbackResult struct {
	SessionID string            `json:"session_id"`
	Params    map[string]string `json:"params"` // code, state, error, etc.
}

// session tracks a pending OAuth flow.
type session struct {
	resultCh  chan CallbackResult
	createdAt time.Time
}

// Proxy manages OAuth callback sessions. It is safe for concurrent use.
type Proxy struct {
	mu       sync.Mutex
	sessions map[string]*session
	baseURL  string // e.g. "https://relay.ts.net"
}

// New creates an OAuth proxy with the given public base URL.
func New(baseURL string) *Proxy {
	return &Proxy{
		sessions: make(map[string]*session),
		baseURL:  strings.TrimRight(baseURL, "/"),
	}
}

// Register creates a new OAuth session and returns the callback URL
// that should be used as the OAuth redirect_uri.
func (p *Proxy) Register(sessionID string) (callbackURL string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sessions[sessionID] = &session{
		resultCh:  make(chan CallbackResult, 1),
		createdAt: time.Now(),
	}
	return fmt.Sprintf("%s/oauth/cb/%s", p.baseURL, sessionID)
}

// Await blocks until the callback for the given session is received,
// the context-derived timeout expires, or the session doesn't exist.
func (p *Proxy) Await(sessionID string, timeout time.Duration) (CallbackResult, error) {
	p.mu.Lock()
	s, ok := p.sessions[sessionID]
	p.mu.Unlock()
	if !ok {
		return CallbackResult{}, fmt.Errorf("oauth: unknown session %q", sessionID)
	}
	select {
	case result := <-s.resultCh:
		return result, nil
	case <-time.After(timeout):
		p.Unregister(sessionID)
		return CallbackResult{}, fmt.Errorf("oauth: session %q timed out", sessionID)
	}
}

// Unregister removes a session.
func (p *Proxy) Unregister(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.sessions, sessionID)
}

// HandleCallback is the HTTP handler for GET /oauth/cb/{session_id}.
// It extracts all query parameters and delivers them to the waiting agent.
func (p *Proxy) HandleCallback(w http.ResponseWriter, r *http.Request) {
	// Extract session_id from path: /oauth/cb/{session_id}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 {
		http.Error(w, "missing session_id", http.StatusBadRequest)
		return
	}
	sessionID := parts[2]

	p.mu.Lock()
	s, ok := p.sessions[sessionID]
	p.mu.Unlock()
	if !ok {
		http.Error(w, "unknown or expired session", http.StatusNotFound)
		return
	}

	// Collect all query params.
	params := make(map[string]string)
	for k, v := range r.URL.Query() {
		if len(v) > 0 {
			params[k] = v[0]
		}
	}

	result := CallbackResult{
		SessionID: sessionID,
		Params:    params,
	}

	select {
	case s.resultCh <- result:
	default:
		// Already delivered (duplicate callback).
	}

	// Show a user-friendly page.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html><html><body>
<h2>✅ Authorization complete</h2>
<p>You can close this tab and return to your chat.</p>
</body></html>`)
}

// CleanupExpired removes sessions older than maxAge.
func (p *Proxy) CleanupExpired(maxAge time.Duration) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for id, s := range p.sessions {
		if s.createdAt.Before(cutoff) {
			delete(p.sessions, id)
			removed++
		}
	}
	return removed
}

// SessionCount returns the number of pending sessions (for testing).
func (p *Proxy) SessionCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sessions)
}
