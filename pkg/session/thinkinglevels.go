// Thinking-level helpers that depend on fir's model catalog. The
// agent-side ladder + clamp logic lives in the agent module
// (github.com/kfet/agent); the catalog knowledge (which specific model
// IDs support xhigh / max) stays here so the agent module does not need
// fir's model catalog. See
// docs/design/ai-agent-extraction.md (Phase 3.5 / Phase 5).

package session

import (
	"github.com/kfet/agent"
	core "github.com/kfet/ai"
	"github.com/kfet/fir/pkg/ai"
)

// AvailableThinkingLevelsForModel returns the thinking levels supported by
// the given model. It mirrors AgentSession.GetAvailableThinkingLevels so the
// same set of options is offered everywhere (CLI, ACP, interactive UI).
//
// A nil model or a non-reasoning model only supports ThinkingOff.
func AvailableThinkingLevelsForModel(model *core.Model) []agent.ThinkingLevel {
	if model == nil || !model.Reasoning {
		return []agent.ThinkingLevel{agent.ThinkingOff}
	}
	levels := []agent.ThinkingLevel{
		agent.ThinkingOff,
		agent.ThinkingMinimal,
		agent.ThinkingLow,
		agent.ThinkingMedium,
		agent.ThinkingHigh,
	}
	if ai.SupportsXhigh(model) {
		levels = append(levels, agent.ThinkingXHigh)
	}
	if ai.SupportsMax(model) {
		levels = append(levels, agent.ThinkingMax)
	}
	return levels
}
