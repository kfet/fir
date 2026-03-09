package tools

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
)

// planNudgeTurnThreshold is the number of turns between plan update reminders.
const planNudgeTurnThreshold = 20

// planNudgeTimeThreshold is the elapsed time since the last plan update after
// which a reminder will be sent.
const planNudgeTimeThreshold = 2 * time.Minute

// PlanNudger generates steering reminders to update an active plan.
// A nudge fires when ANY of the following conditions are met:
//   - The plan has not been updated for planNudgeTimeThreshold.
//   - planNudgeTurnThreshold turns have elapsed since the last update.
//   - The agent stops (CheckOnEnd), if the plan still has incomplete entries.
//
// It is safe for concurrent use.
type PlanNudger struct {
	mu               sync.Mutex
	turnsSinceUpdate int
	lastUpdate       time.Time
	hasActivePlan    func() bool
}

// NewPlanNudger creates a nudger that reminds the agent to update its plan.
// hasActivePlan should return true when the plan has incomplete entries.
//
// hasActivePlan is called while the nudger's internal mutex is held, so it
// must not attempt to acquire the nudger's mutex (it may acquire other locks).
func NewPlanNudger(hasActivePlan func() bool) *PlanNudger {
	return &PlanNudger{
		lastUpdate:    time.Now(),
		hasActivePlan: hasActivePlan,
	}
}

// RecordTurn increments the turn counter.
func (n *PlanNudger) RecordTurn() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.turnsSinceUpdate++
}

// RecordPlanUpdate resets both the turn counter and the last-update timestamp.
func (n *PlanNudger) RecordPlanUpdate() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.turnsSinceUpdate = 0
	n.lastUpdate = time.Now()
}

// Check returns a nudge message when the plan needs attention — either because
// planNudgeTurnThreshold turns have elapsed or planNudgeTimeThreshold time has
// passed since the last update — and there is still an active plan.
// Returns empty string when no nudge is needed.
func (n *PlanNudger) Check() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.hasActivePlan() {
		return ""
	}
	if n.turnsSinceUpdate >= planNudgeTurnThreshold || time.Since(n.lastUpdate) >= planNudgeTimeThreshold {
		n.turnsSinceUpdate = 0
		n.lastUpdate = time.Now()
		return "Reminder: update your plan to reflect current progress."
	}
	return ""
}

// CheckOnEnd returns a nudge message when the agent is about to stop and the
// plan still has incomplete entries. This nudge compels the agent to continue
// working rather than finishing with an unfinished plan.
func (n *PlanNudger) CheckOnEnd() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.hasActivePlan() {
		return "Your plan has incomplete steps. Continue working until all steps are completed or explicitly cancelled."
	}
	return ""
}

// PlanUpdater is the interface the plan tool needs from a session.
type PlanUpdater interface {
	UpdatePlan(title string, entries []agent.PlanEntry, metadata map[string]string)
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
