package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestFormatMeta(t *testing.T) {
	tests := []struct {
		name string
		meta map[string]any
		want string
	}{
		{"nil", nil, ""},
		{"empty", map[string]any{}, ""},
		{"user only (excluded)", map[string]any{"user": "bob"}, ""},
		{"single key", map[string]any{"chat_id": "123"}, ` chat_id="123"`},
		{"user excluded, others kept", map[string]any{
			"user": "bob", "chat_id": "123", "message_id": "42",
		}, ` chat_id="123" message_id="42"`},
		{"sorted keys", map[string]any{
			"z": "last", "a": "first", "m": "mid",
		}, ` a="first" m="mid" z="last"`},
		{"non-string value", map[string]any{"ts": 12345}, ` ts="12345"`},
		{"history excluded", map[string]any{
			"source": "web", "history": []any{map[string]any{"role": "user", "content": "hi"}},
		}, ` source="web"`},
		{"user and history both excluded", map[string]any{
			"user": "bob", "history": "big blob", "chat_id": "1",
		}, ` chat_id="1"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatMeta(tt.meta)
			if got != tt.want {
				t.Errorf("formatMeta(%v) = %q, want %q", tt.meta, got, tt.want)
			}
		})
	}
}

// TestWireChannelInjection_HistoryExcludedFromHeader is an e2e test that
// verifies a channel message carrying meta["history"] (channel conversation
// history) is injected correctly: the history blob must NOT appear in
// the formatted message header, but the message content must come through.
func TestWireChannelInjection_HistoryExcludedFromHeader(t *testing.T) {
	mgr := NewManager(nil, false)

	var injectedText string
	var injectedTS int64

	WireChannelInjection(mgr, func(content any, ts int64) {
		injectedText = fmt.Sprint(content)
		injectedTS = ts
	})

	// Simulate a channel message with history in meta.
	history := []map[string]any{
		{"role": "user", "content": "what is 2+2?"},
		{"role": "assistant", "content": "4"},
		{"role": "user", "content": "what about 3+3?"},
		{"role": "assistant", "content": "6"},
	}
	historyJSON, _ := json.Marshal(history)
	var historyAny any
	json.Unmarshal(historyJSON, &historyAny)

	cm := ChannelMessage{
		ServerName: "chat",
		Content:    "<message message_id=\"m-1\" conversation_id=\"c-1\" user_id=\"u-1\">\nand 10+10?\n</message>",
		Meta: map[string]any{
			"user":            "u-1",
			"source":          "web",
			"conversation_id": "c-1",
			"message_id":      "m-1",
			"history":         historyAny,
		},
	}

	// Fire the callback directly.
	fn := mgr.loadOnChannelMessage()
	if fn == nil {
		t.Fatal("OnChannelMessage not wired")
	}
	fn(cm)

	if injectedTS == 0 {
		t.Fatal("message was not injected")
	}

	// Header should contain source, conversation_id, message_id but NOT history.
	if !strings.Contains(injectedText, "Channel message from u-1 via chat") {
		t.Errorf("missing header: %q", injectedText)
	}
	if !strings.Contains(injectedText, `source="web"`) {
		t.Errorf("missing source in meta: %q", injectedText)
	}
	if !strings.Contains(injectedText, `conversation_id="c-1"`) {
		t.Errorf("missing conversation_id in meta: %q", injectedText)
	}
	if strings.Contains(injectedText, "what is 2+2") {
		t.Errorf("history content leaked into injected text: %q", injectedText)
	}
	if strings.Contains(injectedText, `history=`) {
		t.Errorf("history key leaked into header: %q", injectedText)
	}

	// Content should come through.
	if !strings.Contains(injectedText, "and 10+10?") {
		t.Errorf("message content missing: %q", injectedText)
	}
}

func TestWireChannelInjection_HistoryPreambleOnEmptySession(t *testing.T) {
	mgr := NewManager(nil, false)
	var injected []string
	WireChannelInjection(mgr, func(content any, ts int64) {
		injected = append(injected, fmt.Sprint(content))
	}, func() int { return 0 }) // empty session

	fn := mgr.loadOnChannelMessage()
	if fn == nil {
		t.Fatal("onChannelMessage not set")
	}

	fn(ChannelMessage{
		Content:    "and 3+3?",
		ServerName: "chat",
		Meta: map[string]any{
			"source":  "web",
			"history": json.RawMessage(`[{"role":"user","content":"what is 2+2?"},{"role":"assistant","content":"4"},{"role":"user","content":"and 3+3?"}]`),
		},
	})

	if len(injected) != 2 {
		t.Fatalf("expected 2 injections (preamble + message), got %d", len(injected))
	}
	if !strings.Contains(injected[0], "conversation history") {
		t.Errorf("first injection should be history preamble, got: %s", injected[0])
	}
	if !strings.Contains(injected[0], "what is 2+2?") {
		t.Errorf("preamble should contain prior messages, got: %s", injected[0])
	}
	if !strings.Contains(injected[1], "and 3+3?") {
		t.Errorf("second injection should be current message, got: %s", injected[1])
	}
}

func TestWireChannelInjection_NoHistoryOnExistingSession(t *testing.T) {
	mgr := NewManager(nil, false)
	var injected []string
	WireChannelInjection(mgr, func(content any, ts int64) {
		injected = append(injected, fmt.Sprint(content))
	}, func() int { return 5 }) // existing session with messages

	fn := mgr.loadOnChannelMessage()

	fn(ChannelMessage{
		Content:    "hello",
		ServerName: "chat",
		Meta: map[string]any{
			"source":  "web",
			"history": json.RawMessage(`[{"role":"user","content":"old"},{"role":"assistant","content":"reply"},{"role":"user","content":"hello"}]`),
		},
	})

	if len(injected) != 1 {
		t.Fatalf("expected 1 injection (message only, no preamble), got %d", len(injected))
	}
	if strings.Contains(injected[0], "conversation history") {
		t.Errorf("should not inject history on existing session, got: %s", injected[0])
	}
}
