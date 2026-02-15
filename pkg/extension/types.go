// Package extension provides the extension system for tau.
//
// Extensions are Go packages that register themselves via Register() or init().
// They can subscribe to lifecycle events, register custom tools, commands,
// flags, and shortcuts.
//
// This is a compiled-in extension system (no runtime loading). Extensions
// are included at build time by importing their packages.
package extension

import (
	"github.com/kfet/tau/pkg/agent"
	"github.com/kfet/tau/pkg/ai"
	"github.com/kfet/tau/pkg/core"
)

// Factory is the entry point for an extension.
// It receives an API handle to register event handlers, tools, commands, etc.
type Factory func(api API)

// API is passed to extension factories for registration.
type API interface {
	// On subscribes to an event. The handler receives the event and a context.
	// Return nil to continue normally, or a typed result to modify behavior.
	On(event string, handler Handler)

	// RegisterTool registers a tool callable by the LLM.
	// If name matches a built-in tool, it overrides it.
	RegisterTool(def ToolDefinition)

	// RegisterCommand registers a slash command.
	RegisterCommand(name string, cmd Command)

	// RegisterFlag registers a CLI flag.
	RegisterFlag(name string, flag Flag)

	// RegisterShortcut registers a keyboard shortcut.
	RegisterShortcut(shortcut string, handler ShortcutHandler)

	// SendMessage injects a custom message into the session.
	SendMessage(msg CustomMessageSpec, opts *SendMessageOptions)

	// SendUserMessage sends a user message to the agent.
	SendUserMessage(content string, opts *SendUserMessageOptions)

	// AppendEntry persists extension state in the session (not sent to LLM).
	AppendEntry(customType string, data any)

	// SetSessionName sets the session display name.
	SetSessionName(name string)

	// GetSessionName returns the current session name.
	GetSessionName() string

	// SetLabel sets or clears a label on an entry.
	SetLabel(entryID string, label string)

	// ClearLabel clears a label on an entry.
	ClearLabel(entryID string)

	// GetActiveTools returns the names of currently active tools.
	GetActiveTools() []string

	// GetAllTools returns info about all available tools.
	GetAllTools() []ToolInfo

	// SetActiveTools sets which tools are active by name.
	SetActiveTools(names []string)

	// GetCommands returns available slash commands.
	GetCommands() []core.SlashCommandInfo

	// SetModel sets the current model. Returns false if no API key available.
	SetModel(model *ai.Model) bool

	// GetThinkingLevel returns the current thinking level.
	GetThinkingLevel() string

	// SetThinkingLevel sets the thinking level.
	SetThinkingLevel(level string)

	// GetFlag returns the value of a registered flag.
	GetFlag(name string) any

	// Events returns the shared inter-extension event bus.
	Events() core.EventBus

	// Exec runs a shell command.
	Exec(command string, args []string) (*ExecResult, error)
}

// Handler is a function that handles an extension event.
// Return nil for events that don't produce results.
// Return a typed result struct for events that can modify behavior.
type Handler func(event *Event, ctx Context) (any, error)

// Context provides access to session state during event handling.
type Context interface {
	// UI returns the UI context for user interaction.
	UI() UIContext

	// HasUI returns whether interactive UI is available.
	HasUI() bool

	// Cwd returns the current working directory.
	Cwd() string

	// SessionManager returns read-only access to the session.
	SessionManager() *core.SessionManager

	// ModelRegistry returns the model registry.
	ModelRegistry() *core.ModelRegistry

	// Model returns the current model (may be nil).
	Model() *ai.Model

	// IsIdle returns whether the agent is idle (not streaming).
	IsIdle() bool

	// Abort aborts the current agent operation.
	Abort()

	// HasPendingMessages returns whether there are queued messages.
	HasPendingMessages() bool

	// Shutdown requests a graceful shutdown.
	Shutdown()

	// GetContextUsage returns the current context usage estimate.
	GetContextUsage() *core.ContextUsage

	// GetSystemPrompt returns the current system prompt.
	GetSystemPrompt() string
}

// CommandContext extends Context with session control methods.
// Only available in command handlers (not event handlers) to avoid deadlocks.
type CommandContext interface {
	Context

	// WaitForIdle waits for the agent to finish streaming.
	WaitForIdle()

	// NewSession starts a new session. Returns true if cancelled.
	NewSession() (cancelled bool, err error)

	// Fork creates a branch at the given entry. Returns true if cancelled.
	Fork(entryID string) (cancelled bool, err error)

	// Reload reloads extensions, skills, prompts, and themes.
	Reload() error
}

// UIContext provides user interaction methods.
type UIContext interface {
	// Select shows a selector and returns the user's choice.
	Select(title string, options []string) (string, error)

	// Confirm shows a confirmation dialog.
	Confirm(title string, message string) (bool, error)

	// Input shows a text input dialog.
	Input(title string, placeholder string) (string, error)

	// Notify shows a non-blocking notification.
	Notify(message string, level string) // level: "info", "warning", "error"

	// SetStatus sets persistent status text in the footer. Pass "" to clear.
	SetStatus(key string, text string)

	// SetWidget sets a widget above or below the editor.
	SetWidget(key string, lines []string)

	// ClearWidget removes a widget.
	ClearWidget(key string)
}

// ============================================================================
// Event types
// ============================================================================

