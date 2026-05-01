// Conversation serialization for compaction/summarization.
// Split from utils.go.
package compaction

import (
	"fmt"
	"strings"

	"github.com/kfet/fir/pkg/ai"
)

// SerializeConversation serializes LLM messages to text for summarization.
// This prevents the model from treating it as a conversation to continue.
// Call ConvertToLLM first to handle custom message types.
func SerializeConversation(messages []ai.Message) string {
	var parts []string

	for _, msg := range messages {
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
			if text != "" {
				parts = append(parts, "[Tool result]: "+text)
			}
		}
	}

	return strings.Join(parts, "\n\n")
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
