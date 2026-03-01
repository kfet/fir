package extension

import (
	"context"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/core"
	firlog "github.com/kfet/fir/pkg/log"
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
	firlog.Info("extensions reloading", "newNames", enabledNames)

	// Notify old extensions of shutdown.
	_ = r.Runner.EmitSessionShutdown()

	// Remove previously-added extension tools from the agent.
	removeExtensionTools(r.session, r.Runner)

	// Clear all loaded extensions and merged state.
	r.Runner.Reset()

	// Load new extensions.
	if len(enabledNames) > 0 {
		if err := r.Runner.LoadEnabled(enabledNames); err != nil {
			return err
		}
	}

	// Add newly registered extension tools to the agent.
	addExtensionTools(r.session, r.Runner)

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
	firlog.Debug("extension setup", "enabledNames", opts.EnabledNames)
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

	// Add extension-registered tools to the agent's tool list so the LLM
	// can call them. Each ToolDefinition is converted to an agent.AgentTool.
	addExtensionTools(session, runner)

	return &SetupResult{
		Runner:  runner,
		session: session,
	}, nil
}

// addExtensionTools converts extension-registered ToolDefinitions to
// agent.AgentTools and appends them to the session's agent tool list.
// This makes extension tools callable by the LLM.
func addExtensionTools(session *core.AgentSession, runner *Runner) {
	extTools := runner.GetTools()
	if len(extTools) == 0 {
		return
	}

	// Base tools already in state are wrapped with hooks (applied by SetHooks).
	// Only wrap the newly-created extension tools to avoid double-wrapping.
	state := session.Agent.State()
	tools := make([]agent.AgentTool, len(state.Tools))
	copy(tools, state.Tools)

	for _, td := range extTools {
		at := extensionToolToAgentTool(td, runner)
		wrapped := session.WrapToolsWithHooks([]agent.AgentTool{at})
		tools = append(tools, wrapped[0])
	}

	session.Agent.SetTools(tools)
}

// removeExtensionTools removes any tools that were added by extensions
// (identified by matching names in the runner's tool registry).
func removeExtensionTools(session *core.AgentSession, runner *Runner) {
	extTools := runner.GetTools()
	if len(extTools) == 0 {
		return
	}

	state := session.Agent.State()
	filtered := make([]agent.AgentTool, 0, len(state.Tools))
	for _, t := range state.Tools {
		if _, isExt := extTools[t.Name]; !isExt {
			filtered = append(filtered, t)
		}
	}

	session.Agent.SetTools(filtered)
}

// extensionToolToAgentTool converts an extension ToolDefinition to an agent.AgentTool.
func extensionToolToAgentTool(td *ToolDefinition, runner *Runner) agent.AgentTool {
	return agent.AgentTool{
		Tool: ai.Tool{
			Name:        td.Name,
			Description: td.Description,
			Parameters:  td.Parameters,
		},
		Label: td.Label,
		Execute: func(ctx context.Context, toolCallID string, params map[string]any, onUpdate agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			return td.Execute(ToolContext{
				ToolCallID: toolCallID,
				Params:     params,
				Ctx:        runner.createContext(),
				OnUpdate:   onUpdate,
			})
		},
	}
}

// bridgeSessionEvents subscribes to the AgentSession event stream and
// forwards relevant agent lifecycle events to the extension Runner.
func bridgeSessionEvents(session *core.AgentSession, runner *Runner) {
	var turnCounter int
	var currentTurnIdx int
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
			currentTurnIdx = turnCounter
			turnCounter++
			_ = runner.Emit(&Event{
				Type: "turn_start",
				TurnStart: &TurnStartEvent{
					TurnIndex: currentTurnIdx,
					Timestamp: time.Now().UnixMilli(),
				},
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
					TurnIndex:   currentTurnIdx,
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
