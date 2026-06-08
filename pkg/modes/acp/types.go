// Package acp implements ACP (Agent Client Protocol) mode for fir.
//
// ACP clients (e.g., Zed) spawn fir with `--mode acp` and communicate
// via newline-delimited JSON-RPC 2.0 on stdin/stdout.
//
// Ported from: packages/coding-agent/src/modes/acp/acp-mode.ts
package acp

import (
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
)

// ============================================================================
// ACP mode configuration
// ============================================================================

// Options configures ACP mode, passed from CLI flags.
type Options struct {
	// Additional skill paths from --skill flags.
	AdditionalSkillPaths []string
	// Disable skill discovery (--no-skills).
	NoSkills bool
	// Disable extension loading (--no-extensions).
	NoExtensions bool
	// Disable MCP server integration (--no-mcp).
	NoMCP bool
	// MCPConfig is an extra MCP config file path from --mcp-config.
	MCPConfig string
	// WaitMCP blocks session creation until all MCP servers finish their
	// initial handshake (up to 30s) so the first prompt sees every MCP tool.
	WaitMCP bool
	// EnabledExtensions is an explicit allowlist of extension names from --extension flags.
	// When non-empty, only extensions in this list are started (merged with settings).
	EnabledExtensions []string
	// DisabledExtensions is a denylist of extension names from --disable-extension flags.
	DisabledExtensions []string
	// IdleTTL is how long an in-memory session may sit idle before the
	// background reaper tears it down (freeing extension sidecars and MCP
	// subprocesses). Zero disables the reaper.
	IdleTTL time.Duration
}

// ============================================================================
// Unstable ACP types (not yet in the Go SDK schema 0.10.7)
// These correspond to ACP schema 0.14.1 "unstable" methods used by Zed.
// ============================================================================

// ListSessionsRequest is the request for session/list.
type ListSessionsRequest struct {
	// Optional working directory to filter sessions.
	Cwd string `json:"cwd,omitempty"`
}

// SessionInfo describes a session in a session/list response.
type SessionInfo struct {
	// SessionId is the unique identifier for this session (file path for disk sessions).
	SessionId string `json:"sessionId"`
	// Cwd is the working directory the session was created in.
	Cwd string `json:"cwd,omitempty"`
	// Title is the human-readable session name or first message.
	Title *string `json:"title"`
	// UpdatedAt is the ISO 8601 timestamp of the last modification.
	UpdatedAt string `json:"updatedAt"`
}

// ListSessionsResponse is the response for session/list.
type ListSessionsResponse struct {
	Sessions []SessionInfo `json:"sessions"`
}

// ResumeSessionRequest is the request for session/resume.
type ResumeSessionRequest struct {
	// SessionId is the session to resume (file path, from listSessions).
	SessionId string `json:"sessionId"`
	// Cwd is the working directory to use.
	Cwd string `json:"cwd,omitempty"`
	// McpServers are MCP server configurations provided by the client.
	// These are merged with project-level configs (client entries take precedence).
	McpServers []acpsdk.McpServer `json:"mcpServers,omitempty"`
}

// ResumeSessionResponse is the response for session/resume.
type ResumeSessionResponse struct {
	// Models is the current model state for the resumed session.
	Models interface{} `json:"models,omitempty"`
}

// ============================================================================
// Extended AuthMethod types (RFD: auth-methods)
// https://agentclientprotocol.com/rfds/auth-methods
//
// The Go SDK (v0.6.3) AuthMethod has only Id/Name/Description.
// The RFD adds a "type" discriminator with type-specific fields.
// We define these locally until the SDK adds native support.
// ============================================================================

// AuthMethodType is the type discriminator for auth methods per the RFD.
type AuthMethodType string

const (
	// AuthMethodTypeAgent means the agent handles auth itself (default).
	AuthMethodTypeAgent AuthMethodType = "agent"
	// AuthMethodTypeEnvVar means the client collects a key and passes it as an env var.
	AuthMethodTypeEnvVar AuthMethodType = "env_var"
	// AuthMethodTypeTerminal means the client runs the agent in an interactive terminal.
	AuthMethodTypeTerminal AuthMethodType = "terminal"
)

