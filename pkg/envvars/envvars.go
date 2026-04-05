// Package envvars is the single source of truth for all FIR_* environment
// variables. Both the CLI --help text and the embedded "self" skill read from
// this registry so they can never drift apart.
package envvars

import (
	"fmt"
	"strings"
)

// Var describes one environment variable.
type Var struct {
	Name        string // e.g. "FIR_DEBUG"
	Description string // short one-liner for help text
	Default     string // default value (empty string = none)
	Internal    bool   // if true, omitted from user-facing docs
}

// Registry is the canonical list. Keep it sorted by name.
var Registry = []Var{
	{Name: "FIR_AGENT_DIR", Description: "Override config/session directory (default: ~/.config/fir)"},
	{Name: "FIR_CACHE_RETENTION", Description: "Anthropic prompt cache retention (set to \"long\" for extended)"},
	{Name: "FIR_DEBUG", Description: "Enable debug logging (set to 1)"},
	{Name: "FIR_DEBUG_LOG", Description: "Debug log file path (default: ~/.config/fir/debug.log)"},
	{Name: "FIR_EXT_TIMEOUT", Description: "Extension init handshake timeout in seconds (default: 5)"},
	{Name: "FIR_HARDWARE_CURSOR", Description: "Show hardware cursor in TUI (set to 1)"},
	{Name: "FIR_MCP_CONFIG", Description: "Extra MCP config file path (--mcp-config flag wins)"},
	{Name: "FIR_PTY_SOCKET_DIR", Description: "Override directory for PTY socket files"},
	{Name: "FIR_REEXEC_CONTINUE", Description: "Internal: signal that session is being re-exec'd", Internal: true},
	{Name: "FIR_TIMING", Description: "Enable request timing logs (set to 1)"},
}

// ProviderKeys lists the provider API key env vars (non-FIR_* but still
// important to document alongside).
var ProviderKeys = []Var{
	{Name: "ANTHROPIC_API_KEY", Description: "Anthropic Claude API key"},
	{Name: "AWS_PROFILE", Description: "AWS profile for Amazon Bedrock"},
	{Name: "GEMINI_API_KEY", Description: "Google Gemini API key"},
	{Name: "GROQ_API_KEY", Description: "Groq API key"},
	{Name: "MINIMAX_API_KEY", Description: "MiniMax API key"},
	{Name: "MISTRAL_API_KEY", Description: "Mistral API key"},
	{Name: "OPENAI_API_KEY", Description: "OpenAI GPT API key"},
	{Name: "OPENROUTER_API_KEY", Description: "OpenRouter API key"},
	{Name: "XAI_API_KEY", Description: "xAI Grok API key"},
}

// FormatHelpText returns a formatted block suitable for CLI --help output.
// Each line is: "  %-32s - %s\n". Internal vars are excluded.
func FormatHelpText() string {
	var sb strings.Builder
	// Provider keys first
	for _, v := range ProviderKeys {
		fmt.Fprintf(&sb, "  %-32s - %s\n", v.Name, v.Description)
	}
	// Then FIR_ vars
	for _, v := range Registry {
		if v.Internal {
			continue
		}
		fmt.Fprintf(&sb, "  %-32s - %s\n", v.Name, v.Description)
	}
	return sb.String()
}

// FormatMarkdownTable returns a Markdown table for embedding in skill docs.
// Internal vars are excluded.
func FormatMarkdownTable() string {
	var sb strings.Builder
	sb.WriteString("| Variable | Description |\n")
	sb.WriteString("|----------|-------------|\n")
	for _, v := range ProviderKeys {
		fmt.Fprintf(&sb, "| `%s` | %s |\n", v.Name, v.Description)
	}
	for _, v := range Registry {
		if v.Internal {
			continue
		}
		fmt.Fprintf(&sb, "| `%s` | %s |\n", v.Name, v.Description)
	}
	return sb.String()
}
