package tools

import (
	"context"
	"fmt"
	"sync"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
)

// DefaultPlanNudgeInterval is the number of turns between plan update reminders.
const DefaultPlanNudgeInterval = 5

// PlanNudger generates periodic steering reminders to update an active plan.
// It is safe for concurrent use.
type PlanNudger struct {
	mu               sync.Mutex
	turnsSinceUpdate int
	interval         int
	hasActivePlan    func() bool
}

// NewPlanNudger creates a nudger that fires every interval turns when
// hasActivePlan returns true. Call RecordPlanUpdate when the plan tool is
// invoked and RecordTurn after each agent turn.
//
// hasActivePlan is called while the nudger's internal mutex is held, so it
// must not attempt to acquire the nudger's mutex (it may acquire other locks).
func NewPlanNudger(interval int, hasActivePlan func() bool) *PlanNudger {
	return &PlanNudger{interval: interval, hasActivePlan: hasActivePlan}
}

// RecordTurn increments the turn counter.
func (n *PlanNudger) RecordTurn() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.turnsSinceUpdate++
}

// RecordPlanUpdate resets the turn counter (the model just updated the plan).
func (n *PlanNudger) RecordPlanUpdate() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.turnsSinceUpdate = 0
}

// Check returns a nudge message if enough turns have elapsed and there is an
// active plan with incomplete entries. Returns empty string otherwise.
func (n *PlanNudger) Check() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.turnsSinceUpdate >= n.interval && n.hasActivePlan() {
		n.turnsSinceUpdate = 0
		return "Reminder: update your plan to reflect current progress."
	}
	return ""
}

// PlanUpdater is the interface the plan tool needs from a session.
type PlanUpdater interface {
	UpdatePlan(title string, entries []agent.PlanEntry)
}

// NewPlanTool creates the plan tool. It requires a PlanUpdater (typically
// *core.AgentSession). If nudger is non-nil, it is reset on each plan update.
func NewPlanTool(session PlanUpdater, nudger *PlanNudger) agent.AgentTool {
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
					"title": map[string]any{
						"type":        "string",
						"description": "Short title for the plan (e.g. \"Implement caching layer\"). Shown in the plan header and status bar.",
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

			session.UpdatePlan(title, entries)
			if nudger != nil {
				nudger.RecordPlanUpdate()
			}

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
