// Ported from: packages/ai/src/providers/transform-messages.ts
// Upstream hash: 48aa882
package providers

import (
	"strings"

	"github.com/kfet/fir/pkg/ai"
)

const (
	nonVisionUserImagePlaceholder = "(image omitted: model does not support images)"
	nonVisionToolImagePlaceholder = "(tool image omitted: model does not support images)"
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
	// Downgrade unsupported images for non-vision models
	imageAwareMessages := downgradeUnsupportedImages(messages, model)

	toolCallIDMap := make(map[string]string)

	// First pass: transform messages
	transformed := make([]ai.Message, 0, len(imageAwareMessages))
	for _, msg := range imageAwareMessages {
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

			// isSameProvider is true when the stored assistant message used the
			// same wire protocol (API) as the current call, even if the model ID
			// differs (e.g. "claude-opus-4-7" vs "claude-opus-4-7-20250514").
			// For the Anthropic messages API this matters: a thinking block's
			// signature was issued by the same Anthropic back-end, so it must be
			// forwarded verbatim — converting it to plain text would strip the
			// signature and trigger a 400 "cannot be modified" on the next turn.
			isSameProvider := m.Provider == model.Provider && m.Api == model.Api

			newContent := make([]ai.AssistantContent, 0, len(m.Content))
			for _, block := range m.Content {
				switch {
				case block.IsThinking():
					b := block.Thinking
					if isSameModel && b.ThinkingSignature != "" {
						// Same model, has signature → keep verbatim.
						newContent = append(newContent, block)
					} else if isSameProvider && b.ThinkingSignature != "" {
						// Different model ID but same provider/API (e.g. alias vs
						// dated version).  The signature is still valid; preserve the
						// block verbatim so it can be forwarded to the API unchanged.
						newContent = append(newContent, block)
					} else if b.Thinking == "" || strings.TrimSpace(b.Thinking) == "" {
						// No signature and no content — drop.
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
	var pendingTimestamp int64 // timestamp from the assistant message that owns the orphaned tool calls
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
					Timestamp: pendingTimestamp,
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
				pendingTimestamp = m.Timestamp
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

	// If the conversation ends with unresolved tool calls, synthesize results now.
	insertSyntheticResults()

	return result
}

// downgradeUnsupportedImages replaces image blocks with placeholder text for
// models that don't support image input. This consolidates image filtering
// that was previously done in each provider's convertMessages.
func downgradeUnsupportedImages(messages []ai.Message, model *ai.Model) []ai.Message {
	if model.SupportsImages() {
		return messages
	}

	result := make([]ai.Message, 0, len(messages))
	for _, msg := range messages {
		switch {
		case msg.AsUser() != nil:
			u := msg.AsUser()
			if arr, ok := u.Content.([]any); ok {
				if newBlocks, changed := replaceImagesWithPlaceholder(arr, nonVisionUserImagePlaceholder); changed {
					result = append(result, ai.NewUserMsg(newBlocks, u.Timestamp))
					continue
				}
			}
			result = append(result, msg)

		case msg.AsToolResult() != nil:
			tr := msg.AsToolResult()
			newContent := replaceImagesInToolResult(tr.Content, nonVisionToolImagePlaceholder)
			if len(newContent) != len(tr.Content) {
				tr2 := *tr
				tr2.Content = newContent
				result = append(result, ai.NewToolResultMsg(tr2))
			} else {
				result = append(result, msg)
			}

		default:
			result = append(result, msg)
		}
	}
	return result
}

// replaceImagesWithPlaceholder replaces image content blocks with a text placeholder.
// Consecutive images produce only one placeholder to avoid spam.
// Returns (blocks, changed) — if no images found, returns (nil, false).
func replaceImagesWithPlaceholder(blocks []any, placeholder string) ([]any, bool) {
	hasImage := false
	for _, block := range blocks {
		if m, ok := block.(map[string]any); ok && m["type"] == "image" {
			hasImage = true
			break
		}
	}
	if !hasImage {
		return nil, false
	}
	var result []any
	prevWasPlaceholder := false
	for _, block := range blocks {
		// Check if it's an image content block
		if m, ok := block.(map[string]any); ok {
			if m["type"] == "image" {
				if !prevWasPlaceholder {
					result = append(result, map[string]any{
						"type": "text",
						"text": placeholder,
					})
				}
				prevWasPlaceholder = true
				continue
			}
		}
		result = append(result, block)
		prevWasPlaceholder = false
	}
	return result, true
}

// replaceImagesInToolResult replaces image content blocks in tool results with text placeholders.
func replaceImagesInToolResult(content []ai.ToolResultContent, placeholder string) []ai.ToolResultContent {
	var result []ai.ToolResultContent
	prevWasPlaceholder := false
	for _, block := range content {
		if block.IsImage() {
			if !prevWasPlaceholder {
				result = append(result, ai.ToolResultContent{
					Type: ai.ContentTypeText,
					Text: placeholder,
				})
			}
			prevWasPlaceholder = true
			continue
		}
		result = append(result, block)
		prevWasPlaceholder = false
	}
	return result
}
