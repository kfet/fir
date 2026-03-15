package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
)

// BatchToolProvider is the interface the batch tool needs from the session.
// It provides access to the current tool set for dispatching sub-tool calls
// and to SimplePrompt for synthesising gathered output.
type BatchToolProvider interface {
	// GetTools returns the current tool set. The batch tool uses this to
	// look up and execute sub-tools by name.
	GetTools() *agent.ToolSet
	// SimplePrompt makes a one-shot LLM call with the given messages.
	// Used to synthesise collected tool outputs into a summary.
	SimplePrompt(ctx context.Context, messages []agent.AgentMessage) (string, error)
}

// NewBatchTool creates the batch tool. It executes a list of tool calls,
// collects their output, and pipes it through a SimplePrompt with
// user-provided instructions. Only the synthesis result is returned to the
// caller — raw tool outputs are ephemeral.
func NewBatchTool(provider BatchToolProvider) agent.AgentTool {
	return agent.AgentTool{
		Tool: ai.Tool{
			Name: "batch",
			Description: "Execute multiple tools and synthesise their outputs via a one-shot LLM call. " +
				"Raw tool outputs are ephemeral — only the LLM synthesis is returned to you. " +
				"Use this when you need to gather large amounts of data from several tools " +
				"and want a concise summary without polluting your context window.\n\n" +
				"The tool list is executed sequentially. Each tool's output is collected and " +
				"then passed along with your instructions to a single LLM call that produces " +
				"the final result.",
			Parameters: batchSchema,
		},
		Label: "batch",
		Execute: func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			return executeBatch(ctx, provider, params, onUpdate)
		},
	}
}

var batchSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"description": map[string]any{
			"type":        "string",
			"description": "Brief description of what this batch is doing (shown in UI).",
		},
		"tools": map[string]any{
			"type":        "array",
			"description": "Ordered list of tool invocations to execute. Each entry names a tool and provides its parameters.",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "Name of the tool to call.",
					},
					"params": map[string]any{
						"type":        "object",
						"description": "Parameters to pass to the tool.",
					},
				},
				"required": []string{"name"},
			},
		},
		"instructions": map[string]any{
			"type":        "string",
			"description": "Instructions for the LLM that will process the collected tool outputs. Describe what to extract, summarise, or return.",
		},
	},
	"required": []string{"tools", "instructions"},
}

// batchToolCall is a parsed tool invocation from the batch params.
type batchToolCall struct {
	Name   string         `json:"name"`
	Params map[string]any `json:"params"`
}

// batchToolResult holds the outcome of a single sub-tool execution.
type batchToolResult struct {
	Name    string
	Index   int
	Output  string
	IsError bool
}

func executeBatch(
	ctx context.Context,
	provider BatchToolProvider,
	params map[string]any,
	onUpdate agent.AgentToolUpdateCallback,
) (agent.AgentToolResult, error) {
	// Parse tool calls.
	calls, err := parseBatchToolCalls(params)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	if len(calls) == 0 {
		return errorResult("tools array is empty"), nil
	}

	instructions, _ := params["instructions"].(string)
	if instructions == "" {
		return errorResult("instructions is required"), nil
	}

	description, _ := params["description"].(string)

	tools := provider.GetTools()
	if tools == nil {
		return errorResult("no tools available"), nil
	}

	// Execute each tool sequentially, collecting results.
	results := make([]batchToolResult, 0, len(calls))
	for i, call := range calls {
		// Check context before each tool.
		if ctx.Err() != nil {
			return errorResult("batch cancelled: " + ctx.Err().Error()), nil
		}

		// Report progress.
		if onUpdate != nil {
			progress := fmt.Sprintf("[batch] executing %d/%d: %s", i+1, len(calls), call.Name)
			if description != "" {
				progress = fmt.Sprintf("[batch: %s] executing %d/%d: %s", description, i+1, len(calls), call.Name)
			}
			onUpdate(agent.AgentToolResult{
				Content: []ai.ToolResultContent{{Type: "text", Text: progress}},
			})
		}

		result := executeSubTool(ctx, tools, call, i)
		results = append(results, result)
	}

	// Build the synthesis prompt from collected outputs.
	synthesisPrompt := buildSynthesisPrompt(results, instructions)

	// Report synthesis phase.
	if onUpdate != nil {
		progress := "[batch] synthesising results..."
		if description != "" {
			progress = fmt.Sprintf("[batch: %s] synthesising results...", description)
		}
		onUpdate(agent.AgentToolResult{
			Content: []ai.ToolResultContent{{Type: "text", Text: progress}},
		})
	}

	// Run SimplePrompt to synthesise.
	msgs := []agent.AgentMessage{
		agent.NewAgentMessage(ai.NewUserMsg(synthesisPrompt, time.Now().UnixMilli())),
	}

	synthesis, err := provider.SimplePrompt(ctx, msgs)
	if err != nil {
		return errorResult("synthesis failed: " + err.Error()), nil
	}

	return agent.AgentToolResult{
		Content: []ai.ToolResultContent{{Type: "text", Text: synthesis}},
	}, nil
}

