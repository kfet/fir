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
//
// Deterministic ordering via channels (no sleeps):
//  1. Server signals when it receives the first connection (firstConnReceived)
//  2. Only then does the second goroutine call acquireWebSocket
//  3. Server blocks until we release it (holdConn)
//  4. Both goroutines complete; we assert peak concurrency ≤ 1
func TestAcquireWebSocket_InflightCoalescing(t *testing.T) {
	var (
		connCount          atomic.Int32 // total connections received by server
		activeConns        atomic.Int32 // currently connected clients
		maxConcurrentConns atomic.Int32 // peak concurrent connections
	)

	firstConnReceived := make(chan struct{}, 1) // signalled on first server accept
	holdConn := make(chan struct{})             // closed to release all server connections

	srv := tryNewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		n := connCount.Add(1)
		active := activeConns.Add(1)
		for {
			cur := maxConcurrentConns.Load()
			if active <= cur || maxConcurrentConns.CompareAndSwap(cur, active) {
				break
			}
		}

		// Signal that the first connection has been received.
		if n == 1 {
			select {
			case firstConnReceived <- struct{}{}:
			default:
			}
		}

		// Hold until test releases.
		<-holdConn

		activeConns.Add(-1)
		c.Close(websocket.StatusNormalClosure, "done")
	}))
	defer srv.Close()

	wsURL := "ws" + srv.URL[4:]

	resetWSCache()
	defer resetWSCache()

	sessionID := "coalesce-test-" + t.Name()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	// Goroutine 1: start dialing immediately.
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn, _, release, err := acquireWebSocket(ctx, wsURL, nil, sessionID)
		if err == nil && conn != nil {
			release(false)
		}
	}()

	// Wait until the server has accepted the first connection — the inflight
	// entry is now guaranteed to exist.
	select {
	case <-firstConnReceived:
	case <-ctx.Done():
		t.Fatal("timed out waiting for first connection")
	}

	// Goroutine 2: starts while goroutine 1's dial is in-flight.
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn, _, release, err := acquireWebSocket(ctx, wsURL, nil, sessionID)
		if err == nil && conn != nil {
			release(false)
		}
	}()

	// Release server connections so both goroutines can complete.
	close(holdConn)
	wg.Wait()

	total := connCount.Load()
	peak := maxConcurrentConns.Load()

	if total == 0 {
		t.Fatal("expected at least one connection to be established")
	}
	if peak > 1 {
		t.Errorf("coalescing failed: peak concurrent connections = %d, want ≤ 1", peak)
	}
}

func TestComputeWSContinuationDelta(t *testing.T) {
	cont := &wsContinuation{
		LastBodyJSONNoInput: `{"model":"gpt-5.5","stream":true}`,
		LastInput: []any{
			map[string]any{"type": "message", "role": "user", "content": "hi"},
		},
		LastResponseID: "resp_1",
	}

	// Next request: previous user msg, then the assistant text the server
	// produced (skipped — replayed via previous_response_id), then a follow-up
	// user message which is the only item we should send as the delta.
	body := map[string]any{
		"model":  "gpt-5.5",
		"stream": true,
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "hi"},
			map[string]any{"type": "message", "role": "assistant", "content": "hello"},
			map[string]any{"type": "reasoning", "encrypted_content": "<opaque>"},
			map[string]any{"type": "function_call", "call_id": "c1", "name": "x", "arguments": "{}"},
			map[string]any{"type": "function_call_output", "call_id": "c1", "output": "ok"},
			map[string]any{"type": "message", "role": "user", "content": "follow-up"},
		},
	}
	delta, ok := computeWSContinuationDelta(body, cont)
	if !ok {
		t.Fatalf("expected continuation match")
	}
	if len(delta) != 2 {
		t.Fatalf("expected 2 delta items (function_call_output + new user), got %d", len(delta))
	}
	first := delta[0].(map[string]any)["type"]
	second := delta[1].(map[string]any)["type"]
	if first != "function_call_output" || second != "message" {
		t.Fatalf("unexpected delta items: %+v", delta)
	}

	// Mismatched body shape — different model — should reject continuation.
	body["model"] = "gpt-other"
	if _, ok := computeWSContinuationDelta(body, cont); ok {
		t.Fatalf("expected mismatch on different model")
	}

	// Prefix mismatch — replace user msg in baseline → reject.
	body["model"] = "gpt-5.5"
	body["input"] = []any{map[string]any{"type": "message", "role": "user", "content": "different"}}
	if _, ok := computeWSContinuationDelta(body, cont); ok {
		t.Fatalf("expected mismatch on prefix divergence")
	}

	// Nil continuation → reject.
	if _, ok := computeWSContinuationDelta(body, nil); ok {
		t.Fatalf("expected mismatch with nil continuation")
	}

	// Same as previous (only assistant items, no real delta) → reject so we
	// don't send an empty input.
	body["input"] = []any{
		map[string]any{"type": "message", "role": "user", "content": "hi"},
		map[string]any{"type": "message", "role": "assistant", "content": "hello"},
	}
	if _, ok := computeWSContinuationDelta(body, cont); ok {
		t.Fatalf("expected rejection when delta would be empty")
	}
}
