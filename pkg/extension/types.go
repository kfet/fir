package extension

// This file defines typed structs for every JSON-RPC method and event
// payload that crosses the extension wire boundary. They replace ad-hoc
// `map[string]any{...}` literals scattered through the package so that:
//
//   * JSON field names are declared once, in one place, and verified by
//     the compiler instead of by string-grep.
//   * Reviewers can see the entire wire surface in one file.
//   * Refactors and renames cannot accidentally break wire compatibility.
//
// All field tags are kept identical to the previous map-based output so
// existing extensions continue to work without changes.

// ---------------------------------------------------------------------------
// Generic ack
// ---------------------------------------------------------------------------

// OkResult is the standard `{"ok": true}` ack returned by most bridge
// methods that have nothing else to report.
type OkResult struct {
	Ok bool `json:"ok"`
}

// okTrue is a small convenience for the common case.
var okTrue = OkResult{Ok: true}

// ---------------------------------------------------------------------------
// Inbound bridge-method param shapes (extension → fir → BridgeAPI)
// ---------------------------------------------------------------------------

// notifyParams maps to "notify".
type notifyParams struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

// execParams maps to "exec".
type execParams struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// sendMessageParams maps to "send_message".
type sendMessageParams struct {
	CustomType  string `json:"custom_type"`
	Content     any    `json:"content"`
	Display     bool   `json:"display"`
	DeliverAs   string `json:"deliver_as"`
	TriggerTurn bool   `json:"trigger_turn"`
}

// sendUserMessageParams maps to "send_user_message".
type sendUserMessageParams struct {
	Content   string `json:"content"`
	DeliverAs string `json:"deliver_as"`
}

// setSessionNameParams maps to "set_session_name".
type setSessionNameParams struct {
	Name string `json:"name"`
}

// setLabelParams maps to "set_label".
type setLabelParams struct {
	EntryID string `json:"entry_id"`
	Label   string `json:"label"`
}

// clearLabelParams maps to "clear_label".
type clearLabelParams struct {
	EntryID string `json:"entry_id"`
}

// setModelParams maps to "set_model".
type setModelParams struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
}

// setStatusParams maps to "set_status".
type setStatusParams struct {
	Status string `json:"status"`
}

// sideQueryParams maps to "side_query".
type sideQueryParams struct {
	Question string `json:"question"`
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
	Effort   string `json:"effort,omitempty"`
}

// setSessionDataParams maps to "set_session_data".
type setSessionDataParams struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// getSessionDataParams maps to "get_session_data".
type getSessionDataParams struct {
	Key string `json:"key"`
}

// callToolParams maps to "call_tool".
type callToolParams struct {
	Name   string         `json:"name"`
	Params map[string]any `json:"params"`
}

// prependContextParams maps to "prepend_context".
type prependContextParams struct {
	Content string `json:"content"`
}

// reportProgressParams maps to "report_progress" (request and notification).
type reportProgressParams struct {
	Message string `json:"message"`
}

// restartSessionParams maps to "restart_session".
type restartSessionParams struct {
	Prompt string `json:"prompt"`
}

// ---------------------------------------------------------------------------
// Bridge-method result shapes
// ---------------------------------------------------------------------------

// SideQueryResult is the result of a "side_query" bridge call.
type SideQueryResult struct {
	Ok   bool   `json:"ok"`
	Text string `json:"text"`
}

// GetSessionDataResult is the result of "get_session_data".
type GetSessionDataResult struct {
	Value string `json:"value"`
	Ok    bool   `json:"ok"`
}

// GetSessionFileResult is the result of "get_session_file".
type GetSessionFileResult struct {
	Path string `json:"path"`
}

// GetSessionIDResult is the result of "get_session_id".
type GetSessionIDResult struct {
	ID string `json:"id"`
}

// GetSessionNameResult is the result of "get_session_name".
type GetSessionNameResult struct {
	Name string `json:"name"`
}

// ---------------------------------------------------------------------------
// Event payloads (fir → extension, notification)
// ---------------------------------------------------------------------------

// SessionEndPayload is emitted on session_end. The legacy
// session_shutdown event still carries no payload.
type SessionEndPayload struct {
	Reason string `json:"reason"`
	Error  string `json:"error,omitempty"`
}

// SessionNamedPayload is emitted on session_named.
type SessionNamedPayload struct {
	Name string `json:"name"`
}

// PlanInfo is the `plan` field of a SessionUpdatePayload of type
// "plan_update".
type PlanInfo struct {
	Total     int               `json:"total"`
	Completed int               `json:"completed"`
	Metadata  map[string]string `json:"metadata"`
}

// SessionUpdatePayload is emitted on session_update.
type SessionUpdatePayload struct {
	Type        string    `json:"type"`
	SessionName string    `json:"session_name"`
	Plan        *PlanInfo `json:"plan,omitempty"`
}

// ToolExecutionStartPayload is emitted on tool_execution_start.
type ToolExecutionStartPayload struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
}

// ToolExecutionEndPayload is emitted on tool_execution_end.
type ToolExecutionEndPayload struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	IsError    bool   `json:"is_error"`
	ErrorText  string `json:"error_text,omitempty"`
}

// MessageEndCost is the per-bucket spend reported in MessageEndUsage.
type MessageEndCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
	Total      float64 `json:"total"`
}

// MessageEndUsage is the usage payload attached to assistant messages.
type MessageEndUsage struct {
	Input       int            `json:"input"`
	Output      int            `json:"output"`
	CacheRead   int            `json:"cache_read"`
	CacheWrite  int            `json:"cache_write"`
	TotalTokens int            `json:"total_tokens"`
	Cost        MessageEndCost `json:"cost"`
}

// MessageEndPayload is emitted on message_end. For non-assistant roles
// only Role is populated.
type MessageEndPayload struct {
	Role       string           `json:"role"`
	Provider   string           `json:"provider,omitempty"`
	Model      string           `json:"model,omitempty"`
	StopReason string           `json:"stop_reason,omitempty"`
	ResponseID string           `json:"response_id,omitempty"`
	Usage      *MessageEndUsage `json:"usage,omitempty"`
}

// SessionStartPayload is the per-extension payload for session_start.
// Both fields are optional: a fresh session emits no params at all (we
// pass nil), a resumed session may carry session_id and previously
// persisted session_data.
type SessionStartPayload struct {
	SessionID   string            `json:"session_id,omitempty"`
	SessionData map[string]string `json:"session_data,omitempty"`
}

// ---------------------------------------------------------------------------
// Outbound hook payloads (fir → extension, request)
// ---------------------------------------------------------------------------

// ToolCallHookPayload is the params shape sent with a "tool_call" or
// "hook/tool_call" outbound request to an extension. The inner Params
// is the per-tool input map and stays free-form by design.
type ToolCallHookPayload struct {
	ToolCallID string         `json:"tool_call_id"`
	ToolName   string         `json:"tool_name,omitempty"`
	Name       string         `json:"name,omitempty"`
	Params     map[string]any `json:"params"`
}

// CommandHookPayload is the params shape sent with a "hook/command"
// outbound request to an extension.
type CommandHookPayload struct {
	Name string   `json:"name"`
	Args []string `json:"args"`
}
