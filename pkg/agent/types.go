// Ported from: packages/agent/src/types.ts
// Upstream hash: 036bde0a
package agent

import (
	"context"
	"strings"

	"github.com/kfet/fir/pkg/ai/core"
)

// StreamFn is the function that creates an LLM streaming call.
type StreamFn func(model *core.Model, ctx core.Context, options *core.SimpleStreamOptions) *core.AssistantMessageEventStream

// AgentLoopConfig configures the agent loop.
type AgentLoopConfig struct {
	core.SimpleStreamOptions

	// Model is the LLM model to use.
	Model *core.Model

	// ConvertToLLM converts AgentMessages to LLM-compatible Messages before each call.
	ConvertToLLM func(messages []AgentMessage) ([]core.Message, error)

	// TransformContext is an optional transform applied before ConvertToLLM.
	// Use for context window management, injecting external context, etc.
	TransformContext func(ctx context.Context, messages []AgentMessage) ([]AgentMessage, error)

	// GetApiKey resolves an API key dynamically for each LLM call.
	// Useful for short-lived OAuth tokens that may expire during tool execution.
	GetApiKey func(provider string) (string, error)

	// GetSteeringMessages returns steering messages to inject mid-run.
	// Called after the current assistant turn finishes executing its tool calls,
	// unless ShouldStopAfterTurn exits first.
	// Tool calls from the current assistant message are not skipped.
	//
	// Contract: must not return an error. Return nil/empty when no steering messages are available.
	GetSteeringMessages func() ([]AgentMessage, error)

	// ShouldStopAfterTurn is called after each turn fully completes and the
	// turn_end event has been emitted. If it returns true, the loop emits
	// agent_end and exits before polling steering or follow-up queues, without
	// starting another LLM call.
	//
	// Use this to request a graceful stop after the current turn, e.g. before
	// context gets too full.
	//
	// Contract: must not panic. Panicking interrupts the agent loop without
	// producing a normal event sequence.
	ShouldStopAfterTurn func(ctx ShouldStopAfterTurnContext) bool

	// GetFollowUpMessages returns follow-up messages after the agent would otherwise stop.
	GetFollowUpMessages func() ([]AgentMessage, error)

	// Reasoning specifies the thinking/reasoning level.
	Reasoning core.ThinkingLevel

	// SessionID is the unique identifier for this session.
	SessionID string

	// ThinkingBudgets specifies token budgets for thinking.
	ThinkingBudgets *core.ThinkingBudgets

	// Transport is the preferred transport for providers that support multiple transports.
	Transport core.Transport

	// MaxRetryDelayMs is the maximum delay between retries in milliseconds.
	MaxRetryDelayMs *int

	// ServerTools configures Anthropic server-side tools (web search, code execution, etc.).
	// Only used when the model provider is Anthropic.
	ServerTools []core.AnthropicServerTool

	// Compaction configures Anthropic server-side context compaction.
	Compaction *core.AnthropicCompaction

	// OnPayload is an optional callback to inspect or replace provider payloads before sending.
	// Return nil to keep the original payload unchanged.
	OnPayload func(payload any, model *core.Model) any

	// OnRetry is invoked before a retryable pre-stream error (rate limit /
	// overloaded / transient 5xx) is retried. Sessions can use this to notify
	// the user that a retry is in flight.
	OnRetry func(attempt int, delaySeconds float64, errMsg string)
}

// ThinkingLevel is an alias for core.ThinkingLevel so all packages use the same type.
type ThinkingLevel = core.ThinkingLevel

// Re-export core.ThinkingLevel constants for convenience.
const (
	ThinkingOff     = core.ThinkingOff
	ThinkingMinimal = core.ThinkingMinimal
	ThinkingLow     = core.ThinkingLow
	ThinkingMedium  = core.ThinkingMedium
	ThinkingHigh    = core.ThinkingHigh
	ThinkingXHigh   = core.ThinkingXHigh
	ThinkingMax     = core.ThinkingMax
)

// ToAIThinkingLevel converts a ThinkingLevel to the ai-layer value.
// Returns empty string for "off" (off means no thinking).
func ToAIThinkingLevel(t ThinkingLevel) core.ThinkingLevel {
	if t == ThinkingOff {
		return ""
	}
	return t
}

// AgentMessage is a message in the agent's conversation.
// It wraps an core.Message and can be extended with custom message types.
type AgentMessage struct {
	core.Message
	// Custom holds extension-defined message types (e.g., BashExecutionMessage).
	// When non-nil, the Message field may be empty and Custom determines the role.
	Custom any `json:"custom,omitempty"`
}

// NewAgentMessage wraps an core.Message as an AgentMessage.
func NewAgentMessage(msg core.Message) AgentMessage {
	return AgentMessage{Message: msg}
}

// Text returns the concatenated text of an assistant message's text content
// blocks (joined without separators, in source order), or "" if the message
// is not an assistant message or carries no text. It is the smallest viable
// way to pull the rendered answer out of an EventMessageEnd payload without
// walking AsAssistant().Content by hand.
func (m *AgentMessage) Text() string {
	am := m.AsAssistant()
	if am == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range am.Content {
		if c.Text != nil {
			b.WriteString(c.Text.Text)
		}
	}
	return b.String()
}