// executeSubTool looks up a tool by name and executes it.
func executeSubTool(ctx context.Context, tools *agent.ToolSet, call batchToolCall, index int) batchToolResult {
	tool, found := tools.Get(call.Name)
	if !found {
		return batchToolResult{
			Name:    call.Name,
			Index:   index,
			Output:  fmt.Sprintf("tool %q not found", call.Name),
			IsError: true,
		}
	}

	if tool.Execute == nil {
		return batchToolResult{
			Name:    call.Name,
			Index:   index,
			Output:  fmt.Sprintf("tool %q has no execute function", call.Name),
			IsError: true,
		}
	}

	// Don't allow batch to call itself (prevent infinite recursion).
	if call.Name == "batch" {
		return batchToolResult{
			Name:    call.Name,
			Index:   index,
			Output:  "batch cannot call itself",
			IsError: true,
		}
	}

	toolParams := call.Params
	if toolParams == nil {
		toolParams = make(map[string]any)
	}

	result, err := tool.Execute(ctx, fmt.Sprintf("batch-%d", index), toolParams, nil)
	if err != nil {
		return batchToolResult{
			Name:    call.Name,
			Index:   index,
			Output:  err.Error(),
			IsError: true,
		}
	}

	// Collect text content from the tool result.
	var sb strings.Builder
	for _, c := range result.Content {
		if c.Type == "text" && c.Text != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(c.Text)
		}
	}

	return batchToolResult{
		Name:    call.Name,
		Index:   index,
		Output:  sb.String(),
		IsError: result.IsError,
	}
}

// buildSynthesisPrompt constructs the prompt for the SimplePrompt call.
func buildSynthesisPrompt(results []batchToolResult, instructions string) string {
	var sb strings.Builder
	sb.WriteString("You are processing the outputs of multiple tool calls. ")
	sb.WriteString("Below are the results, followed by instructions on what to return.\n\n")

	for _, r := range results {
		sb.WriteString(fmt.Sprintf("--- Tool %d: %s", r.Index+1, r.Name))
		if r.IsError {
			sb.WriteString(" [ERROR]")
		}
		sb.WriteString(" ---\n")
		sb.WriteString(r.Output)
		sb.WriteString("\n\n")
	}

	sb.WriteString("--- Instructions ---\n")
	sb.WriteString(instructions)
	sb.WriteString("\n")

	return sb.String()
}

// parseBatchToolCalls extracts the tool call list from batch params.
func parseBatchToolCalls(params map[string]any) ([]batchToolCall, error) {
	rawTools, ok := params["tools"]
	if !ok {
		return nil, fmt.Errorf("tools is required")
	}

	arr, ok := rawTools.([]any)
	if !ok {
		return nil, fmt.Errorf("tools must be an array")
	}

	calls := make([]batchToolCall, 0, len(arr))
	for i, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("tools[%d]: must be an object", i)
		}

		name, _ := obj["name"].(string)
		if name == "" {
			return nil, fmt.Errorf("tools[%d]: name is required", i)
		}

		var toolParams map[string]any
		if p, ok := obj["params"]; ok {
			if pm, ok := p.(map[string]any); ok {
				toolParams = pm
			}
		}

		calls = append(calls, batchToolCall{Name: name, Params: toolParams})
	}

	return calls, nil
}

func errorResult(msg string) agent.AgentToolResult {
	return agent.AgentToolResult{
		Content: []ai.ToolResultContent{{Type: "text", Text: msg}},
		IsError: true,
	}
}
