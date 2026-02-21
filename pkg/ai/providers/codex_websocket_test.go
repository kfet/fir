package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

func TestResetWSCache(t *testing.T) {
	// Verify resetWSCache doesn't panic on empty cache.
	resetWSCache()

	// Add a nil-conn entry and let resetWSCache handle it (guards against
	// calling conn.Close on nil, which would panic).
	wsSessionCacheMu.Lock()
	wsSessionCache["nil-conn-session"] = &cachedWSConn{
		conn: nil,
		busy: false,
	}
	wsSessionCacheMu.Unlock()

	resetWSCache() // must not panic despite nil conn

	wsSessionCacheMu.Lock()
	count := len(wsSessionCache)
	wsSessionCacheMu.Unlock()
	if count != 0 {
		t.Errorf("expected empty cache after reset, got %d entries", count)
	}
}

func TestResolveCodexWebSocketURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
	}{
		{
			name:    "empty base URL uses default with wss",
			baseURL: "",
			want:    "wss://chatgpt.com/backend-api/codex/responses",
		},
		{
			name:    "https to wss",
			baseURL: "https://example.com/backend-api",
			want:    "wss://example.com/backend-api/codex/responses",
		},
		{
			name:    "http to ws",
			baseURL: "http://localhost:8080/backend-api",
			want:    "ws://localhost:8080/backend-api/codex/responses",
		},
		{
			name:    "https URL already has codex path",
			baseURL: "https://example.com/codex/responses",
			want:    "wss://example.com/codex/responses",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveCodexWebSocketURL(tt.baseURL)
			if got != tt.want {
				t.Errorf("resolveCodexWebSocketURL(%q) = %q, want %q", tt.baseURL, got, tt.want)
			}
		})
	}
}

func TestMapCodexEventFromMap_EmptyType(t *testing.T) {
	result := mapCodexEventFromMap(map[string]any{})
	if result != "" {
		t.Errorf("expected empty string for no type, got %q", result)
	}
}

func TestMapCodexEventFromMap_Error(t *testing.T) {
	input := map[string]any{
		"type":    "error",
		"code":    "rate_limit",
		"message": "too many requests",
	}
	result := mapCodexEventFromMap(input)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if parsed["type"] != "error" {
		t.Errorf("expected type=error, got %v", parsed["type"])
	}
	if parsed["message"] != "too many requests" {
		t.Errorf("expected message='too many requests', got %v", parsed["message"])
	}
}

func TestMapCodexEventFromMap_ResponseFailed_WithMessage(t *testing.T) {
	input := map[string]any{
		"type": "response.failed",
		"response": map[string]any{
			"error": map[string]any{
				"message": "model overloaded",
			},
		},
	}
	result := mapCodexEventFromMap(input)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if parsed["type"] != "error" {
		t.Errorf("expected type=error, got %v", parsed["type"])
	}
	if parsed["code"] != "response_failed" {
		t.Errorf("expected code=response_failed, got %v", parsed["code"])
	}
	if parsed["message"] != "model overloaded" {
		t.Errorf("expected message='model overloaded', got %v", parsed["message"])
	}
}

func TestMapCodexEventFromMap_ResponseFailed_NoMessage(t *testing.T) {
	input := map[string]any{
		"type": "response.failed",
	}
	result := mapCodexEventFromMap(input)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if parsed["type"] != "error" {
		t.Errorf("expected type=error, got %v", parsed["type"])
	}
	if parsed["message"] != "Codex response failed" {
		t.Errorf("expected default message, got %v", parsed["message"])
	}
}

func TestMapCodexEventFromMap_ResponseDone(t *testing.T) {
	input := map[string]any{
		"type": "response.done",
		"response": map[string]any{
			"status": "completed",
			"id":     "resp_123",
		},
	}
	result := mapCodexEventFromMap(input)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	// response.done should be remapped to response.completed
	if parsed["type"] != "response.completed" {
		t.Errorf("expected type=response.completed, got %v", parsed["type"])
	}
}

func TestMapCodexEventFromMap_ResponseCompleted(t *testing.T) {
	input := map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"status": "completed",
		},
	}
	result := mapCodexEventFromMap(input)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if parsed["type"] != "response.completed" {
		t.Errorf("expected type=response.completed, got %v", parsed["type"])
	}
}

func TestMapCodexEventFromMap_DefaultPassthrough(t *testing.T) {
	input := map[string]any{
		"type": "response.output_text.delta",
		"delta": map[string]any{
			"text": "hello",
		},
	}
	result := mapCodexEventFromMap(input)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	if parsed["type"] != "response.output_text.delta" {
		t.Errorf("expected passthrough type, got %v", parsed["type"])
	}
}

func TestMapCodexEventFromMap_StatusNormalization(t *testing.T) {
	// Known status should be preserved
	input := map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"status": "completed",
		},
	}
	result := mapCodexEventFromMap(input)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	resp, ok := parsed["response"].(map[string]any)
	if !ok {
		t.Fatal("expected response field")
	}
	if resp["status"] != "completed" {
		t.Errorf("expected status=completed, got %v", resp["status"])
	}

	// Unknown status should be normalized to empty
	input2 := map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"status": "some_unknown_status",
		},
	}
	result2 := mapCodexEventFromMap(input2)
	var parsed2 map[string]any
	if err := json.Unmarshal([]byte(result2), &parsed2); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}
	resp2, ok := parsed2["response"].(map[string]any)
	if !ok {
		t.Fatal("expected response field")
	}
	if resp2["status"] != "" {
		t.Errorf("expected empty status for unknown input, got %v", resp2["status"])
	}
}

// TestAcquireWebSocket_InflightCoalescing verifies that two goroutines racing to
// acquire a WebSocket connection for the same sessionID coalesce: at most one
// dial is in progress at any moment during the inflight window.
func TestAcquireWebSocket_InflightCoalescing(t *testing.T) {
	const delay = 40 * time.Millisecond // server-side hold time ensures goroutines race

	var (
		connCount        atomic.Int32 // total connections received by server
		activeConns      atomic.Int32 // currently connected clients
		maxConcurrentConns atomic.Int32 // peak concurrent connections during dial window
	)

	// Create a WebSocket test server.
	srv := tryNewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		connCount.Add(1)
		active := activeConns.Add(1)
		// Track peak concurrency.
		for {
			cur := maxConcurrentConns.Load()
			if active <= cur || maxConcurrentConns.CompareAndSwap(cur, active) {
				break
			}
		}

		// Hold the connection long enough that concurrent dials overlap.
		time.Sleep(delay)

		activeConns.Add(-1)
		c.Close(websocket.StatusNormalClosure, "done")
	}))
	defer srv.Close()

	// Convert http:// → ws:// for WebSocket dial.
	wsURL := "ws" + srv.URL[4:]

	resetWSCache()
	defer resetWSCache()

	sessionID := "coalesce-test-" + t.Name()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const numGoroutines = 2
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			conn, release, err := acquireWebSocket(ctx, wsURL, nil, sessionID)
			if err == nil && conn != nil {
				release(false)
			}
		}()
	}

	close(start)
	wg.Wait()

	total := connCount.Load()
	peak := maxConcurrentConns.Load()

	if total == 0 {
		t.Fatal("expected at least one connection to be established")
	}
	// The coalescing path ensures at most one connection is in progress at a time
	// during the inflight window. Verify peak concurrent connections never exceeded 1.
	if peak > 1 {
		t.Errorf("coalescing failed: peak concurrent connections = %d, want ≤ 1", peak)
	}
}
