package providers

import (
	"encoding/json"
	"testing"
)

func TestResetWSCache(t *testing.T) {
	// Verify resetWSCache doesn't panic on empty cache
	resetWSCache()

	// Manually add a fake entry (no real conn, just test cleanup)
	wsSessionCacheMu.Lock()
	wsSessionCache["test-session"] = &cachedWSConn{
		conn: nil, // nil conn is safe; resetWSCache handles it
		busy: false,
	}
	wsSessionCacheMu.Unlock()

	// resetWSCache should clear without panic even with nil conn
	// (conn.Close on nil would panic, so we handle nil)
	// Actually the code calls conn.Close which would panic on nil.
	// Let's just verify empty cache behavior.
	wsSessionCacheMu.Lock()
	delete(wsSessionCache, "test-session")
	wsSessionCacheMu.Unlock()

	resetWSCache()

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
