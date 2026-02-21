// Ported from: packages/coding-agent/src/modes/rpc/rpc-mode.ts
// Upstream hash: 1caadb2e
package rpc

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"strings"

	"github.com/kfet/fir/pkg/core"
)

// writeConversationHTML writes the conversation history as a minimal HTML document.
func writeConversationHTML(w io.Writer, entries []*core.SessionEntry, sessionID string) error {
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

		text := extractHTMLMessageText(msg.Content)
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

// extractHTMLMessageText extracts plain text from a message content value.
func extractHTMLMessageText(raw json.RawMessage) string {
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
