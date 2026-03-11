// Ported from: packages/coding-agent/src/modes/rpc/rpc-mode.ts (exportToHtml)
// Upstream hash: 1caadb2e
package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/session/store"
)

// makeMessageEntry builds a SessionEntry of type "message" with the given role and content.
func makeMessageEntry(role string, content any) *store.SessionEntry {
	contentJSON, _ := json.Marshal(content)
	msg, _ := json.Marshal(map[string]any{
		"role":    role,
		"content": json.RawMessage(contentJSON),
	})
	return &store.SessionEntry{
		Type:       "message",
		RawMessage: json.RawMessage(msg),
	}
}

func TestExportToHTML_TempFile(t *testing.T) {
	agentSess, _ := newTestAgentSession(t)
	defer agentSess.Close()

	// Add a couple of messages so there's content to export.
	agentSess.SessionManager.AppendAgentMessage(agent.NewAgentMessage(ai.NewUserMsg("hello", 0)))
	agentSess.SessionManager.AppendAgentMessage(agent.NewAgentMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{ai.NewTextContent("world")},
	})))

	// Empty path → temp file created.
	outPath, err := agentSess.ExportToHTML("")
	if err != nil {
		t.Fatalf("ExportToHTML temp path: %v", err)
	}
	t.Cleanup(func() { os.Remove(outPath) })

	if !strings.HasSuffix(outPath, ".html") {
		t.Errorf("expected .html temp file, got %q", outPath)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("reading temp file: %v", err)
	}
	out := string(data)
	if !strings.HasPrefix(out, "<!doctype html>") {
		t.Error("expected HTML doctype in output")
	}
	if !strings.Contains(out, "hello") {
		t.Error("expected user message 'hello' in output")
	}
	if !strings.Contains(out, "world") {
		t.Error("expected assistant message 'world' in output")
	}
}

func TestExportToHTML_ExplicitPath(t *testing.T) {
	agentSess, _ := newTestAgentSession(t)
	defer agentSess.Close()

	agentSess.SessionManager.AppendAgentMessage(agent.NewAgentMessage(ai.NewUserMsg("question", 0)))

	out := filepath.Join(t.TempDir(), "export.html")
	gotPath, err := agentSess.ExportToHTML(out)
	if err != nil {
		t.Fatalf("ExportToHTML explicit path: %v", err)
	}
	if gotPath != out {
		t.Errorf("expected returned path %q, got %q", out, gotPath)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading exported file: %v", err)
	}
	if !strings.Contains(string(data), "question") {
		t.Error("expected user message 'question' in exported file")
	}
}

// ---------------------------------------------------------------------------
// WriteConversationHTML — moved from pkg/modes/rpc/server_html_export_test.go
// ---------------------------------------------------------------------------

func TestWriteConversationHTML_UserAndAssistantMessages(t *testing.T) {
	entries := []*store.SessionEntry{
		makeMessageEntry("user", "Hello, world!"),
		makeMessageEntry("assistant", "Hi there!"),
	}
	var buf strings.Builder
	if err := WriteConversationHTML(&buf, entries, "sess-1"); err != nil {
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
	entries := []*store.SessionEntry{
		makeMessageEntry("user", "<script>alert('xss')</script>"),
	}
	var buf strings.Builder
	if err := WriteConversationHTML(&buf, entries, "sess-1"); err != nil {
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
	if err := WriteConversationHTML(&buf, nil, `<img src=x onerror=alert(1)>`); err != nil {
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
	entries := []*store.SessionEntry{
		{Type: "compaction", RawMessage: json.RawMessage(`{"role":"user","content":"skip me"}`)},
		{Type: "model_change"},
		makeMessageEntry("user", "keep me"),
	}
	var buf strings.Builder
	if err := WriteConversationHTML(&buf, entries, "sess"); err != nil {
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
	entries := []*store.SessionEntry{
		makeMessageEntry("user", ""),
		makeMessageEntry("assistant", "real content"),
	}
	var buf strings.Builder
	if err := WriteConversationHTML(&buf, entries, "s"); err != nil {
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
	if err := WriteConversationHTML(&buf, nil, "my-session"); err != nil {
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
// ExtractHTMLMessageText — moved from pkg/modes/rpc/server_html_export_test.go
// ---------------------------------------------------------------------------

func TestExtractHTMLMessageText_StringContent(t *testing.T) {
	raw, _ := json.Marshal("hello world")
	got := ExtractHTMLMessageText(raw)
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
	got := ExtractHTMLMessageText(raw)
	if got != "part one part two" {
		t.Errorf("expected 'part one part two', got %q", got)
	}
}

func TestExtractHTMLMessageText_ArrayBlocksEmptyTextSkipped(t *testing.T) {
	raw, _ := json.Marshal([]map[string]string{
		{"type": "text", "text": ""},
		{"type": "text", "text": "only this"},
	})
	got := ExtractHTMLMessageText(raw)
	if got != "only this" {
		t.Errorf("expected 'only this', got %q", got)
	}
}

func TestExtractHTMLMessageText_EmptyRaw(t *testing.T) {
	got := ExtractHTMLMessageText(json.RawMessage(nil))
	if got != "" {
		t.Errorf("expected empty string for nil raw, got %q", got)
	}
	got = ExtractHTMLMessageText(json.RawMessage{})
	if got != "" {
		t.Errorf("expected empty string for empty raw, got %q", got)
	}
}

func TestExtractHTMLMessageText_InvalidJSON(t *testing.T) {
	got := ExtractHTMLMessageText(json.RawMessage(`{not valid`))
	if got != "" {
		t.Errorf("expected empty string for invalid JSON, got %q", got)
	}
}

