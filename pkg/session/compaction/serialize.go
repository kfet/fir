// Conversation serialization for compaction/summarization.
// Split from utils.go.
package compaction

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/kfet/fir/pkg/ai"
)

// StubOptions controls pointer-stub elision of large/old observations in
// summarizer input. Stubbing applies to tool-result messages only and is
// summarizer-input only — live LLM context is not affected, so prompt-cache
// prefixes are preserved. Phase 2 #4 of the compaction rework.
type StubOptions struct {
	// SizeThreshold: tool-result texts larger than this many bytes are
	// replaced by a stub. Zero or negative disables size-based stubbing.
	SizeThreshold int
	// HeadBytes / TailBytes: characters from the start / end of the
	// elided text to include in the stub. Both default to 128 when zero.
	HeadBytes int
	TailBytes int
	// OldestKeptIndex: tool-result messages at indices strictly less than
	// this in the input slice are stubbed regardless of size. Zero
	// disables age-based stubbing.
	OldestKeptIndex int
}

// DefaultStubOptions are the defaults used by SerializeConversation.
// Tuned conservatively: 4 KiB size cap, 128 head + 128 tail.
var DefaultStubOptions = StubOptions{
	SizeThreshold: 4096,
	HeadBytes:     128,
	TailBytes:     128,
}

// SerializeConversation serializes LLM messages to text for summarization.
// This prevents the model from treating it as a conversation to continue.
// Call ConvertToLLM first to handle custom message types.
//
// Stubbing of large/old tool results uses DefaultStubOptions and assumes
// no entry IDs are available. Use SerializeConversationWithIDs to render
// pointer-stub keys.
func SerializeConversation(messages []ai.Message) string {
	return SerializeConversationWithIDs(messages, nil, DefaultStubOptions)
}

// SerializeConversationWithIDs is like SerializeConversation but each
// message at index i is associated with entryIDs[i] (or "" if unknown).
// Tool results that meet stub criteria are rendered as
// `[entry <id> tool=<name> bytes=<n> head="..." tail="..."]`.
//
// entryIDs may be nil or shorter than messages — missing IDs are treated
// as "".
func SerializeConversationWithIDs(messages []ai.Message, entryIDs []string, opts StubOptions) string {
	if opts.HeadBytes <= 0 {
		opts.HeadBytes = DefaultStubOptions.HeadBytes
	}
	if opts.TailBytes <= 0 {
		opts.TailBytes = DefaultStubOptions.TailBytes
	}

	var parts []string

	for i, msg := range messages {
		switch msg.Role() {
		case "user":
			u := msg.AsUser()
			if u == nil {
				continue
			}
			text := extractTextFromUserContent(u.Content)
			if text != "" {
				parts = append(parts, "[User]: "+text)
			}

		case "assistant":
			a := msg.AsAssistant()
			if a == nil {
				continue
			}
			var textParts, toolCalls []string

			for _, block := range a.Content {
				if block.Text != nil {
					textParts = append(textParts, block.Text.Text)
				} else if block.ToolCall != nil {
					var argParts []string
					for k, v := range block.ToolCall.Arguments {
						argParts = append(argParts, fmt.Sprintf("%s=%s", k, toJSON(v)))
					}
					toolCalls = append(toolCalls, fmt.Sprintf("%s(%s)", block.ToolCall.Name, strings.Join(argParts, ", ")))
				}
			}

			if len(textParts) > 0 {
				parts = append(parts, "[Assistant]: "+strings.Join(textParts, "\n"))
			}
			// NOTE: thinking blocks are intentionally dropped from
			// summarizer input. They bloat the prompt, bias the summary
			// toward the agent's prior CoT, and would otherwise leak
			// reasoning across compactions.
			if len(toolCalls) > 0 {
				parts = append(parts, "[Assistant tool calls]: "+strings.Join(toolCalls, "; "))
			}

		case "toolResult":
			tr := msg.AsToolResult()
			if tr == nil {
				continue
			}
			var texts []string
			for _, c := range tr.Content {
				if c.IsText() {
					texts = append(texts, c.Text)
				}
			}
			text := strings.Join(texts, "")
			if text == "" {
				continue
			}

			id := ""
			if i < len(entryIDs) {
				id = entryIDs[i]
			}

			oldEnough := opts.OldestKeptIndex > 0 && i < opts.OldestKeptIndex
			tooLarge := opts.SizeThreshold > 0 && len(text) > opts.SizeThreshold
			if oldEnough || tooLarge {
				parts = append(parts, formatToolResultStub(id, tr.ToolName, text, opts))
			} else {
				parts = append(parts, "[Tool result]: "+text)
			}
		}
	}

	return strings.Join(parts, "\n\n")
}

// formatToolResultStub renders a pointer-stub for an elided tool result.
//
//	[entry <id> tool=<name> bytes=<n> head="..." tail="..."]
//
// id may be empty, in which case the `entry` token is rendered as
// `entry=?` so the stub is still well-formed. head/tail are sliced on
// UTF-8 rune boundaries — a naïve byte slice could land mid-rune and
// produce invalid UTF-8 inside the summarizer prompt.
func formatToolResultStub(id, toolName, text string, opts StubOptions) string {
	head := text
	tail := ""
	if len(text) > opts.HeadBytes+opts.TailBytes {
		head = utf8Prefix(text, opts.HeadBytes)
		tail = utf8Suffix(text, opts.TailBytes)
	}
	idTok := "entry=?"
	if id != "" {
		idTok = "entry " + id
	}
	if toolName == "" {
		toolName = "?"
	}
	return fmt.Sprintf("[%s tool=%s bytes=%d head=%q tail=%q]", idTok, toolName, len(text), head, tail)
}

// utf8Prefix returns the longest valid-UTF-8 prefix of s with byte length
// ≤ n. If a multi-byte rune straddles position n it is dropped entirely.
func utf8Prefix(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if n >= len(s) {
		return s
	}
	// Walk back from n until we hit a rune-start byte.
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// utf8Suffix returns the longest valid-UTF-8 suffix of s with byte length
// ≤ n. If a multi-byte rune straddles position len(s)-n it is dropped.
func utf8Suffix(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if n >= len(s) {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

func extractTextFromUserContent(content any) string {
	switch c := content.(type) {
	case string:
		return c
	default:
		if blocks, ok := c.([]any); ok {
			var texts []string
			for _, block := range blocks {
				if m, ok := block.(map[string]any); ok {
					if m["type"] == "text" {
						if t, ok := m["text"].(string); ok {
							texts = append(texts, t)
						}
					}
				}
			}
			return strings.Join(texts, "")
		}
		return ""
	}
}

func toJSON(v any) string {
	switch val := v.(type) {
	case string:
		return fmt.Sprintf("%q", val)
	default:
		return fmt.Sprintf("%v", val)
	}
}

// SummarizationSystemPrompt is the system prompt for context summarization.
const SummarizationSystemPrompt = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI coding assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary.`
