// Package acp implements ACP (Agent Client Protocol) mode for tau.
//
// ACP clients (e.g., Zed) spawn tau with `--mode acp` and communicate
// via newline-delimited JSON-RPC 2.0 on stdin/stdout.
//
// Ported from: packages/coding-agent/src/modes/acp/acp-mode.ts
package acp

// ============================================================================
// ACP mode configuration
// ============================================================================

// Options configures ACP mode, passed from CLI flags.
type Options struct {
	// Additional skill paths from --skill flags.
	AdditionalSkillPaths []string
	// Additional prompt template paths from --prompt-template flags.
	AdditionalPromptTemplatePaths []string
	// Disable skill discovery (--no-skills).
	NoSkills bool
	// Disable prompt template discovery (--no-prompt-templates).
	NoPromptTemplates bool
	// Disable extension loading (--no-extensions).
	NoExtensions bool
	// Enabled extension names.
	EnabledExtensions []string
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
}

// ResumeSessionResponse is the response for session/resume.
type ResumeSessionResponse struct {
	// Models is the current model state for the resumed session.
	Models interface{} `json:"models,omitempty"`
}
