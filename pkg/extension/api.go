package extension

import (
	"context"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/session"
)

// BridgeAPI is the subset of the session API that external process
// extensions can call. Implemented by SessionBridge in session_bridge.go.
type BridgeAPI interface {
	Exec(command string, args []string) (ExecResult, error)
	SendMessage(spec CustomMessageSpec, opts *SendMessageOptions)
	SendUserMessage(content string, opts *SendUserMessageOptions)
	SetSessionName(name string)
	GetSessionName() string
	// GetSessionFile returns the absolute path to the session's JSONL
	// transcript on disk, or "" for in-memory (non-persisted) sessions.
	// The file is created at session start and appended to as events occur,
	// so observers (e.g. `fir observe`) can `tail -F` it from byte 0.
	GetSessionFile() string
	// GetSessionID returns the unique identifier for the current session.
	GetSessionID() string
	SetLabel(entryID string, label string)
	ClearLabel(entryID string)
	SetModel(model *ai.Model) bool
	ContinueSession() error
	SideQuery(question string, opts *session.SideQueryOptions) (string, error)
	RegisterTool(def ToolDefinition)
	// SetSessionData stores a key/value pair in the extension's session data
	// store.  Values are persisted across /reexec via the reexec sidecar and
	// are handed back to the extension in the session_start event params under
	// the "session_data" key.
	SetSessionData(key, value string)
	// GetSessionData retrieves a previously stored value.  Returns ("", false)
	// when the key is absent.
	GetSessionData(key string) (string, bool)
	// CallTool executes a registered tool by name and returns its result.
	// Used by extensions that need to call other tools programmatically
	// (e.g. the aside extension).
	CallTool(ctx context.Context, name string, params map[string]any) (ToolResult, error)
	// ListTools returns the names and parameter schemas of all registered tools.
	ListTools() []ToolInfo
	// PrependContext adds a [SYS_EXT] block to the system prompt.
	// Extensions use this to inject dynamic context.
	PrependContext(content string)
	// ReportProgress sends a transient status message to the UI
	// (e.g. "Calling Read..."). Only meaningful while a tool is executing.
	ReportProgress(message string)
	// Introspect returns a snapshot of the session's runtime state.
	Introspect() session.Introspection
	// RestartSession aborts any in-flight stream, clears the session
	// (LLM history, plan, system prompt rebuild) and submits prompt as
	// the first message of the fresh session. Returns an error when the
	// active mode does not support restart (e.g. ACP, headless).
	//
	// The call is "fire-and-forget on the agent loop": Abort() is invoked
	// synchronously so the in-flight tool call's result is short-circuited;
	// the rest (clear + Prompt) happens asynchronously. Callers therefore
	// must not rely on any state from the *current* turn surviving.
	RestartSession(prompt string) error
}

// ExecResult is the result of a shell command.
type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// CustomMessageSpec describes a custom message to inject.
type CustomMessageSpec struct {
	CustomType string `json:"custom_type"`
	Content    any    `json:"content"`
	Display    bool   `json:"display"`
}

// SendMessageOptions configures message delivery.
type SendMessageOptions struct {
	TriggerTurn bool   `json:"trigger_turn"`
	DeliverAs   string `json:"deliver_as"`
}

// SendUserMessageOptions configures user message delivery.
type SendUserMessageOptions struct {
	DeliverAs string `json:"deliver_as"`
}

// ToolDisplayHint is an alias for agent.ToolDisplayHint.
type ToolDisplayHint = agent.ToolDisplayHint

// TitleArg is an alias for agent.TitleArg.
type TitleArg = agent.TitleArg

// ToolDefinition defines a tool registered by an external extension.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]any
	DisplayHint *ToolDisplayHint
	Execute     ToolExecuteFunc
	// Bridge is the owning Bridge (set by Bridge.RegisterTools).
	// Used by SessionBridge to wire per-call progress reporting.
	Bridge *Bridge
}

// ToolExecuteFunc executes a tool.
type ToolExecuteFunc func(ctx ToolContext) (ToolResult, error)

// ToolContext provides context for tool execution.
type ToolContext struct {
	Context    context.Context
	ToolCallID string
	Params     map[string]any
}

// ToolResult is the result of a tool execution.
type ToolResult struct {
	Content []ai.ToolResultContent `json:"content"`
	IsError bool                   `json:"is_error"`
	Details map[string]any         `json:"details,omitempty"`
}

// ToolInfo describes a registered tool's name and parameter schema.
type ToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}
