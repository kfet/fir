package extension

import (
	"github.com/kfet/tau/pkg/agent"
	"github.com/kfet/tau/pkg/ai"
	"github.com/kfet/tau/pkg/core"
)

// SetupResult holds the results of setting up extensions for a session.
// It keeps internal references needed to reload extensions at runtime.
type SetupResult struct {
	Runner *Runner

	// Internal state for reload support.
	session *core.AgentSession
}

// Reload tears down the current extensions and loads a new set.
// It emits session_shutdown to old extensions, resets the runner,
// loads the new extensions, and emits session_start to them.
//
// Because the tool hooks and event bridge closures reference the Runner
// (not individual extensions), they automatically dispatch to the newly
// loaded handlers after reload — no re-wiring is needed.
func (r *SetupResult) Reload(enabledNames []string) error {
	if r.Runner == nil {
		return nil
	}

	// Notify old extensions of shutdown.
	_ = r.Runner.EmitSessionShutdown()

	// Clear all loaded extensions and merged state.
	r.Runner.Reset()

	// Load new extensions.
	if len(enabledNames) > 0 {
		if err := r.Runner.LoadEnabled(enabledNames); err != nil {
			return err
		}
	}

	// Notify new extensions of start.
	_ = r.Runner.EmitSessionStart()

	return nil
}

// SetupOptions configures which extensions to load.
type SetupOptions struct {
	// EnabledNames lists the extension names to activate. If nil or empty,
	// no extensions are loaded (extensions are off by default).
	EnabledNames []string
}

// Setup creates and configures an extension runner for the given session.
// It loads only the extensions named in opts.EnabledNames, binds actions,
// sets up tool hooks, and bridges agent session events to the extension runner.
//
// Tool hooks and the event bridge are always wired (even when no extensions are
// initially loaded) so that a subsequent call to SetupResult.Reload can activate
// extensions without needing to re-wrap tools or re-subscribe events.
func Setup(session *core.AgentSession, eventBus core.EventBus, opts SetupOptions) (*SetupResult, error) {
	runner := NewRunner(eventBus)

	if len(opts.EnabledNames) > 0 {
		if err := runner.LoadEnabled(opts.EnabledNames); err != nil {
			return nil, err
		}
	}

	// Bind actions — these closures reference the session and remain valid
	// across extension reloads since the session doesn't change.
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

	// Set up tool hooks on the session.
	// These are always wired so that extensions loaded via Reload() work
	// without re-wrapping tools. When no handlers are registered, the
	// runner's emit methods return nil and the hooks are no-ops.
	hooks := &core.AgentSessionHooks{
		OnToolCall: func(toolCallID, toolName string, input map[string]any) *core.ToolCallBlock {
			result := runner.EmitToolCall(toolCallID, toolName, input)
			if result != nil && result.Block {
				return &core.ToolCallBlock{Reason: result.Reason}
			}
			return nil
		},
		OnToolResult: func(toolCallID, toolName string, input map[string]any, content []ai.ToolResultContent, details any, isError bool) *core.ToolResultModification {
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
		},
	}
	session.SetHooks(hooks)

	// Bridge agent session events to the extension runner so that extensions
	// subscribing to agent_start, agent_end, turn_start, turn_end, etc.
	// receive those events without each mode having to forward them manually.
	// The bridge closure references the runner, so it automatically dispatches
	// to whatever handlers are currently loaded after a reload.
	bridgeSessionEvents(session, runner)

	return &SetupResult{
		Runner:  runner,
		session: session,
	}, nil
}

// bridgeSessionEvents subscribes to the AgentSession event stream and
// forwards relevant agent lifecycle events to the extension Runner.
func bridgeSessionEvents(session *core.AgentSession, runner *Runner) {
	session.Subscribe(func(event core.AgentSessionEvent) {
		ae := event.AgentEvent
		if ae == nil {
			return
		}

		switch ae.Type {
		case agent.EventAgentStart:
			_ = runner.EmitAgentStart()

		case agent.EventAgentEnd:
			_ = runner.EmitAgentEnd(ae.Messages)

		case agent.EventTurnStart:
			_ = runner.Emit(&Event{
				Type:      "turn_start",
				TurnStart: &TurnStartEvent{},
			})

		case agent.EventTurnEnd:
			var toolResults []ai.ToolResultMessage
			if ae.ToolResults != nil {
				toolResults = ae.ToolResults
			}
			var turnMsg agent.AgentMessage
			if ae.TurnMessage != nil {
				turnMsg = *ae.TurnMessage
			}
			_ = runner.Emit(&Event{
				Type: "turn_end",
				TurnEnd: &TurnEndEvent{
					Message:     turnMsg,
					ToolResults: toolResults,
				},
			})

		case agent.EventMessageStart:
			if ae.Message != nil {
				_ = runner.Emit(&Event{
					Type:         "message_start",
					MessageStart: &MessageStartEvent{Message: *ae.Message},
				})
			}

		case agent.EventMessageEnd:
			if ae.Message != nil {
				_ = runner.Emit(&Event{
					Type:       "message_end",
					MessageEnd: &MessageEndEvent{Message: *ae.Message},
				})
			}

		case agent.EventToolExecutionStart:
			_ = runner.Emit(&Event{
				Type: "tool_execution_start",
				ToolExecutionStart: &ToolExecutionStartEvent{
					ToolCallID: ae.ToolCallID,
					ToolName:   ae.ToolName,
					Args:       ae.Args,
				},
			})

		case agent.EventToolExecutionEnd:
			_ = runner.Emit(&Event{
				Type: "tool_execution_end",
				ToolExecutionEnd: &ToolExecutionEndEvent{
					ToolCallID: ae.ToolCallID,
					ToolName:   ae.ToolName,
					Result:     ae.Result,
					IsError:    ae.IsError,
				},
			})
		}
	})
}
