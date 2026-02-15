// Ported from: packages/ai/src/providers/transform-messages.ts
// Upstream hash: 1caadb2e
package providers

import (
	"strings"
	"time"

	"github.com/kfet/tau/pkg/ai"
)

// NormalizeToolCallIDFunc normalizes tool call IDs for cross-provider compatibility.
type NormalizeToolCallIDFunc func(id string, model *ai.Model, source *ai.AssistantMessage) string

// TransformMessages normalizes messages for cross-provider compatibility.
// It handles:
//   - Thinking block conversion (keep signatures for same model, convert to text for others)
//   - Tool call ID normalization
//   - Synthetic tool results for orphaned tool calls
//   - Skipping errored/aborted assistant messages
func TransformMessages(messages []ai.Message, model *ai.Model, normalizeToolCallID NormalizeToolCallIDFunc) []ai.Message {
	toolCallIDMap := make(map[string]string)

	// First pass: transform messages
	transformed := make([]ai.Message, 0, len(messages))
	for _, msg := range messages {
		switch {
		case msg.AsUser() != nil:
			transformed = append(transformed, msg)

		case msg.AsToolResult() != nil:
			tr := msg.AsToolResult()
			normalizedID, ok := toolCallIDMap[tr.ToolCallID]
			if ok && normalizedID != tr.ToolCallID {
				tr2 := *tr
				tr2.ToolCallID = normalizedID
				transformed = append(transformed, ai.NewToolResultMsg(tr2))
			} else {
				transformed = append(transformed, msg)
			}

		case msg.AsAssistant() != nil:
			m := msg.AsAssistant()
			isSameModel := m.Provider == model.Provider &&
				m.Api == model.Api &&
				m.Model == model.ID

			newContent := make([]ai.AssistantContent, 0, len(m.Content))
			for _, block := range m.Content {
				switch {
				case block.IsThinking():
					b := block.Thinking
					if isSameModel && b.ThinkingSignature != "" {
						newContent = append(newContent, block)
					} else if b.Thinking == "" || strings.TrimSpace(b.Thinking) == "" {
						// skip empty
					} else if isSameModel {
						newContent = append(newContent, block)
					} else {
						newContent = append(newContent, ai.NewTextContent(b.Thinking))
					}

				case block.IsText():
					if isSameModel {
						newContent = append(newContent, block)
					} else {
						newContent = append(newContent, ai.NewTextContent(block.Text.Text))
					}

				case block.IsToolCall():
					tc := *block.ToolCall
					if !isSameModel && tc.ThoughtSignature != "" {
						tc.ThoughtSignature = ""
					}
					if !isSameModel && normalizeToolCallID != nil {
						normalizedID := normalizeToolCallID(tc.ID, model, m)
						if normalizedID != tc.ID {
							toolCallIDMap[tc.ID] = normalizedID
							tc.ID = normalizedID
						}
					}
					newContent = append(newContent, ai.AssistantContent{ToolCall: &tc})

				default:
					newContent = append(newContent, block)
				}
			}
			m2 := *m
			m2.Content = newContent
			transformed = append(transformed, ai.NewAssistantMsg(m2))

		default:
			transformed = append(transformed, msg)
		}
	}

	// Second pass: insert synthetic tool results for orphaned tool calls
	result := make([]ai.Message, 0, len(transformed))
	var pendingToolCalls []*ai.ToolCall
	existingToolResultIDs := make(map[string]bool)

	insertSyntheticResults := func() {
		for _, tc := range pendingToolCalls {
			if !existingToolResultIDs[tc.ID] {
				result = append(result, ai.NewToolResultMsg(ai.ToolResultMessage{
					Role:       ai.RoleToolResult,
					ToolCallID: tc.ID,
					ToolName:   tc.Name,
					Content: []ai.ToolResultContent{
						{Type: ai.ContentTypeText, Text: "No result provided"},
					},
					IsError:   true,
					Timestamp: time.Now().UnixMilli(),
				}))
			}
		}
		pendingToolCalls = nil
		existingToolResultIDs = make(map[string]bool)
	}

	for _, msg := range transformed {
		switch {
		case msg.AsAssistant() != nil:
			m := msg.AsAssistant()

			if len(pendingToolCalls) > 0 {
				insertSyntheticResults()
			}

			// Skip errored/aborted
			if m.StopReason == ai.StopReasonError || m.StopReason == ai.StopReasonAborted {
				continue
			}

			// Track tool calls
			for i := range m.Content {
				if m.Content[i].IsToolCall() {
					pendingToolCalls = append(pendingToolCalls, m.Content[i].ToolCall)
				}
			}
			if len(pendingToolCalls) > 0 {
				existingToolResultIDs = make(map[string]bool)
			}

			result = append(result, msg)

		case msg.AsToolResult() != nil:
			existingToolResultIDs[msg.AsToolResult().ToolCallID] = true
			result = append(result, msg)

		case msg.AsUser() != nil:
			if len(pendingToolCalls) > 0 {
				insertSyntheticResults()
			}
			result = append(result, msg)

		default:
			result = append(result, msg)
		}
	}

	return result
}
