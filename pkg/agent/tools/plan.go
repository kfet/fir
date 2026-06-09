package tools

import (
	"context"
	"fmt"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai/core"
)

// PlanSink is the minimal interface the plan tool needs from a session:
// a place to commit the updated plan. Implementations live outside
// pkg/agent/tools (typically in the host session package).
type PlanSink interface {
	UpdatePlan(title string, entries []agent.PlanEntry, metadata map[string]string)
}

// CardPublisher is an optional callback invoked on every successful
// plan mutation. Host code may use it to render a fir-style observable
// "plan/active" card (see docs/design/observable-cards.md) — the plan
// tool itself stays free of that dependency.
//
// May be nil; the plan tool is silent when no publisher is provided.
type CardPublisher func(title string, entries []agent.PlanEntry, metadata map[string]string, entryID string)

// NewPlanTool creates the plan tool.
//
//   - sink is required and is called on every plan update.
//   - publisher is optional. When non-nil it is invoked after sink with
//     the same arguments plus the tool-call id, so host code can mirror
//     the plan state into a card store, status bar, telemetry sink, etc.
func NewPlanTool(sink PlanSink, publisher CardPublisher) agent.AgentTool {
	return agent.AgentTool{
		Tool: core.Tool{
			Name: "plan",
			Description: "Create or update a plan for tracking task progress. " +
				"You MUST create a plan before starting any task that involves 3 or more non-trivial steps. " +
				"When in doubt, create a plan.\n\n" +
				"Rules:\n" +
				"- Create the plan BEFORE your first action — not midway through\n" +
				"- Mark each step in_progress (s=\"i\") as you begin it, completed (s=\"x\") when done\n" +
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
						"description": "The complete list of plan entries. Each entry has c (content), p (priority), and s (status).",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"c": map[string]any{
									"type":        "string",
									"description": "content: description of this plan step",
								},
								"s": map[string]any{
									"type":        "string",
									"enum":        []string{"p", "i", "x"},
									"description": "status: p (pending), i (in_progress), x (completed)",
								},
								"p": map[string]any{
									"type":        "string",
									"enum":        []string{"h", "m", "l"},
									"description": "priority: h (high), m (medium), l (low)",
								},
							},
							"required": []string{"c", "s", "p"},
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
					Content: []core.ToolResultContent{{Type: "text", Text: err.Error()}},
					IsError: true,
				}, nil
			}

			title, _ := params["title"].(string)
			metadata := parsePlanMetadata(params)

			sink.UpdatePlan(title, entries, metadata)

			if publisher != nil {
				publisher(title, entries, metadata, toolCallID)
			}

			var msg string
			if len(entries) == 0 {
				msg = "Plan cleared."
			} else {
				msg = fmt.Sprintf("Plan updated (%d entries). Remember to update your plan as you complete steps.", len(entries))
			}

			return agent.AgentToolResult{
				Content: []core.ToolResultContent{{Type: "text", Text: msg}},
			}, nil
		},
	}
}

// planCodec declares the per-field compression aliases for the plan tool's
// `entries` records (transcript-footprint P3). The canonical full-name form
// (content/priority/status, with full enum values) is the source of truth; the
// model emits the compact wire form (c/p/s with short enum codes) and the codec
// expands it before validation. Decode is tolerant, so old transcripts using
// full keys and full enum values still decode and render unchanged.
//
// Only `plan` opts in for now; the codec is generic and enabled per-field.
var planCodec = newSchemaCodec(
	schemaField{
		Full:    "entries",
		IsArray: true,
		Item: newSchemaCodec(
			schemaField{Full: "content", Short: "c"},
			schemaField{Full: "priority", Short: "p", Enum: map[string]string{
				"high":   "h",
				"medium": "m",
				"low":    "l",
			}},
			schemaField{Full: "status", Short: "s", Enum: map[string]string{
				"pending":     "p",
				"in_progress": "i",
				"completed":   "x",
			}},
		),
	},
)

// DecodePlanParams expands a plan tool-call's (possibly compressed) arguments
// to canonical full-name form. It is tolerant of full-keyed/full-enum input so
// old transcripts still decode. Renderers that read raw plan args from the
// transcript should run them through this first. On a structural error the
// original params are returned unchanged so best-effort rendering can proceed.
func DecodePlanParams(params map[string]any) map[string]any {
	decoded, err := planCodec.Decode(params)
	if err != nil {
		return params
	}
	return decoded
}

func parsePlanEntries(params map[string]any) ([]agent.PlanEntry, error) {
	// Expand the compact wire form (short keys c/p/s + short enum codes) to the
	// canonical full-name form. Fail closed on a structural decode error.
	decoded, err := planCodec.Decode(params)
	if err != nil {
		return nil, err
	}

	rawEntries, ok := decoded["entries"]
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
