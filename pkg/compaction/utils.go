// Ported from: packages/coding-agent/src/core/compaction/utils.ts
// Upstream hash: 1caadb2e
package compaction

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
)

// ============================================================================
// File Operation Tracking
// ============================================================================

// FileOperations tracks files read, written, and edited during a conversation.
type FileOperations struct {
	Read    map[string]struct{}
	Written map[string]struct{}
	Edited  map[string]struct{}
}

// NewFileOperations creates an empty FileOperations.
func NewFileOperations() *FileOperations {
	return &FileOperations{
		Read:    make(map[string]struct{}),
		Written: make(map[string]struct{}),
		Edited:  make(map[string]struct{}),
	}
}

// ExtractFileOpsFromMessage extracts file operations from tool calls in an assistant message.
func ExtractFileOpsFromMessage(message agent.AgentMessage, fileOps *FileOperations) {
	if message.Role() != "assistant" {
		return
	}
	assistant := message.Message.AsAssistant()
	if assistant == nil {
		return
	}

	for _, block := range assistant.Content {
		if block.ToolCall == nil {
			continue
		}
		tc := block.ToolCall
		path, ok := tc.Arguments["path"].(string)
		if !ok || path == "" {
			continue
		}

		switch tc.Name {
		case "read":
			fileOps.Read[path] = struct{}{}
		case "write":
			fileOps.Written[path] = struct{}{}
		case "edit":
			fileOps.Edited[path] = struct{}{}
		}
	}
}

// ComputeFileLists returns readFiles (only read, not modified) and modifiedFiles.
func ComputeFileLists(fileOps *FileOperations) (readFiles, modifiedFiles []string) {
	modified := make(map[string]struct{})
	for f := range fileOps.Edited {
		modified[f] = struct{}{}
	}
	for f := range fileOps.Written {
		modified[f] = struct{}{}
	}

	for f := range fileOps.Read {
		if _, isModified := modified[f]; !isModified {
			readFiles = append(readFiles, f)
		}
	}

	for f := range modified {
		modifiedFiles = append(modifiedFiles, f)
	}

	sort.Strings(readFiles)
	sort.Strings(modifiedFiles)
	return
}

// FormatFileOperations formats file operations as XML tags for summary.
func FormatFileOperations(readFiles, modifiedFiles []string) string {
	var sections []string
	if len(readFiles) > 0 {
		sections = append(sections, fmt.Sprintf("<read-files>\n%s\n</read-files>", strings.Join(readFiles, "\n")))
	}
	if len(modifiedFiles) > 0 {
		sections = append(sections, fmt.Sprintf("<modified-files>\n%s\n</modified-files>", strings.Join(modifiedFiles, "\n")))
	}
	if len(sections) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(sections, "\n\n")
}

// ============================================================================
// Message Serialization
// ============================================================================

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
			var textParts, thinkingParts, toolCalls []string

			for _, block := range a.Content {
				if block.Text != nil {
					textParts = append(textParts, block.Text.Text)
				} else if block.Thinking != nil {
					thinkingParts = append(thinkingParts, block.Thinking.Thinking)
				} else if block.ToolCall != nil {
					var argParts []string
					for k, v := range block.ToolCall.Arguments {
						argParts = append(argParts, fmt.Sprintf("%s=%s", k, toJSON(v)))
					}
					toolCalls = append(toolCalls, fmt.Sprintf("%s(%s)", block.ToolCall.Name, strings.Join(argParts, ", ")))
				}
			}

			if len(thinkingParts) > 0 {
				parts = append(parts, "[Assistant thinking]: "+strings.Join(thinkingParts, "\n"))
			}
			if len(textParts) > 0 {
				parts = append(parts, "[Assistant]: "+strings.Join(textParts, "\n"))
			}
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
		// For structured content ([]any with text/image blocks), extract text
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

// ============================================================================
// Summarization System Prompt
// ============================================================================

// SummarizationSystemPrompt is the system prompt for context summarization.
const SummarizationSystemPrompt = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI coding assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary.`
