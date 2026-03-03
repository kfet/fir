package extension

import "github.com/kfet/fir/pkg/ai"

// BridgeAPI is the subset of the session API that external process
// extensions can call. Implemented by SessionBridge in session_bridge.go.
type BridgeAPI interface {
	Exec(command string, args []string) (ExecResult, error)
	SendMessage(spec CustomMessageSpec, opts *SendMessageOptions)
	SendUserMessage(content string, opts *SendUserMessageOptions)
	SetSessionName(name string)
	GetSessionName() string
	SetLabel(entryID string, label string)
	ClearLabel(entryID string)
	GetActiveTools() []string
	SetActiveTools(names []string)
	SetModel(model *ai.Model) bool
	RegisterTool(def ToolDefinition)
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

// ToolDefinition defines a tool registered by an external extension.
type ToolDefinition struct {
	Name        string
	Description string
	// PromptSnippet is an optional one-line snippet for the system prompt's
	// Available tools section. Falls back to Description when empty.
	PromptSnippet string
	// PromptGuidelines are optional guideline bullets appended to the system
	// prompt's Guidelines section when this tool is active.
	PromptGuidelines []string
	Parameters       map[string]any
	Execute          ToolExecuteFunc
}

// ToolExecuteFunc executes a tool.
type ToolExecuteFunc func(ctx ToolContext) (ToolResult, error)

// ToolContext provides context for tool execution.
type ToolContext struct {
	ToolCallID string
	Params     map[string]any
}

// ToolResult is the result of a tool execution.
type ToolResult struct {
	Content []ai.ToolResultContent `json:"content"`
	IsError bool                   `json:"is_error"`
}