// ExtendedAuthMethod extends acpsdk.AuthMethod with RFD auth-methods fields.
// JSON-serialized, these extra fields ride alongside the SDK's Id/Name/Description.
type ExtendedAuthMethod struct {
	// Id is the unique identifier for this auth method.
	Id string `json:"id"`
	// Name is the human-readable display name.
	Name string `json:"name"`
	// Description provides details about this auth method.
	Description string `json:"description,omitempty"`
	// Type is the auth method discriminator. Defaults to "agent" if empty.
	Type AuthMethodType `json:"type,omitempty"`
	// VarName is the env var name (env_var type only).
	VarName string `json:"varName,omitempty"`
	// Link is an optional URL where the user can obtain their key (env_var type only).
	Link string `json:"link,omitempty"`
	// Args are additional CLI arguments (terminal type only).
	Args []string `json:"args,omitempty"`
	// Env are additional environment variables (terminal type only).
	Env map[string]string `json:"env,omitempty"`
	// Meta is the _meta extension point, used for terminal-auth capability negotiation.
	Meta map[string]any `json:"_meta,omitempty"`
}

// AuthRequiredError is the JSON-RPC error code for auth-required (-32000).
const AuthRequiredError = -32000

// SessionNotFoundError is the stable JSON-RPC error code returned when a
// request references a sessionID that the agent does not currently hold in
// memory. It is the shared contract between fir (ACP agent) and any relay
// (e.g. poe-acp) so the relay can detect a released/reaped session and
// transparently recover by re-creating it. Do NOT change this value.
const SessionNotFoundError = -32001

// newSessionNotFound builds the typed session-not-found JSON-RPC error.
func newSessionNotFound(sessionID string) *acpsdk.RequestError {
	return &acpsdk.RequestError{
		Code:    SessionNotFoundError,
		Message: "Session not found",
		Data:    map[string]any{"sessionId": sessionID},
	}
}

// ReleaseSessionRequest is the request for session/release. It instructs the
// agent to tear down and forget the named in-memory session (freeing its
// extension sidecars, MCP subprocesses, and goroutines). The on-disk session
// file is left intact and can be resumed later.
type ReleaseSessionRequest struct {
	SessionId string `json:"sessionId"`
}

// ReleaseSessionResponse is the (empty) response for session/release.
type ReleaseSessionResponse struct{}

// ============================================================================
// Session config types (not yet in the Go SDK v0.6.3)
// https://agentclientprotocol.com/protocol/schema
//
// SessionConfigOption is a dropdown selector that appears in the client UI.
// Category "thought_level" tells Zed to render it as the thinking mode picker.
// ============================================================================

// SessionConfigOptionCategory identifies the semantic category of a config option.
type SessionConfigOptionCategory string

const (
	SessionConfigCategoryThoughtLevel SessionConfigOptionCategory = "thought_level"
	SessionConfigCategoryModel        SessionConfigOptionCategory = "model"
)

// SessionConfigSelectOption is one selectable value in a config dropdown.
type SessionConfigSelectOption struct {
	Value       string  `json:"value"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

// SessionConfigOption describes a dropdown config selector and its current state.
type SessionConfigOption struct {
	Type         string                      `json:"type"` // always "select"
	Id           string                      `json:"id"`
	Name         string                      `json:"name"`
	Description  *string                     `json:"description,omitempty"`
	Category     SessionConfigOptionCategory `json:"category,omitempty"`
	CurrentValue string                      `json:"currentValue"`
	Options      []SessionConfigSelectOption `json:"options"`
}

// SetSessionConfigOptionRequest is the request for session/set_config_option.
type SetSessionConfigOptionRequest struct {
	SessionId string `json:"sessionId"`
	ConfigId  string `json:"configId"`
	Value     string `json:"value"`
}

// SetSessionConfigOptionResponse is the response for session/set_config_option.
type SetSessionConfigOptionResponse struct {
	ConfigOptions []SessionConfigOption `json:"configOptions"`
}
