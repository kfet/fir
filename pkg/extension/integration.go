package extension

import (
	"github.com/kfet/pi-go/pkg/ai"
	"github.com/kfet/pi-go/pkg/core"
)

// SetupResult holds the results of setting up extensions for a session.
type SetupResult struct {
	Runner *Runner
}

// Setup creates and configures an extension runner for the given session.
// It loads all registered extensions, binds actions, and sets up tool hooks.
func Setup(session *core.AgentSession, eventBus core.EventBus) (*SetupResult, error) {
	runner := NewRunner(eventBus)

	if err := runner.LoadAll(); err != nil {
		return nil, err
	}

	// No extensions registered — skip binding
	if len(runner.Extensions()) == 0 {
		return &SetupResult{Runner: runner}, nil
	}

	// Bind actions
	runner.BindActions(&Actions{
		GetModel: func() *ai.Model {
			return session.Model()
		},
		IsIdle: func() bool {
			return !session.IsStreaming()
		},
		Abort: func() {
			session.Agent.Abort()
		},
		HasPendingMessages: func() bool {
			return false // simplified for now
		},
		Shutdown: func() {
			// Will be overridden by the mode
		},
		GetContextUsage: func() *core.ContextUsage {
			return session.GetContextUsage()
		},
		GetSystemPrompt: func() string {
			return session.GetSystemPrompt()
		},
		Cwd: func() string {
			return session.GetCwd()
		},
		SessionManager: func() *core.SessionManager {
			return session.SessionManager
		},
		ModelRegistry: func() *core.ModelRegistry {
			return session.ModelRegistryRef()
		},
		AgentSession: func() *core.AgentSession {
			return session
		},
	})

	// Set up tool hooks on the session
	hooks := &core.AgentSessionHooks{}

	if runner.HasHandlers("tool_call") {
		hooks.OnToolCall = func(toolCallID, toolName string, input map[string]any) *core.ToolCallBlock {
			result := runner.EmitToolCall(toolCallID, toolName, input)
			if result != nil && result.Block {
				return &core.ToolCallBlock{Reason: result.Reason}
			}
			return nil
		}
	}

	if runner.HasHandlers("tool_result") {
		hooks.OnToolResult = func(toolCallID, toolName string, input map[string]any, content []ai.ToolResultContent, details any, isError bool) *core.ToolResultModification {
			event := &ToolResultEvent{
				ToolCallID: toolCallID,
				ToolName:   toolName,
				Input:      input,
				Content:    content,
				Details:    details,
				IsError:    isError,
			}
			result := runner.EmitToolResult(event)
			if result != nil {
				return &core.ToolResultModification{
					Content: result.Content,
					Details: result.Details,
					IsError: result.IsError,
				}
			}
			return nil
		}
	}

	if hooks.OnToolCall != nil || hooks.OnToolResult != nil {
		session.SetHooks(hooks)
	}

	return &SetupResult{Runner: runner}, nil
}
