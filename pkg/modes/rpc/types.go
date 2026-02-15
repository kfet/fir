// Ported from: packages/coding-agent/src/modes/rpc/rpc-types.ts
// Upstream hash: 1caadb2e
package rpc

import (
	"encoding/json"

	"github.com/kfet/tau/pkg/agent"
	"github.com/kfet/tau/pkg/ai"
	"github.com/kfet/tau/pkg/core/compaction"
)

// ============================================================================
// RPC Commands (stdin)
// ============================================================================

// RpcCommandType enumerates the types of RPC commands.
type RpcCommandType string

const (
	CmdPrompt              RpcCommandType = "prompt"
	CmdSteer               RpcCommandType = "steer"
	CmdFollowUp            RpcCommandType = "follow_up"
	CmdAbort               RpcCommandType = "abort"
	CmdNewSession          RpcCommandType = "new_session"
	CmdGetState            RpcCommandType = "get_state"
	CmdSetModel            RpcCommandType = "set_model"
	CmdCycleModel          RpcCommandType = "cycle_model"
	CmdGetAvailableModels  RpcCommandType = "get_available_models"
	CmdSetThinkingLevel    RpcCommandType = "set_thinking_level"
	CmdCycleThinkingLevel  RpcCommandType = "cycle_thinking_level"
	CmdSetSteeringMode     RpcCommandType = "set_steering_mode"
	CmdSetFollowUpMode     RpcCommandType = "set_follow_up_mode"
	CmdCompact             RpcCommandType = "compact"
	CmdSetAutoCompaction   RpcCommandType = "set_auto_compaction"
	CmdSetAutoRetry        RpcCommandType = "set_auto_retry"
	CmdAbortRetry          RpcCommandType = "abort_retry"
	CmdBash                RpcCommandType = "bash"
	CmdAbortBash           RpcCommandType = "abort_bash"
	CmdGetSessionStats     RpcCommandType = "get_session_stats"
	CmdExportHTML          RpcCommandType = "export_html"
	CmdSwitchSession       RpcCommandType = "switch_session"
	CmdFork                RpcCommandType = "fork"
	CmdGetForkMessages     RpcCommandType = "get_fork_messages"
	CmdGetLastAssistantText RpcCommandType = "get_last_assistant_text"
	CmdSetSessionName      RpcCommandType = "set_session_name"
	CmdGetMessages         RpcCommandType = "get_messages"
	CmdGetCommands         RpcCommandType = "get_commands"
)

// RpcCommand is a command sent from the client to the RPC server via stdin as JSON lines.
type RpcCommand struct {
	ID   string         `json:"id,omitempty"`
	Type RpcCommandType `json:"type"`

	// Prompt/steer/follow_up fields
	Message           string             `json:"message,omitempty"`
	Images            []ai.ImageContent  `json:"images,omitempty"`
	StreamingBehavior string             `json:"streamingBehavior,omitempty"` // "steer" or "followUp"

	// new_session fields
	ParentSession string `json:"parentSession,omitempty"`

	// set_model fields
	Provider string `json:"provider,omitempty"`
	ModelID  string `json:"modelId,omitempty"`

	// set_thinking_level fields
	Level ai.ThinkingLevel `json:"level,omitempty"`

	// set_steering_mode / set_follow_up_mode fields
	Mode string `json:"mode,omitempty"` // "all" or "one-at-a-time"

	// compact fields
	CustomInstructions string `json:"customInstructions,omitempty"`

	// set_auto_compaction / set_auto_retry fields
	Enabled *bool `json:"enabled,omitempty"`

	// bash fields
	Command string `json:"command,omitempty"`

	// export_html fields
	OutputPath string `json:"outputPath,omitempty"`

	// switch_session fields
	SessionPath string `json:"sessionPath,omitempty"`

	// fork fields
	EntryID string `json:"entryId,omitempty"`

	// set_session_name fields
	Name string `json:"name,omitempty"`
}

// ============================================================================
// RPC Slash Command
// ============================================================================