// AgentState holds the current state of the agent.
type AgentState struct {
	SystemPrompt     string
	Model            *core.Model
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
	Content []core.ToolResultContent
	// Details for UI display or logging.
	Details any
	// IsError signals that the tool result represents an error,
	// even when Execute returns a nil error. Used by extension hooks
	// to mark a modified result as an error.
	IsError bool
	// Terminate hints that the agent should stop after the current tool batch.
	// Early termination only happens when every finalized tool result in the batch
	// sets this to true.
	Terminate bool
	// StatusMessage is a transient progress label for the UI (e.g.
	// "Calling Read..."). It is only meaningful on partial-update
	// results and never persisted.
	StatusMessage string
}

// AgentToolUpdateCallback is called during streaming tool execution.
type AgentToolUpdateCallback func(partialResult AgentToolResult)

// ToolExecutionMode controls how tool calls are dispatched.
// - "sequential": tools must execute one at a time with other tool calls.
// - "parallel":   tools can execute concurrently with other tool calls.
type ToolExecutionMode string

const (
	ToolExecutionSequential ToolExecutionMode = "sequential"
	ToolExecutionParallel   ToolExecutionMode = "parallel"
)

// AgentTool extends core.Tool with execution capability.
type AgentTool struct {
	core.Tool

	// Label is a human-readable label for UI display.
	Label string

	// DisplayHint tells the TUI how to format this tool's execution.
	// Nil means use built-in formatting or the generic fallback.
	DisplayHint *ToolDisplayHint

	// ExecutionMode is an optional per-tool override of the agent's default
	// tool execution mode. When set to ToolExecutionSequential, the presence
	// of this tool in a batch forces the entire batch to execute
	// sequentially (matching upstream behaviour).
	ExecutionMode ToolExecutionMode

	// Execute runs the tool. The context can be cancelled for abort.
	Execute func(
		ctx context.Context,
		toolCallID string,
		params map[string]any,
		onUpdate AgentToolUpdateCallback,
	) (AgentToolResult, error)
}

// ToolDisplayHint tells the UI how to format a tool's execution display.
// Extensions provide this when registering tools so the TUI can render them
// nicely instead of falling back to a raw JSON dump.
type ToolDisplayHint struct {
	// TitleArgs lists argument names to show on the header line, in order.
	TitleArgs []TitleArg `json:"title_args,omitempty"`
	// ResultMaxLines is the default number of result lines shown when
	// collapsed.  Zero means use the default (10).
	ResultMaxLines int `json:"result_max_lines,omitempty"`
	// UseBox renders the tool output in a bordered box (like bash).
	UseBox bool `json:"use_box,omitempty"`
}

// TitleArg describes a single argument to display on the tool header line.
type TitleArg struct {
	// Name is the JSON parameter name.
	Name string `json:"name"`
	// Style controls how the value is rendered: "path" shortens and accents
	// it, "pattern" wraps it in /…/, "accent" just colours it.  Empty string
	// means plain text.
	Style string `json:"style,omitempty"`
	// Label is an optional prefix shown before the value (e.g. "in").
	Label string `json:"label,omitempty"`
}

// AgentContext is like core.Context but uses AgentTool.
type AgentContext struct {
	SystemPrompt string
	Messages     []AgentMessage
	Tools        *ToolSet
}

// ShouldStopAfterTurnContext is the context passed to AgentLoopConfig.ShouldStopAfterTurn.
type ShouldStopAfterTurnContext struct {
	// Message is the assistant message that completed the turn.
	Message *core.AssistantMessage
	// ToolResults are the tool result messages passed to the preceding turn_end event.
	ToolResults []core.ToolResultMessage
	// Context is the current agent context after the turn's assistant message
	// and tool results have been appended.
	Context AgentContext
	// NewMessages are the messages that this loop invocation will return if it
	// exits at this point. Prompt runs include the initial prompt messages;
	// continuation runs do not include pre-existing context messages.
	NewMessages []AgentMessage
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
	// EventStreamRetry is emitted when the agent loop detects a mid-tool-call
	// stream error (stop_reason=error with an incomplete tool_use block whose
	// Arguments never finished streaming) and is about to retry the request
	// after dropping the broken partial from history.
	EventStreamRetry AgentEventType = "stream_retry"
	// EventAutoResume is emitted when an assistant turn ends with a transport/
	// stream error (connection reset, broken pipe, unexpected EOF, …) rather
	// than a clean stop or tool call, and the agent loop is auto-resuming the
	// turn instead of pausing for a human. RetryAttempt is the 1-based resume
	// number and ErrorMessage is the transport error that triggered it. When a
	// partial response had already been emitted, the resume injects the
	// AutoResumeMarker user message so the model continues cleanly.
	EventAutoResume AgentEventType = "auto_resume"
)

// AgentEvent represents a lifecycle event from the agent.
type AgentEvent struct {
	Type AgentEventType

	// For agent_end
	Messages []AgentMessage

	// For turn_end
	TurnMessage *AgentMessage
	ToolResults []core.ToolResultMessage

	// For message_start, message_update, message_end
	Message *AgentMessage

	// For message_update
	AssistantMessageEvent *core.AssistantMessageEvent

	// For tool_execution_start, tool_execution_update, tool_execution_end
	ToolCallID  string
	ToolName    string
	Args        any
	DisplayHint *ToolDisplayHint

	// For tool_execution_update
	PartialResult any
	StatusMessage string // progress message from extensions (e.g. "Calling Read...")

	// For tool_execution_end
	Result  any
	IsError bool

	// For stream_retry
	RetryAttempt int    // 1-based attempt number of the upcoming retry
	ErrorMessage string // the stream error that triggered the retry
}
