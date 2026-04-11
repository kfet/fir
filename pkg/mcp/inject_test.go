package mcp

import "testing"

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
