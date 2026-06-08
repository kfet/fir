// Ported from: packages/coding-agent/src/core/compaction/compaction.ts
// Upstream hash: 1caadb2e
package compaction

import (
	"encoding/json"
	"math"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/session/store"
)

// ============================================================================
// Token calculation
// ============================================================================

// CalculateContextTokens calculates total context tokens from usage.
func CalculateContextTokens(usage ai.Usage) int {
	if usage.TotalTokens > 0 {
		return usage.TotalTokens
	}
	return usage.Input + usage.Output + usage.CacheRead + usage.CacheWrite
}

// getAssistantUsage returns usage from an assistant message if valid.
func getAssistantUsage(msg agent.AgentMessage) *ai.Usage {
	if msg.Role() != "assistant" {
		return nil
	}
	a := msg.Message.AsAssistant()
	if a == nil {
		return nil
	}
	if a.StopReason == ai.StopReasonAborted || a.StopReason == ai.StopReasonError {
		return nil
	}
	// Treat all-zero usage as "no data" so the character-count fallback fires.
	u := &a.Usage
	if u.TotalTokens == 0 && u.Input == 0 && u.Output == 0 && u.CacheRead == 0 && u.CacheWrite == 0 {
		return nil
	}
	return u
}

// EstimateContextTokens estimates context tokens from messages.
func EstimateContextTokens(messages []agent.AgentMessage) ContextUsageEstimate {
	var lastUsage *ai.Usage
	var lastIndex *int
	for i := len(messages) - 1; i >= 0; i-- {
		if u := getAssistantUsage(messages[i]); u != nil {
			lastUsage = u
			idx := i
			lastIndex = &idx
			break
		}
	}

	if lastUsage == nil {
		estimated := 0
		for _, msg := range messages {
			estimated += EstimateTokens(msg)
		}
		return ContextUsageEstimate{
			Tokens:         estimated,
			TrailingTokens: estimated,
		}
	}

	usageTokens := CalculateContextTokens(*lastUsage)
	trailingTokens := 0
	for i := *lastIndex + 1; i < len(messages); i++ {
		trailingTokens += EstimateTokens(messages[i])
	}

	return ContextUsageEstimate{
		Tokens:         usageTokens + trailingTokens,
		UsageTokens:    usageTokens,
		TrailingTokens: trailingTokens,
		LastUsageIndex: lastIndex,
	}
}

// ShouldCompact checks if compaction should trigger.
// Compaction fires if either condition is true:
//   - Absolute token cap: contextTokens > MaxContextTokens (when MaxContextTokens > 0)
//   - Fill-ratio: context is at least 70% full. Models degrade well before
//     the window is exhausted (research: ~30k effective regardless of
//     stated window), so we trigger early rather than waiting for the
//     reserve to be eaten. The old AND-with-reserve gate is removed.
func ShouldCompact(contextTokens, contextWindow int, settings CompactionSettings) bool {
	if !settings.Enabled || contextWindow <= 0 {
		return false
	}

	// Absolute token cap — fires unconditionally when set.
	if settings.MaxContextTokens > 0 && contextTokens > settings.MaxContextTokens {
		return true
	}

	const minFillRatio = 0.70
	return float64(contextTokens)/float64(contextWindow) >= minFillRatio
}

// ============================================================================
// Token estimation
// ============================================================================

// EstimateTokens estimates token count for a message using chars/4 heuristic.
func EstimateTokens(message agent.AgentMessage) int {
	chars := 0

	// Handle custom message types
	if message.Custom != nil {
		switch msg := message.Custom.(type) {
		case *store.BashExecutionMessage:
			chars = len(msg.Command) + len(msg.Output)
		case *store.BranchSummaryMessage:
			chars = len(msg.Summary)
		case *store.CompactionSummaryMessage:
			chars = len(msg.Summary)
		case *store.CustomMessage:
			if s, ok := msg.Content.(string); ok {
				chars = len(s)
			}
		}
		return int(math.Ceil(float64(chars) / 4))
	}

	role := message.Role()
	switch role {
	case "user":
		u := message.Message.AsUser()
		if u == nil {
			return 0
		}
		switch content := u.Content.(type) {
		case string:
			chars = len(content)
		case []any:
			for _, block := range content {
				if m, ok := block.(map[string]any); ok {
					if m["type"] == "text" {
						if t, ok := m["text"].(string); ok {
							chars += len(t)
						}
					}
				}
			}
		}

	case "assistant":
		a := message.Message.AsAssistant()
		if a == nil {
			return 0
		}
		for _, block := range a.Content {
			if block.Text != nil {
				chars += len(block.Text.Text)
			} else if block.Thinking != nil {
				chars += len(block.Thinking.Thinking)
			} else if block.ToolCall != nil {
				chars += len(block.ToolCall.Name)
				argsJSON, _ := json.Marshal(block.ToolCall.Arguments)
				chars += len(argsJSON)
			}
		}

	case "toolResult":
		tr := message.Message.AsToolResult()
		if tr == nil {
			return 0
		}
		for _, c := range tr.Content {
			if c.IsText() {
				chars += len(c.Text)
			}
			if c.IsImage() {
				chars += 4800
			}
		}
	}

	return int(math.Ceil(float64(chars) / 4))
}
