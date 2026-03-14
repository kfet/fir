// Ported from: packages/agent/src/types.ts
// Upstream hash: 9e22d391
package agent

import (
	"context"

	"github.com/kfet/fir/pkg/ai"
)

// StreamFn is the function that creates an LLM streaming call.
type StreamFn func(model *ai.Model, ctx ai.Context, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream

// AgentLoopConfig configures the agent loop.
type AgentLoopConfig struct {
	ai.SimpleStreamOptions

	// Model is the LLM model to use.
	Model *ai.Model

	// ConvertToLLM converts AgentMessages to LLM-compatible Messages before each call.
	ConvertToLLM func(messages []AgentMessage) ([]ai.Message, error)

	// TransformContext is an optional transform applied before ConvertToLLM.
	// Use for context window management, injecting external context, etc.
	TransformContext func(ctx context.Context, messages []AgentMessage) ([]AgentMessage, error)

	// GetApiKey resolves an API key dynamically for each LLM call.
	// Useful for short-lived OAuth tokens that may expire during tool execution.
	GetApiKey func(provider string) (string, error)

	// GetSteeringMessages returns steering messages to inject mid-run.
	// Called after each tool execution to check for user interruptions.
	GetSteeringMessages func() ([]AgentMessage, error)

	// GetFollowUpMessages returns follow-up messages after the agent would otherwise stop.
	GetFollowUpMessages func() ([]AgentMessage, error)

	// Reasoning specifies the thinking/reasoning level.
	Reasoning ai.ThinkingLevel

	// SessionID is the unique identifier for this session.
	SessionID string

	// ThinkingBudgets specifies token budgets for thinking.
	ThinkingBudgets *ai.ThinkingBudgets

	// Transport is the preferred transport for providers that support multiple transports.
	Transport ai.Transport

	// MaxRetryDelayMs is the maximum delay between retries in milliseconds.
	MaxRetryDelayMs *int

	// ServerTools configures Anthropic server-side tools (web search, code execution, etc.).
	// Only used when the model provider is Anthropic.
	ServerTools []ai.AnthropicServerTool

	// Compaction configures Anthropic server-side context compaction.
	Compaction *ai.AnthropicCompaction

	// OnPayload is an optional callback to inspect or replace provider payloads before sending.
	// Return nil to keep the original payload unchanged.
	OnPayload func(payload any, model *ai.Model) any
}

// ThinkingLevel is an alias for ai.ThinkingLevel so all packages use the same type.
type ThinkingLevel = ai.ThinkingLevel

// Re-export ai.ThinkingLevel constants for convenience.
const (
	ThinkingOff     = ai.ThinkingOff
	ThinkingMinimal = ai.ThinkingMinimal
	ThinkingLow     = ai.ThinkingLow
	ThinkingMedium  = ai.ThinkingMedium
	ThinkingHigh    = ai.ThinkingHigh
	ThinkingXHigh   = ai.ThinkingXHigh
)

// ToAIThinkingLevel converts a ThinkingLevel to the ai-layer value.
// Returns empty string for "off" (off means no thinking).
func ToAIThinkingLevel(t ThinkingLevel) ai.ThinkingLevel {
	if t == ThinkingOff {
		return ""
	}
	return t
}

// AgentMessage is a message in the agent's conversation.
// It wraps an ai.Message and can be extended with custom message types.
type AgentMessage struct {
	ai.Message
	// Custom holds extension-defined message types (e.g., BashExecutionMessage).
	// When non-nil, the Message field may be empty and Custom determines the role.
	Custom any `json:"custom,omitempty"`
}

// NewAgentMessage wraps an ai.Message as an AgentMessage.
func NewAgentMessage(msg ai.Message) AgentMessage {
	return AgentMessage{Message: msg}
}

// AgentState holds the current state of the agent.
type AgentState struct {
	SystemPrompt     string
	Model            *ai.Model
	ThinkingLevel    ThinkingLevel
	Tools            *ToolSet
	Messages         []AgentMessage
	IsStreaming      bool
	StreamMessage    *AgentMessage
	PendingToolCalls map[string]bool
	Error            string
}

// AgentToolResult is the result of executing a tool.
type AgentToolResult struct {
	// Content blocks supporting text and images.
	Content []ai.ToolResultContent
	// Details for UI display or logging.
	Details any
	// IsError signals that the tool result represents an error,
	// even when Execute returns a nil error. Used by extension hooks
	// to mark a modified result as an error.
	IsError bool
}

// AgentToolUpdateCallback is called during streaming tool execution.
type AgentToolUpdateCallback func(partialResult AgentToolResult)

// AgentTool extends ai.Tool with execution capability.
type AgentTool struct {
	ai.Tool

	// Label is a human-readable label for UI display.
	Label string

	// Execute runs the tool. The context can be cancelled for abort.
	Execute func(
		ctx context.Context,
		toolCallID string,
		params map[string]any,
		onUpdate AgentToolUpdateCallback,
	) (AgentToolResult, error)
}

// AgentContext is like ai.Context but uses AgentTool.
type AgentContext struct {
	SystemPrompt string
	Messages     []AgentMessage
	Tools        *ToolSet
}

// --- Agent Events ---

// AgentEventType identifies the type of agent lifecycle event.
type AgentEventType string

const (
	EventAgentStart          AgentEventType = "agent_start"
	EventAgentEnd            AgentEventType = "agent_end"
	EventTurnStart           AgentEventType = "turn_start"
	EventTurnEnd             AgentEventType = "turn_end"
	EventMessageStart        AgentEventType = "message_start"
	EventMessageUpdate       AgentEventType = "message_update"
	EventMessageEnd          AgentEventType = "message_end"
	EventToolExecutionStart  AgentEventType = "tool_execution_start"
	EventToolExecutionUpdate AgentEventType = "tool_execution_update"
	EventToolExecutionEnd    AgentEventType = "tool_execution_end"
)

// AgentEvent represents a lifecycle event from the agent.
type AgentEvent struct {
	Type AgentEventType

	// For agent_end
	Messages []AgentMessage

	// For turn_end
	TurnMessage *AgentMessage
	ToolResults []ai.ToolResultMessage

	// For message_start, message_update, message_end
	Message *AgentMessage

	// For message_update
	AssistantMessageEvent *ai.AssistantMessageEvent

	// For tool_execution_start, tool_execution_update, tool_execution_end
	ToolCallID string
	ToolName   string
	Args       any

	// For tool_execution_update
	PartialResult any

	// For tool_execution_end
	Result  any
	IsError bool
}
