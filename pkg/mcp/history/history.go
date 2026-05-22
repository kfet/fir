// Package history formats channel conversation history for injection into a
// new fir agent session. When an agent picks up an existing conversation,
// a channel can provide prior role/content messages as metadata so the model
// has the prior exchange.
package history

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Message is the minimal role/content shape used by channel history metadata.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// FormatPreamble takes a raw JSON array of role/content messages and formats it as a
// context preamble for fir. It returns the preamble text and the latest
// user message separately. If there are fewer than 2 messages (i.e. no
// prior history), preamble is empty and only the latest message is returned.
//
// The preamble uses a clear format that the model can parse:
//
//	[Prior conversation history]
//	user: ...
//	assistant: ...
//	user: ...
//	assistant: ...
//	[End of history]
func FormatPreamble(queryJSON json.RawMessage) (preamble string, latestUserMsg string) {
	if len(queryJSON) == 0 {
		return "", ""
	}

	var msgs []Message
	if err := json.Unmarshal(queryJSON, &msgs); err != nil {
		return "", ""
	}

	if len(msgs) == 0 {
		return "", ""
	}

	// Latest user message is always the last entry.
	latestUserMsg = msgs[len(msgs)-1].Content

	// No history to replay if there's only one message.
	if len(msgs) <= 1 {
		return "", latestUserMsg
	}

	// Build preamble from all messages except the last (which is the new query).
	var b strings.Builder
	b.WriteString("[Prior conversation history]\n")
	for _, m := range msgs[:len(msgs)-1] {
		role := mapRole(m.Role)
		// Indent multi-line content for clarity.
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", role, content)
	}
	b.WriteString("[End of history]\n")

	return b.String(), latestUserMsg
}

// mapRole normalizes common channel role aliases to human-readable labels.
func mapRole(role string) string {
	switch role {
	case "user", "human":
		return "user"
	case "assistant":
		return "assistant"
	case "system":
		return "system"
	default:
		return role
	}
}
