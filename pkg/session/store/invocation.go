package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
)

// SessionInvocation records the user-intent runtime configuration that was
// passed on the command line (or equivalent) when the session was created.
//
// It is stamped into the session header (first jsonl line) exactly once at
// session creation and is never rewritten — subsequent `fir -c` / `fir -r` /
// `/resume` invocations read it and re-apply the original config so the
// resumed session has the same MCPs/extensions/model/etc. as the session it
// claims to continue.
//
// Only user-intent inputs are persisted (paths, flags, allowlists). Snapshots
// of file contents are not — referenced files are re-read on resume. To detect
// drift we record a sha256 of the file contents at stamp time and warn on
// resume if it has changed.
//
// Never persisted: api keys, env, mode, cwd (already in the header),
// transient flags like --wait-mcp, --session-name, --session-dir.
type SessionInvocation struct {
	// Provider and model selection.
	Provider string   `json:"provider,omitempty"`
	Model    string   `json:"model,omitempty"`
	Models   []string `json:"models,omitempty"`
	Thinking string   `json:"thinking,omitempty"`

	// System prompts.
	SystemPrompt       string `json:"system_prompt,omitempty"`
	AppendSystemPrompt string `json:"append_system_prompt,omitempty"`

	// Tools.
	Tools   []string `json:"tools,omitempty"`
	NoTools bool     `json:"no_tools,omitempty"`

	// MCP.
	MCPConfig       string `json:"mcp_config,omitempty"`
	MCPConfigSHA256 string `json:"mcp_config_sha256,omitempty"`
	NoMCP           bool   `json:"no_mcp,omitempty"`

	// Extensions.
	Extensions         []string `json:"extensions,omitempty"`
	DisabledExtensions []string `json:"disabled_extensions,omitempty"`
	NoExtensions       bool     `json:"no_extensions,omitempty"`

	// Skills / themes.
	Skills   []string `json:"skills,omitempty"`
	NoSkills bool     `json:"no_skills,omitempty"`
	Themes   []string `json:"themes,omitempty"`
	NoThemes bool     `json:"no_themes,omitempty"`
}

// IsEmpty reports whether the invocation carries no meaningful config.
func (inv *SessionInvocation) IsEmpty() bool {
	if inv == nil {
		return true
	}
	return inv.Provider == "" && inv.Model == "" && len(inv.Models) == 0 &&
		inv.Thinking == "" &&
		inv.SystemPrompt == "" && inv.AppendSystemPrompt == "" &&
		len(inv.Tools) == 0 && !inv.NoTools &&
		inv.MCPConfig == "" && !inv.NoMCP &&
		len(inv.Extensions) == 0 && len(inv.DisabledExtensions) == 0 && !inv.NoExtensions &&
		len(inv.Skills) == 0 && !inv.NoSkills &&
		len(inv.Themes) == 0 && !inv.NoThemes
}

// HashFile returns the lowercase hex sha256 of the file at path, or "" if the
// file is missing or unreadable. Used by stamp-time and resume-time code to
// detect drift in referenced config files (currently --mcp-config).
func HashFile(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// LoadInvocation reads only the first line of the session file and returns
// the persisted invocation, or nil if the session has no invocation stamped
// (legacy sessions) or the header could not be parsed.
func LoadInvocation(sessionFile string) *SessionInvocation {
	data, err := os.ReadFile(sessionFile)
	if err != nil {
		return nil
	}
	line := string(data)
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	var h SessionHeader
	if err := json.Unmarshal([]byte(line), &h); err != nil {
		return nil
	}
	if h.Type != "session" {
		return nil
	}
	if h.Invocation == nil || h.Invocation.IsEmpty() {
		return nil
	}
	return h.Invocation
}
