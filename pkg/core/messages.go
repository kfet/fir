// Ported from: packages/coding-agent/src/core/messages.ts
// Upstream hash: 1caadb2e
package core

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/kfet/tau/pkg/agent"
	"github.com/kfet/tau/pkg/ai"
)

// Compaction and branch summary prefixes/suffixes.
const (
	CompactionSummaryPrefix = "The conversation history before this point was compacted into the following summary:\n\n<summary>\n"
	CompactionSummarySuffix = "\n</summary>"

	BranchSummaryPrefix = "The following is a summary of a branch that this conversation came back from:\n\n<summary>\n"
	BranchSummarySuffix = "</summary>"
)

// BashExecutionMessage represents a bash execution via the ! command.
type BashExecutionMessage struct {
	Role               string `json:"role"` // "bashExecution"
	Command            string `json:"command"`
	Output             string `json:"output"`
	ExitCode           *int   `json:"exitCode,omitempty"`
	Cancelled          bool   `json:"cancelled"`
	Truncated          bool   `json:"truncated"`
	FullOutputPath     string `json:"fullOutputPath,omitempty"`
	Timestamp          int64  `json:"timestamp"`
	ExcludeFromContext bool   `json:"excludeFromContext,omitempty"`
}

// CustomMessage represents an extension-injected message.
type CustomMessage struct {
	Role       string `json:"role"` // "custom"
	CustomType string `json:"customType"`
	Content    any    `json:"content"` // string or structured content
	Display    bool   `json:"display"`
	Details    any    `json:"details,omitempty"`
	Timestamp  int64  `json:"timestamp"`
}

// BranchSummaryMessage represents a branch summary.
type BranchSummaryMessage struct {
	Role      string `json:"role"` // "branchSummary"
	Summary   string `json:"summary"`
	FromID    string `json:"fromId"`
	Timestamp int64  `json:"timestamp"`
}

// CompactionSummaryMessage represents a compaction summary.
type CompactionSummaryMessage struct {
	Role         string `json:"role"` // "compactionSummary"
	Summary      string `json:"summary"`
	TokensBefore int    `json:"tokensBefore"`
	Timestamp    int64  `json:"timestamp"`
}

// TextContent is a text block for message content.
type TextContent struct {
	Type string `json:"type"` // "text"
	Text string `json:"text"`
}

// BashExecutionToText converts a BashExecutionMessage to user-facing text.
func BashExecutionToText(msg *BashExecutionMessage) string {
	text := fmt.Sprintf("Ran `%s`\n", msg.Command)
	if msg.Output != "" {
		text += fmt.Sprintf("```\n%s\n```", msg.Output)
	} else {
		text += "(no output)"
	}
	if msg.Cancelled {
		text += "\n\n(command cancelled)"
	} else if msg.ExitCode != nil && *msg.ExitCode != 0 {
		text += fmt.Sprintf("\n\nCommand exited with code %d", *msg.ExitCode)
	}
	if msg.Truncated && msg.FullOutputPath != "" {
		text += fmt.Sprintf("\n\n[Output truncated. Full output: %s]", msg.FullOutputPath)
	}
	return text
}

// CreateBranchSummaryMessage creates a branch summary agent message.
func CreateBranchSummaryMessage(summary, fromID string, timestamp time.Time) agent.AgentMessage {
	return agent.AgentMessage{
		Custom: &BranchSummaryMessage{
			Role:      "branchSummary",
			Summary:   summary,
			FromID:    fromID,
			Timestamp: timestamp.UnixMilli(),
		},
	}
}

// CreateCompactionSummaryMessage creates a compaction summary agent message.
func CreateCompactionSummaryMessage(summary string, tokensBefore int, timestamp time.Time) agent.AgentMessage {
	return agent.AgentMessage{
		Custom: &CompactionSummaryMessage{
			Role:         "compactionSummary",
			Summary:      summary,
			TokensBefore: tokensBefore,
			Timestamp:    timestamp.UnixMilli(),
		},
	}
}

// CreateCustomMessage creates a custom extension agent message.
func CreateCustomMessage(customType string, content json.RawMessage, display bool, details json.RawMessage, timestamp time.Time) agent.AgentMessage {
	var contentVal any
	if len(content) > 0 {
		// Try to unmarshal as string first
		var s string
		if err := json.Unmarshal(content, &s); err == nil {
			contentVal = s
		} else {
			contentVal = content
		}
	}
	var detailsVal any
	if len(details) > 0 {
		detailsVal = details
	}
	return agent.AgentMessage{
		Custom: &CustomMessage{
			Role:       "custom",
			CustomType: customType,
			Content:    contentVal,
			Display:    display,
			Details:    detailsVal,
			Timestamp:  timestamp.UnixMilli(),
		},
	}
}

// ConvertToLLM transforms AgentMessages (including custom types) to LLM-compatible Messages.
func ConvertToLLM(messages []agent.AgentMessage) ([]ai.Message, error) {
	var result []ai.Message

	for _, m := range messages {
		if m.Custom != nil {
			converted := convertCustomToLLM(m)
			if converted != nil {
				result = append(result, *converted)
			}
			continue
		}

		role := m.Role()
		switch role {
		case "user", "assistant", "toolResult":
			result = append(result, m.Message)
		default:
			// Skip unknown roles
		}
	}

	return result, nil
}

// convertCustomToLLM converts a custom message to an LLM-compatible user message.
func convertCustomToLLM(m agent.AgentMessage) *ai.Message {
	switch msg := m.Custom.(type) {
	case *BashExecutionMessage:
		if msg.ExcludeFromContext {
			return nil
		}
		text := BashExecutionToText(msg)
		result := ai.NewUserMsg(text, msg.Timestamp)
		return &result

	case *BranchSummaryMessage:
		text := BranchSummaryPrefix + msg.Summary + BranchSummarySuffix
		result := ai.NewUserMsg(text, msg.Timestamp)
		return &result

	case *CompactionSummaryMessage:
		text := CompactionSummaryPrefix + msg.Summary + CompactionSummarySuffix
		result := ai.NewUserMsg(text, msg.Timestamp)
		return &result

	case *CustomMessage:
		result := ai.NewUserMsg(msg.Content, msg.Timestamp)
		return &result

	default:
		return nil
	}
}
