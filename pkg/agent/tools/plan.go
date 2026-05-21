package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/session/store"
)

// PlanUpdater is the interface the plan tool needs from a session.
// In addition to updating the in-memory plan, the tool publishes an
// observable card on every mutation so the plan is visible to sibling
// agents through the cards file. See docs/design/observable-cards.md
// "Producers in MVP".
type PlanUpdater interface {
	UpdatePlan(title string, entries []agent.PlanEntry, metadata map[string]string)
	// Observables returns the session's observable cards store. Implementations
	// may return nil; Put/Clear are nil-safe at the store layer.
	Observables() *store.ObservableStore
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
				"- Update the plan immediately whenever any item's status changes — do not batch status updates\n" +
				"- Each call replaces the entire plan — always include all entries\n" +
				"- Keep steps concrete and actionable, not vague\n" +
				"- Use metadata for short contextual info (e.g. how to access a fleet, session name, worktree path)\n" +
				"- Set metadata key \"progress_metric\" to a free-form short string that represents real task progress (e.g. \"coverage=95.2%\", \"endpoints migrated 3/8\", \"tests passing 12/40\"). Update it as the underlying number moves; the harness counts plan-updates since the string last changed and surfaces stagnation back to you.\n" +
				"- Always set metadata key \"next_update_in\" to estimate how many turns before your next plan update (e.g. \"3\"). This controls how often you get reminded.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title": map[string]any{
						"type":        "string",
						"description": "Short title for the plan (e.g. \"Implement caching layer\"). Shown in the plan header and status bar.",
					},
					"metadata": map[string]any{
						"type":        "object",
						"description": "Optional key-value pairs shown in the plan header. Max 5 keys, values ≤80 chars. Use for context like session names, access commands, or links. Recognised keys: \"progress_metric\" (a free-form short string representing real progress, e.g. \"coverage=95.2%\" or \"endpoints migrated 3/8\"; the harness counts plan-updates since this string last changed and surfaces stagnation back to you), \"next_update_in\" (estimated turns until next plan update, e.g. \"3\"; controls reminder cadence).",
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

			// Publish "plan/active" card in lockstep with UpdatePlan
			// (the plan tool owns the source — see the design doc).
			publishPlanCard(session.Observables(), title, entries, metadata, toolCallID)

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

// publishPlanCard writes (or clears) the canonical "plan/active" card
// for the current plan state. Called from the plan tool's Execute on
// every mutation. Slug prefers metadata.progress_metric; detail is a
// bullet listing. Nil store is a silent no-op (store layer guarantee).
func publishPlanCard(s *store.ObservableStore, title string, entries []agent.PlanEntry, metadata map[string]string, entryID string) {
	if len(entries) == 0 {
		s.Clear("plan", "active")
		return
	}
	s.Put("plan", "active",
		planSlug(entries, metadata),
		planDetail(title, entries),
		entryID,
	)
}

// planSlug renders the short headline for the plan card.
//
//	progress_metric (if set, non-empty)
//	  OR
//	"<completed>/<total> <inflight-status>"
//	  where inflight-status is "in_progress" if any entry is in progress,
//	  "done" if all completed, else "pending"
func planSlug(entries []agent.PlanEntry, metadata map[string]string) string {
	if metric := strings.TrimSpace(metadata["progress_metric"]); metric != "" {
		return metric
	}
	total := len(entries)
	completed := 0
	inProgress := false
	for _, e := range entries {
		switch e.Status {
		case agent.PlanEntryStatusCompleted:
			completed++
		case agent.PlanEntryStatusInProgress:
			inProgress = true
		}
	}
	status := "pending"
	switch {
	case completed == total:
		status = "done"
	case inProgress:
		status = "in_progress"
	}
	return fmt.Sprintf("%d/%d %s", completed, total, status)
}

// planDetail renders the bullet listing of plan entries. One line per
// entry, with a status marker (✓ done, ▶ in progress, · pending) and
// the entry's content. Title (when set) goes on the first line.
func planDetail(title string, entries []agent.PlanEntry) string {
	var sb strings.Builder
	if t := strings.TrimSpace(title); t != "" {
		sb.WriteString(t)
		sb.WriteByte('\n')
	}
	for _, e := range entries {
		marker := "·" // pending / unknown
		switch e.Status {
		case agent.PlanEntryStatusCompleted:
			marker = "✓"
		case agent.PlanEntryStatusInProgress:
			marker = "▶"
		}
		sb.WriteString(marker)
		sb.WriteByte(' ')
		sb.WriteString(e.Content)
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
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
