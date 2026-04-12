package oauthproxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRegisterAndCallback(t *testing.T) {
	p := New("https://relay.example.com")

	url := p.Register("sess-1")
	if url != "https://relay.example.com/oauth/cb/sess-1" {
		t.Errorf("callback URL: %q", url)
	}
	if p.SessionCount() != 1 {
		t.Errorf("sessions: %d", p.SessionCount())
	}

	// Simulate OAuth callback in a goroutine.
	go func() {
		req := httptest.NewRequest("GET", "/oauth/cb/sess-1?code=abc123&state=xyz", nil)
		w := httptest.NewRecorder()
		p.HandleCallback(w, req)
		if w.Code != 200 {
			t.Errorf("callback status: %d", w.Code)
		}
	}()

	result, err := p.Await("sess-1", 5*time.Second)
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	if result.SessionID != "sess-1" {
		t.Errorf("session_id: %q", result.SessionID)
	}
	if result.Params["code"] != "abc123" {
		t.Errorf("code: %q", result.Params["code"])
	}
	if result.Params["state"] != "xyz" {
		t.Errorf("state: %q", result.Params["state"])
	}
}

func TestAwait_UnknownSession(t *testing.T) {
	p := New("https://relay.example.com")
	_, err := p.Await("nonexistent", 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected error for unknown session")
	}
}

func TestAwait_Timeout(t *testing.T) {
	p := New("https://relay.example.com")
	p.Register("sess-timeout")

	_, err := p.Await("sess-timeout", 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	// Session should be cleaned up after timeout.
	if p.SessionCount() != 0 {
		t.Errorf("session not cleaned up: %d", p.SessionCount())
	}
}

func TestCallback_UnknownSession(t *testing.T) {
	p := New("https://relay.example.com")
	req := httptest.NewRequest("GET", "/oauth/cb/unknown?code=x", nil)
	w := httptest.NewRecorder()
	p.HandleCallback(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status: %d, want 404", w.Code)
	}
}

func TestCallback_MissingSessionID(t *testing.T) {
	p := New("https://relay.example.com")
	req := httptest.NewRequest("GET", "/oauth/cb", nil)
	w := httptest.NewRecorder()
	p.HandleCallback(w, req)
	// Path has only 2 parts, should be bad request.
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: %d, want 400", w.Code)
	}
}

func TestCallback_DuplicateIgnored(t *testing.T) {
	p := New("https://relay.example.com")
	p.Register("sess-dup")

	// First callback.
	req1 := httptest.NewRequest("GET", "/oauth/cb/sess-dup?code=first", nil)
	w1 := httptest.NewRecorder()
	p.HandleCallback(w1, req1)

	// Second callback (duplicate) — should not block.
	req2 := httptest.NewRequest("GET", "/oauth/cb/sess-dup?code=second", nil)
	w2 := httptest.NewRecorder()
	p.HandleCallback(w2, req2)

	if w2.Code != 200 {
		t.Errorf("duplicate callback status: %d", w2.Code)
	}

	// Await should get the first one.
	result, err := p.Await("sess-dup", time.Second)
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	if result.Params["code"] != "first" {
		t.Errorf("code: %q, want first", result.Params["code"])
	}
}

func TestCleanupExpired(t *testing.T) {
	p := New("https://relay.example.com")
	p.Register("old")
	p.Register("new")

	// Hack: make "old" session appear old.
	p.mu.Lock()
	p.sessions["old"].createdAt = time.Now().Add(-10 * time.Minute)
	p.mu.Unlock()

	removed := p.CleanupExpired(5 * time.Minute)
	if removed != 1 {
		t.Errorf("removed: %d, want 1", removed)
	}
	if p.SessionCount() != 1 {
		t.Errorf("remaining: %d, want 1", p.SessionCount())
	}
}

func TestUnregister(t *testing.T) {
	p := New("https://relay.example.com")
	p.Register("sess-1")
	p.Unregister("sess-1")
	if p.SessionCount() != 0 {
		t.Errorf("sessions after unregister: %d", p.SessionCount())
	}
}

func TestCallback_MultipleParams(t *testing.T) {
	p := New("https://relay.example.com")
	p.Register("sess-multi")

	go func() {
		req := httptest.NewRequest("GET", "/oauth/cb/sess-multi?code=c&state=s&scope=email+profile&error=none", nil)
		w := httptest.NewRecorder()
		p.HandleCallback(w, req)
	}()

	result, err := p.Await("sess-multi", time.Second)
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	if len(result.Params) != 4 {
		t.Errorf("params count: %d, want 4", len(result.Params))
	}
	if result.Params["scope"] != "email profile" {
		t.Errorf("scope: %q", result.Params["scope"])
	}
}