// RpcSlashCommand represents a command available for invocation via prompt.
type RpcSlashCommand struct {
	// Command name (without leading slash)
	Name string `json:"name"`
	// Human-readable description
	Description string `json:"description,omitempty"`
	// What kind of command this is: "extension", "prompt", or "skill"
	Source string `json:"source"`
	// Where the command was loaded from: "user", "project", or "path" (undefined for extensions)
	Location string `json:"location,omitempty"`
	// File path to the command source
	Path string `json:"path,omitempty"`
}

// ============================================================================
// RPC Session State
// ============================================================================

// RpcSessionState represents the current state of an agent session.
type RpcSessionState struct {
	Model                  *ai.Model        `json:"model,omitempty"`
	ThinkingLevel          ai.ThinkingLevel `json:"thinkingLevel"`
	IsStreaming            bool             `json:"isStreaming"`
	IsCompacting           bool             `json:"isCompacting"`
	SteeringMode           string           `json:"steeringMode"`
	FollowUpMode           string           `json:"followUpMode"`
	SessionFile            string           `json:"sessionFile,omitempty"`
	SessionID              string           `json:"sessionId"`
	SessionName            string           `json:"sessionName,omitempty"`
	AutoCompactionEnabled  bool             `json:"autoCompactionEnabled"`
	MessageCount           int              `json:"messageCount"`
	PendingMessageCount    int              `json:"pendingMessageCount"`
}

// ============================================================================
// Session Stats
// ============================================================================

// SessionStats provides summary statistics for a session.
type SessionStats struct {
	SessionFile       string      `json:"sessionFile,omitempty"`
	SessionID         string      `json:"sessionId"`
	UserMessages      int         `json:"userMessages"`
	AssistantMessages int         `json:"assistantMessages"`
	ToolCalls         int         `json:"toolCalls"`
	ToolResults       int         `json:"toolResults"`
	TotalMessages     int         `json:"totalMessages"`
	Tokens            TokenStats  `json:"tokens"`
	Cost              float64     `json:"cost"`
}

// TokenStats provides token usage breakdown.
type TokenStats struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cacheRead"`
	CacheWrite int `json:"cacheWrite"`
	Total      int `json:"total"`
}

// ============================================================================
// Bash Result
// ============================================================================

// BashResultData is the result of a bash command execution via RPC.
type BashResultData struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	TimedOut bool   `json:"timedOut,omitempty"`
}

// ============================================================================
// RPC Responses (stdout)
// ============================================================================

// RpcResponse is a response sent from the RPC server to the client via stdout as JSON lines.
type RpcResponse struct {
	ID      string         `json:"id,omitempty"`
	Type    string         `json:"type"` // always "response"
	Command RpcCommandType `json:"command"`
	Success bool           `json:"success"`
	Error   string         `json:"error,omitempty"`
	Data    interface{}    `json:"data,omitempty"`
}

// NewSuccessResponse creates a success response with optional data.
func NewSuccessResponse(id string, command RpcCommandType, data interface{}) RpcResponse {
	return RpcResponse{
		ID:      id,
		Type:    "response",
		Command: command,
		Success: true,
		Data:    data,
	}
}

// NewErrorResponse creates an error response.
func NewErrorResponse(id string, command RpcCommandType, errMsg string) RpcResponse {
	return RpcResponse{
		ID:      id,
		Type:    "response",
		Command: command,
		Success: false,
		Error:   errMsg,
	}
}

// ============================================================================
// Typed response data
// ============================================================================

// NewSessionData is the data for a new_session response.
type NewSessionData struct {
	Cancelled bool `json:"cancelled"`
}

// SetModelData is the data for a set_model response.
type SetModelData = ai.Model

// CycleModelData is the data for a cycle_model response.
type CycleModelData struct {
	Model        ai.Model         `json:"model"`
	ThinkingLevel ai.ThinkingLevel `json:"thinkingLevel"`
	IsScoped     bool             `json:"isScoped"`
}

// GetAvailableModelsData is the data for a get_available_models response.
type GetAvailableModelsData struct {
	Models []ai.Model `json:"models"`
}

