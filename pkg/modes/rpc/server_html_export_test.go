// Ported from: packages/coding-agent/src/modes/rpc/rpc-mode.ts
// Upstream hash: 1caadb2e
package rpc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/core"
)

// makeMessageEntry builds a SessionEntry of type "message" with the given role and content.
func makeMessageEntry(role string, content any) *core.SessionEntry {
	contentJSON, _ := json.Marshal(content)
	msg, _ := json.Marshal(map[string]any{
		"role":    role,
		"content": json.RawMessage(contentJSON),
	})
	return &core.SessionEntry{
		Type:       "message",
		RawMessage: json.RawMessage(msg),
	}
}

// ---------------------------------------------------------------------------
// writeConversationHTML — direct tests
// ---------------------------------------------------------------------------

func TestWriteConversationHTML_UserAndAssistantMessages(t *testing.T) {
	entries := []*core.SessionEntry{
		makeMessageEntry("user", "Hello, world!"),
		makeMessageEntry("assistant", "Hi there!"),
	}
	var buf strings.Builder
	if err := writeConversationHTML(&buf, entries, "sess-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `class="msg user"`) {
		t.Error("expected user message div")
	}
	if !strings.Contains(out, `class="msg assistant"`) {
		t.Error("expected assistant message div")
	}
	if !strings.Contains(out, "Hello, world!") {
		t.Error("expected user message text")
	}
	if !strings.Contains(out, "Hi there!") {
		t.Error("expected assistant message text")
	}
}

func TestWriteConversationHTML_HTMLEscaping(t *testing.T) {
	entries := []*core.SessionEntry{
		makeMessageEntry("user", "<script>alert('xss')</script>"),
	}
	var buf strings.Builder
	if err := writeConversationHTML(&buf, entries, "sess-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "<script>") {
		t.Error("raw <script> tag must be escaped in output")
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Error("expected HTML-escaped script tag")
	}
}

func TestWriteConversationHTML_SessionIDEscaped(t *testing.T) {
	var buf strings.Builder
	if err := writeConversationHTML(&buf, nil, `<img src=x onerror=alert(1)>`); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "<img") {
		t.Error("raw <img> in session ID must be escaped")
	}
	if !strings.Contains(out, "&lt;img") {
		t.Error("expected HTML-escaped img tag in session ID")
	}
}

func TestWriteConversationHTML_NonMessageEntriesSkipped(t *testing.T) {
	entries := []*core.SessionEntry{
		{Type: "compaction", RawMessage: json.RawMessage(`{"role":"user","content":"skip me"}`)},
		{Type: "model_change"},
		makeMessageEntry("user", "keep me"),
	}
	var buf strings.Builder
	if err := writeConversationHTML(&buf, entries, "sess"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	count := strings.Count(out, `class="msg`)
	if count != 1 {
		t.Errorf("expected 1 message div, got %d", count)
	}
	if !strings.Contains(out, "keep me") {
		t.Error("expected the user message to appear")
	}
}

func TestWriteConversationHTML_EmptyContentSkipped(t *testing.T) {
	// entry with valid JSON but empty content should be skipped
	entries := []*core.SessionEntry{
		makeMessageEntry("user", ""),
		makeMessageEntry("assistant", "real content"),
	}
	var buf strings.Builder
	if err := writeConversationHTML(&buf, entries, "s"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	count := strings.Count(out, `class="msg`)
	if count != 1 {
		t.Errorf("expected 1 message div (empty skipped), got %d", count)
	}
}

func TestWriteConversationHTML_ValidHTML(t *testing.T) {
	var buf strings.Builder
	if err := writeConversationHTML(&buf, nil, "my-session"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "<!doctype html>") {
		t.Error("expected HTML doctype")
	}
	if !strings.Contains(out, "</body></html>") {
		t.Error("expected closing tags")
	}
}

// ---------------------------------------------------------------------------
// extractHTMLMessageText — direct tests
// ---------------------------------------------------------------------------

func TestExtractHTMLMessageText_StringContent(t *testing.T) {
	raw, _ := json.Marshal("hello world")
	got := extractHTMLMessageText(raw)
	if got != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
}

func TestExtractHTMLMessageText_ArrayBlocks(t *testing.T) {
	raw, _ := json.Marshal([]map[string]string{
		{"type": "text", "text": "part one "},
		{"type": "tool_use", "text": "ignored"},
		{"type": "text", "text": "part two"},
	})
	got := extractHTMLMessageText(raw)
	if got != "part one part two" {
		t.Errorf("expected 'part one part two', got %q", got)
	}
}

func TestExtractHTMLMessageText_ArrayBlocksEmptyTextSkipped(t *testing.T) {
	raw, _ := json.Marshal([]map[string]string{
		{"type": "text", "text": ""},
		{"type": "text", "text": "only this"},
	})
	got := extractHTMLMessageText(raw)
	if got != "only this" {
		t.Errorf("expected 'only this', got %q", got)
	}
}

func TestExtractHTMLMessageText_EmptyRaw(t *testing.T) {
	got := extractHTMLMessageText(json.RawMessage(nil))
	if got != "" {
		t.Errorf("expected empty string for nil raw, got %q", got)
	}
	got = extractHTMLMessageText(json.RawMessage{})
	if got != "" {
		t.Errorf("expected empty string for empty raw, got %q", got)
	}
}

func TestExtractHTMLMessageText_InvalidJSON(t *testing.T) {
	got := extractHTMLMessageText(json.RawMessage(`{not valid`))
	if got != "" {
		t.Errorf("expected empty string for invalid JSON, got %q", got)
	}
}