// Event is the unified event passed to handlers.
type Event struct {
	Type string

	// Session events
	SessionStart    *SessionStartEvent
	SessionShutdown *SessionShutdownEvent

	// Agent events
	BeforeAgentStart *BeforeAgentStartEvent
	AgentStart       *AgentStartEvent
	AgentEnd         *AgentEndEvent
	TurnStart        *TurnStartEvent
	TurnEnd          *TurnEndEvent

	// Message events
	MessageStart  *MessageStartEvent
	MessageUpdate *MessageUpdateEvent
	MessageEnd    *MessageEndEvent

	// Tool events
	ToolCall           *ToolCallEvent
	ToolResult         *ToolResultEvent
	ToolExecutionStart *ToolExecutionStartEvent
	ToolExecutionEnd   *ToolExecutionEndEvent

	// Model events
	ModelSelect *ModelSelectEvent

	// Input events
	Input    *InputEvent
	UserBash *UserBashEvent

	// Context events
	Context *ContextEvent
}

// --- Session events ---

type SessionStartEvent struct{}

type SessionShutdownEvent struct{}

// --- Agent events ---

type BeforeAgentStartEvent struct {
	Prompt      string
	Images      []ai.ImageContent
	SystemPrompt string
}

type BeforeAgentStartResult struct {
	// Message to inject into the session.
	Message *CustomMessageSpec
	// Replacement system prompt (chained across extensions).
	SystemPrompt string
}

type AgentStartEvent struct{}

type AgentEndEvent struct {
	Messages []agent.AgentMessage
}

type TurnStartEvent struct {
	TurnIndex int
	Timestamp int64
}

type TurnEndEvent struct {
	TurnIndex   int
	Message     agent.AgentMessage
	ToolResults []ai.ToolResultMessage
}

// --- Message events ---

type MessageStartEvent struct {
	Message agent.AgentMessage
}

type MessageUpdateEvent struct {
	Message               agent.AgentMessage
	AssistantMessageEvent *ai.AssistantMessageEvent
}

type MessageEndEvent struct {
	Message agent.AgentMessage
}

// --- Tool events ---

type ToolCallEvent struct {
	ToolCallID string
	ToolName   string
	Input      map[string]any
}

type ToolCallResult struct {
	Block  bool
	Reason string
}

type ToolResultEvent struct {
	ToolCallID string
	ToolName   string
	Input      map[string]any
	Content    []ai.ToolResultContent
	Details    any
	IsError    bool
}

type ToolResultResult struct {
	Content []ai.ToolResultContent
	Details any
	IsError *bool
}

type ToolExecutionStartEvent struct {
	ToolCallID string
	ToolName   string
	Args       any
}

type ToolExecutionEndEvent struct {
	ToolCallID string
	ToolName   string
	Result     any
	IsError    bool
}

// --- Model events ---

type ModelSelectEvent struct {
	Model         *ai.Model
	PreviousModel *ai.Model
	Source        string // "set", "cycle", "restore"
}

// --- Input events ---

type InputEvent struct {
	Text   string
	Images []ai.ImageContent
	Source string // "interactive", "rpc", "extension"
}

type InputResult struct {
	Action string // "continue", "transform", "handled"
	Text   string
	Images []ai.ImageContent
}

type UserBashEvent struct {
	Command            string
	ExcludeFromContext bool
	Cwd                string
}

type UserBashResult struct {
	// Result provides a full replacement result.
	Result *core.BashResult
}

// --- Context events ---

type ContextEvent struct {
	Messages []agent.AgentMessage
}

type ContextResult struct {
	Messages []agent.AgentMessage
}

// ============================================================================
// Tool, command, flag, shortcut definitions
// ============================================================================

// ToolDefinition defines a tool that can be registered by an extension.
type ToolDefinition struct {
	// Name is the tool name used in LLM tool calls.
	Name string
	// Label is a human-readable label for UI.
	Label string
	// Description is shown to the LLM.
	Description string
	// Parameters defines the JSON Schema for tool parameters.
	Parameters map[string]any
	// Execute runs the tool.
	Execute ToolExecuteFunc
}

// ToolExecuteFunc executes a tool.
type ToolExecuteFunc func(ctx ToolContext) (agent.AgentToolResult, error)

// ToolContext provides context to tool execution.
type ToolContext struct {
	ToolCallID string
	Params     map[string]any
	Ctx        Context
	OnUpdate   agent.AgentToolUpdateCallback
}

// ToolInfo describes a tool (name + description).
type ToolInfo struct {
	Name        string
	Description string
}

// Command defines a slash command.
type Command struct {
	Description string
	Handler     func(args string, ctx CommandContext) error
}

// Flag defines a CLI flag.
type Flag struct {
	Description string
	Type        string // "boolean" or "string"
	Default     any    // bool or string
}

// ShortcutHandler handles a keyboard shortcut.
type ShortcutHandler struct {
	Description string
	Handler     func(ctx Context) error
}

// CustomMessageSpec describes a custom message to inject.
type CustomMessageSpec struct {
	CustomType string
	Content    any
	Display    bool
	Details    any
}

// SendMessageOptions configures how a custom message is delivered.
type SendMessageOptions struct {
	TriggerTurn bool
	DeliverAs   string // "steer", "followUp", "nextTurn"
}

// SendUserMessageOptions configures how a user message is delivered.
type SendUserMessageOptions struct {
	DeliverAs string // "steer", "followUp"
}

// ExecResult is the result of a shell command execution.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}