// CycleThinkingLevelData is the data for a cycle_thinking_level response.
type CycleThinkingLevelData struct {
	Level ai.ThinkingLevel `json:"level"`
}

// CompactData is the data for a compact response.
type CompactData = compaction.CompactionResult

// ExportHTMLData is the data for an export_html response.
type ExportHTMLData struct {
	Path string `json:"path"`
}

// SwitchSessionData is the data for a switch_session response.
type SwitchSessionData struct {
	Cancelled bool `json:"cancelled"`
}

// ForkData is the data for a fork response.
type ForkData struct {
	Text      string `json:"text"`
	Cancelled bool   `json:"cancelled"`
}

// ForkMessageEntry represents a single forkable message.
type ForkMessageEntry struct {
	EntryID string `json:"entryId"`
	Text    string `json:"text"`
}

// GetForkMessagesData is the data for a get_fork_messages response.
type GetForkMessagesData struct {
	Messages []ForkMessageEntry `json:"messages"`
}

// GetLastAssistantTextData is the data for a get_last_assistant_text response.
type GetLastAssistantTextData struct {
	Text *string `json:"text"` // nil if no assistant text
}

// GetMessagesData is the data for a get_messages response.
type GetMessagesData struct {
	Messages []agent.AgentMessage `json:"messages"`
}

// GetCommandsData is the data for a get_commands response.
type GetCommandsData struct {
	Commands []RpcSlashCommand `json:"commands"`
}

// ============================================================================
// Extension UI Events (stdout → client)
// ============================================================================

// RpcExtensionUIRequest is emitted when an extension needs user input.
type RpcExtensionUIRequest struct {
	Type    string `json:"type"` // always "extension_ui_request"
	ID      string `json:"id"`
	Method  string `json:"method"` // "select", "confirm", "input", "editor", "notify", "setStatus", "setWidget", "setTitle", "set_editor_text"

	// select fields
	Title   string   `json:"title,omitempty"`
	Options []string `json:"options,omitempty"`

	// confirm fields
	Message string `json:"message,omitempty"`

	// input fields
	Placeholder string `json:"placeholder,omitempty"`

	// editor fields
	Prefill string `json:"prefill,omitempty"`

	// notify fields
	NotifyType string `json:"notifyType,omitempty"` // "info", "warning", "error"

	// setStatus fields
	StatusKey  string `json:"statusKey,omitempty"`
	StatusText string `json:"statusText,omitempty"`

	// setWidget fields
	WidgetKey       string   `json:"widgetKey,omitempty"`
	WidgetLines     []string `json:"widgetLines,omitempty"`
	WidgetPlacement string   `json:"widgetPlacement,omitempty"` // "aboveEditor" or "belowEditor"

	// set_editor_text fields
	Text string `json:"text,omitempty"`

	// Common optional field
	Timeout *int `json:"timeout,omitempty"`
}

// ============================================================================
// Extension UI Commands (stdin → server)
// ============================================================================

// RpcExtensionUIResponse is a response to an extension UI request.
type RpcExtensionUIResponse struct {
	Type      string `json:"type"` // always "extension_ui_response"
	ID        string `json:"id"`
	Value     string `json:"value,omitempty"`
	Confirmed *bool  `json:"confirmed,omitempty"`
	Cancelled bool   `json:"cancelled,omitempty"`
}

// ============================================================================
// JSON helpers
// ============================================================================

// MarshalJSON serializes a response to JSON.
func (r RpcResponse) MarshalJSON() ([]byte, error) {
	type Alias RpcResponse
	return json.Marshal(struct {
		Alias
	}{Alias: Alias(r)})
}

// ParseRpcCommand parses a JSON line into an RpcCommand.
func ParseRpcCommand(data []byte) (RpcCommand, error) {
	var cmd RpcCommand
	err := json.Unmarshal(data, &cmd)
	return cmd, err
}

// ParseExtensionUIResponse parses a JSON line into an RpcExtensionUIResponse.
func ParseExtensionUIResponse(data []byte) (RpcExtensionUIResponse, error) {
	var resp RpcExtensionUIResponse
	err := json.Unmarshal(data, &resp)
	return resp, err
}
