package tools

import (
	"context"
	"fmt"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
)

// PlanUpdater is the interface the plan tool needs from a session.
type PlanUpdater interface {
	UpdatePlan(entries []agent.PlanEntry)
}

// NewPlanTool creates the plan tool. It requires a PlanUpdater (typically *core.AgentSession).
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
				"- Keep steps concrete and actionable, not vague",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
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

			session.UpdatePlan(entries)

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
