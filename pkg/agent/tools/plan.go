package tools

import (
	"context"
	"fmt"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
)

// PlanUpdater is the interface the plan tool needs from a session.
type PlanUpdater interface {
	UpdatePlan(title string, entries []agent.PlanEntry, metadata map[string]string)
}

// NewPlanTool creates the plan tool. It requires a PlanUpdater (typically
// *core.AgentSession).
func NewPlanTool(session PlanUpdater) agent.AgentTool {
	return agent.AgentTool{
		Tool: ai.Tool{
			Name: "plan",
			Description: "Create or update a plan for tracking task progress. " +
				"You MUST create a plan before starting any task that involves 3 or more non-trivial steps. " +
				"When in doubt, create a plan.\n\n" +
				"Rules:\n" +
				"- Create the plan BEFORE your first action — not midway through\n" +
				"- Mark each step \"in_progress\" as you begin it, \"completed\" when done\n" +
				"- Update the plan after completing each step, before moving to the next\n" +
				"- Each call replaces the entire plan — always include all entries\n" +
				"- Keep steps concrete and actionable, not vague\n" +
				"- Use metadata for short contextual info (e.g. how to access a fleet, session name, worktree path)",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title": map[string]any{
						"type":        "string",
						"description": "Short title for the plan (e.g. \"Implement caching layer\"). Shown in the plan header and status bar.",
					},
					"metadata": map[string]any{
						"type":        "object",
						"description": "Optional key-value pairs shown in the plan header. Max 5 keys, values ≤80 chars. Use for context like session names, access commands, or links.",
						"additionalProperties": map[string]any{
							"type":      "string",
							"maxLength": 80,
						},
					},
					"entries": map[string]any{
						"type":        "array",
						"description": "The complete list of plan entries. Each entry has content, status, and priority.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"content": map[string]any{
									"type":        "string",
									"description": "Description of this plan step",
								},
								"status": map[string]any{
									"type":        "string",
									"enum":        []string{"pending", "in_progress", "completed"},
									"description": "Current status of this step",
								},
								"priority": map[string]any{
									"type":        "string",
									"enum":        []string{"high", "medium", "low"},
									"description": "Priority of this step",
								},
							},
							"required": []string{"content", "status", "priority"},
						},
					},
				},
				"required": []string{"entries"},
			},
		},
		Label: "plan",
		Execute: func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			entries, err := parsePlanEntries(params)
			if err != nil {
				return agent.AgentToolResult{
					Content: []ai.ToolResultContent{{Type: "text", Text: err.Error()}},
					IsError: true,
				}, nil
			}

			title, _ := params["title"].(string)
			metadata := parsePlanMetadata(params)

			session.UpdatePlan(title, entries, metadata)

			var msg string
			if len(entries) == 0 {
				msg = "Plan cleared."
			} else {
				msg = fmt.Sprintf("Plan updated (%d entries). Remember to update your plan as you complete steps.", len(entries))
			}

			return agent.AgentToolResult{
				Content: []ai.ToolResultContent{{Type: "text", Text: msg}},
			}, nil
		},
	}
}

func parsePlanEntries(params map[string]any) ([]agent.PlanEntry, error) {
	rawEntries, ok := params["entries"]
	if !ok {
		return nil, nil
	}

	arr, ok := rawEntries.([]any)
	if !ok {
		return nil, fmt.Errorf("entries must be an array")
	}

	entries := make([]agent.PlanEntry, 0, len(arr))
	for i, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("entry %d: must be an object", i)
		}

		content, _ := obj["content"].(string)
		if content == "" {
			return nil, fmt.Errorf("entry %d: content is required", i)
		}

		status, _ := obj["status"].(string)
		if !agent.ValidStatus(status) {
			status = "pending"
		}

		priority, _ := obj["priority"].(string)
		if !agent.ValidPriority(priority) {
			priority = "medium"
		}

		entries = append(entries, agent.PlanEntry{
			Content:  content,
			Status:   agent.PlanEntryStatus(status),
			Priority: agent.PlanEntryPriority(priority),
		})
	}

	return entries, nil
}

// parsePlanMetadata extracts the optional metadata map from plan params.
// Enforces max 5 keys and 80-char value limit.
func parsePlanMetadata(params map[string]any) map[string]string {
	raw, ok := params["metadata"]
	if !ok {
		return nil
	}
	obj, ok := raw.(map[string]any)
	if !ok || len(obj) == 0 {
		return nil
	}
	result := make(map[string]string, len(obj))
	i := 0
	for k, v := range obj {
		if i >= 5 {
			break
		}
		s, _ := v.(string)
		if len(s) > 80 {
			s = s[:80]
		}
		result[k] = s
		i++
	}
	return result
}
