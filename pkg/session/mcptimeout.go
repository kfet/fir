package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
)

// internalToolCallKey marks a context as an internal, re-entrant tool call —
// one issued on behalf of a builtin/extension (pipe, wait, aside, …) via
// SessionBridge.CallTool, rather than dispatched directly by the model in the
// agent loop. The default MCP tool-call timeout is applied ONLY on the direct
// model-dispatch path; internal calls carry the sentinel and are left to run
// under their own governance (e.g. pipe/wait declaring timeout=-1). See
// wrapMCPToolTimeout and SessionBridge.CallTool.
type internalToolCallKey struct{}

// WithInternalToolCall stamps ctx as an internal re-entrant tool call so the
// default MCP tool-call timeout is not (re-)applied to it.
func WithInternalToolCall(ctx context.Context) context.Context {
	return context.WithValue(ctx, internalToolCallKey{}, true)
}

// IsInternalToolCall reports whether ctx was stamped by WithInternalToolCall.
func IsInternalToolCall(ctx context.Context) bool {
	v, _ := ctx.Value(internalToolCallKey{}).(bool)
	return v
}

// wrapMCPToolTimeout decorates an MCP tool's Execute so that, on the direct
// model-dispatch path, the call is bounded by timeout with TRUE cancellation:
// the derived context is a child of the turn context the agent loop passes
// through, so hitting the deadline cancels the underlying go-sdk tools/call
// and unwinds the server round-trip (rather than merely abandoning the wait).
//
// The wrapper is a no-op (returns the tool unchanged) when timeout <= 0.
//
// It must be applied to the RAW tool Execute, innermost — before hook
// wrapping — so N bounds only the MCP round-trip and never a blocking
// OnToolCall hook.
//
// Internal re-entrant calls (IsInternalToolCall) bypass the bound entirely so
// a pipe/wait step that is itself a slow MCP tool still honours its own
// declared timeout (including timeout=-1, i.e. run arbitrarily long).
func wrapMCPToolTimeout(t agent.AgentTool, timeout time.Duration) agent.AgentTool {
	if timeout <= 0 || t.Execute == nil {
		return t
	}
	orig := t.Execute
	toolName := t.Name
	t.Execute = func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
		if IsInternalToolCall(ctx) {
			return orig(ctx, toolCallID, params, onUpdate)
		}
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		result, err := orig(callCtx, toolCallID, params, onUpdate)
		// Only synthesise a clean timeout result when OUR deadline fired and
		// the parent (turn) context is still live. If the parent was cancelled
		// (ESC / turn abort) propagate the original error unchanged so abort
		// semantics are preserved and a user cancel is never reported to the
		// model as a tool timeout.
		if err != nil && errors.Is(callCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return agent.AgentToolResult{
				Content: []ai.ToolResultContent{{
					Type: ai.ContentTypeText,
					Text: fmt.Sprintf("MCP tool %q timed out after %s. The server did not respond in time; it may be unresponsive or the operation may be too slow. You can retry, try a different approach, or ask the user to raise the mcp.toolTimeoutSeconds setting.", toolName, timeout),
				}},
				IsError: true,
			}, nil
		}
		return result, err
	}
	return t
}
