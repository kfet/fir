// Ported from: packages/coding-agent/src/modes/rpc/rpc-mode.ts (exportToHtml)
// Upstream hash: 1caadb2e
package core

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"os"
	"strings"
)

// ExportToHTML exports the current session branch to an HTML file.
// If path is empty a temp file is created and its path returned.
func (s *AgentSession) ExportToHTML(path string) (string, error) {
	entries := s.SessionManager.GetBranch("")
	sessionID := s.GetSessionStats().SessionID

	var f *os.File
	var err error
	if path == "" {
		f, err = os.CreateTemp("", "fir-session-*.html")
		if err != nil {
			return "", fmt.Errorf("creating export file: %w", err)
		}
		path = f.Name()
	} else {
		f, err = os.Create(path)
		if err != nil {
			return "", fmt.Errorf("creating export file: %w", err)
		}
	}

	if err := WriteConversationHTML(f, entries, sessionID); err != nil {
		f.Close()
		os.Remove(path)
		return "", fmt.Errorf("writing HTML: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("closing export file: %w", err)
	}
	return path, nil
}

// WriteConversationHTML writes the conversation history as a minimal HTML document.
// This is shared between interactive (/export) and RPC (export_html) modes.
func WriteConversationHTML(w io.Writer, entries []*SessionEntry, sessionID string) error {
	fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>fir session %s</title>
<style>
body{font-family:sans-serif;max-width:800px;margin:2rem auto;padding:0 1rem;color:#222}
.msg{margin:1rem 0;padding:.75rem 1rem;border-radius:6px;white-space:pre-wrap;word-break:break-word}
.user{background:#e8f0fe}
.assistant{background:#f4f4f4}
.role{font-weight:bold;font-size:.85rem;margin-bottom:.25rem;text-transform:uppercase;color:#666}
</style>
</head>
<body>
<h1>Session %s</h1>
`, html.EscapeString(sessionID), html.EscapeString(sessionID))

	for _, e := range entries {
		if e.Type != "message" || len(e.RawMessage) == 0 {
			continue
		}
		var msg struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(e.RawMessage, &msg); err != nil {
			continue
		}
		if msg.Role != "user" && msg.Role != "assistant" {
			continue
		}

		text := ExtractHTMLMessageText(msg.Content)
		if text == "" {
			continue
		}

		fmt.Fprintf(w, `<div class="msg %s"><div class="role">%s</div>%s</div>`+"\n",
			html.EscapeString(msg.Role),
			html.EscapeString(msg.Role),
			html.EscapeString(text),
		)
	}

	fmt.Fprintln(w, "</body></html>")
	return nil
}

// ExtractHTMLMessageText extracts plain text from a message content JSON value.
// It handles both string content and arrays of content blocks.
func ExtractHTMLMessageText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// String content
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	// Array of content blocks
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "")
}
