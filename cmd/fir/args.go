// Ported from: packages/coding-agent/src/cli/args.ts
// Upstream hash: 1caadb2e
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/envvars"
)

// Mode is the output mode for fir.
type Mode string

const (
	ModeText Mode = "text"
	ModeJSON Mode = "json"
	ModeACP  Mode = "acp"
)

// Args holds parsed CLI arguments.
type Args struct {
	Provider           string
	Model              string
	ApiKey             string
	SystemPrompt       string
	AppendSystemPrompt string
	Thinking           agent.ThinkingLevel
	Continue           bool
	Resume             bool
	Help               bool
	Version            bool
	OutputMode         Mode
	NoSession          bool
	Session            string
	SessionName        string
	SessionDir         string
	Models             []string
	Tools              []string
	NoTools            bool
	NoMCP              bool
	MCPConfig          string
	WaitMCP            bool
	Extensions         []string
	DisabledExtensions []string
	NoExtensions       bool
	Print              bool
	Export             string
	NoSkills           bool
	Skills             []string
	PromptTemplates    []string
	NoPromptTemplates  bool
	Themes             []string
	NoThemes           bool
	ListModels         any // true (bool) or string (search pattern)
	ListAvailModels    any // true (bool) or string (search pattern)
	Verbose            bool
	Debug              bool
	DebugLogFile       string
	Login              string
	Messages           []string
	FileArgs           []string
}

// ValidThinkingLevels lists all valid thinking level values.
var ValidThinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// IsValidThinkingLevel checks if a string is a valid thinking level.
func IsValidThinkingLevel(level string) bool {
	for _, v := range ValidThinkingLevels {
		if v == level {
			return true
		}
	}
	return false
}

// ParseArgs parses CLI arguments into an Args struct.
func ParseArgs(args []string) *Args {
	result := &Args{
		Messages: []string{},
		FileArgs: []string{},
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]

		switch {
		case arg == "--help" || arg == "-h":
			result.Help = true

		case arg == "--version" || arg == "-v":
			result.Version = true

		case arg == "--mode" && i+1 < len(args):
			i++
			mode := args[i]
			if mode == "text" || mode == "json" || mode == "acp" {
				result.OutputMode = Mode(mode)
			}

		case arg == "--continue" || arg == "-c":
			result.Continue = true

		case arg == "--resume" || arg == "-r":
			result.Resume = true

		case arg == "--provider" && i+1 < len(args):
			i++
			result.Provider = args[i]

		case arg == "--model" && i+1 < len(args):
			i++
			result.Model = args[i]

		case arg == "--api-key" && i+1 < len(args):
			i++
			result.ApiKey = args[i]

		case arg == "--system-prompt" && i+1 < len(args):
			i++
			result.SystemPrompt = args[i]

		case arg == "--append-system-prompt" && i+1 < len(args):
			i++
			result.AppendSystemPrompt = args[i]

		case arg == "--no-session":
			result.NoSession = true

		case arg == "--session" && i+1 < len(args):
			i++
			result.Session = args[i]

		case arg == "--session-name" && i+1 < len(args):
			i++
			result.SessionName = args[i]

		case arg == "--session-dir" && i+1 < len(args):
			i++
			result.SessionDir = args[i]

		case arg == "--models" && i+1 < len(args):
			i++
			parts := strings.Split(args[i], ",")
			result.Models = make([]string, 0, len(parts))
			for _, p := range parts {
				s := strings.TrimSpace(p)
				if s != "" {
					result.Models = append(result.Models, s)
				}
			}

		case arg == "--no-tools":
			result.NoTools = true

		case arg == "--no-mcp":
			result.NoMCP = true

		case arg == "--mcp-config" && i+1 < len(args):
			i++
			result.MCPConfig = args[i]

		case arg == "--wait-mcp":
			result.WaitMCP = true

		case arg == "--tools" && i+1 < len(args):
			i++
			parts := strings.Split(args[i], ",")
			result.Tools = make([]string, 0, len(parts))
			for _, p := range parts {
				s := strings.TrimSpace(p)
				if s != "" {
					result.Tools = append(result.Tools, s)
				}
			}

		case arg == "--thinking" && i+1 < len(args):
			i++
			level := args[i]
			if IsValidThinkingLevel(level) {
				result.Thinking = agent.ThinkingLevel(level)
			} else {
				fmt.Fprintf(os.Stderr, "Warning: Invalid thinking level %q. Valid values: %s\n",
					level, strings.Join(ValidThinkingLevels, ", "))
			}

		case arg == "--print" || arg == "-p":
			result.Print = true

		case arg == "--export" && i+1 < len(args):
			i++
			result.Export = args[i]

		case arg == "--no-extensions":
			result.NoExtensions = true

		case (arg == "--extension" || arg == "-e") && i+1 < len(args):
			i++
			result.Extensions = append(result.Extensions, args[i])

		case (arg == "--disable-extension" || arg == "-d") && i+1 < len(args):
			i++
			result.DisabledExtensions = append(result.DisabledExtensions, args[i])

		case arg == "--skill" && i+1 < len(args):
			i++
			result.Skills = append(result.Skills, args[i])

		case arg == "--prompt-template" && i+1 < len(args):
			i++
			result.PromptTemplates = append(result.PromptTemplates, args[i])

		case arg == "--theme" && i+1 < len(args):
			i++
			result.Themes = append(result.Themes, args[i])

		case arg == "--no-skills":
			result.NoSkills = true

		case arg == "--no-prompt-templates":
			result.NoPromptTemplates = true

		case arg == "--no-themes":
			result.NoThemes = true

		case arg == "--list-models":
			// Check if next arg is a search pattern (not a flag or file arg)
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && !strings.HasPrefix(args[i+1], "@") {
				i++
				result.ListModels = args[i]
			} else {
				result.ListModels = true
			}

		case arg == "--list-available-models":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && !strings.HasPrefix(args[i+1], "@") {
				i++
				result.ListAvailModels = args[i]
			} else {
				result.ListAvailModels = true
			}

		case arg == "--verbose":
			result.Verbose = true

		case arg == "--debug":
			result.Debug = true

		case arg == "--debug-log-file" && i+1 < len(args):
			i++
			result.DebugLogFile = args[i]

		case arg == "--login" && i+1 < len(args):
			i++
			result.Login = args[i]

		case strings.HasPrefix(arg, "@"):
			result.FileArgs = append(result.FileArgs, arg[1:]) // Remove @ prefix

		default:
			if !strings.HasPrefix(arg, "-") {
				result.Messages = append(result.Messages, arg)
			}
		}
	}

	// Environment variable fallback: CLI flag wins over env var.
	if result.MCPConfig == "" {
		if v := os.Getenv("FIR_MCP_CONFIG"); v != "" {
			result.MCPConfig = v
		}
	}

	return result
}

// PrintHelp prints the CLI usage information.
func PrintHelp() {
	appName := "fir"

	fmt.Printf(`%s - AI coding assistant with read, bash, edit, write tools

Usage:
  %s [options] [@files...] [messages...]
  %s update                      Self-update to the latest release
  %s skills [list]               List all loaded skills
  %s skills install <name>       Install a builtin skill to project (.fir/skills/)
                                   Options: --user (install to ~/.config/fir/skills/), --force
  %s extensions [list]           List all builtin extensions
  %s extensions install <name>   Install a builtin extension to project (.fir/extensions/)
                                   Options: --user (install to ~/.config/fir/extensions/), --force
  %s install <source> [--local]  Install a package (git repo or local path)
                                   --local installs to project scope (.fir/packages/)
  %s uninstall <source> [--local] Remove an installed package
  %s packages [list]             List installed packages
  %s packages update [source]    Update one or all installed packages
  %s sessions [list]             List sessions associated with the current directory
  %s login <provider-id>         OAuth login for a provider (auth extensions loaded)
  %s login list                  List available OAuth providers
  %s completion <bash|zsh>       Print shell completion script

Options:
  -C <dir>                       Run as if fir was started in <dir>
  --provider <name>              Provider name (default: from settings, else first provider with a valid API key)
  --model <id>                   Model ID
  --api-key <key>                API key (defaults to env vars)
  --system-prompt <text>         System prompt (default: coding assistant prompt)
  --append-system-prompt <text>  Append text or file contents to the system prompt
  --mode <mode>                  Output mode: text (default), json, or acp
  --print, -p                    Non-interactive mode: process prompt and exit
  --continue, -c                 Continue previous session
  --resume, -r                   Select a session to resume
  --session <path>               Use specific session file
  --session-name <name>          Set display name for the session
  --session-dir <dir>            Directory for session storage and lookup
  --no-session                   Don't save session (ephemeral)
  --models <patterns>            Comma-separated model patterns for cycling
  --no-tools                     Disable all built-in tools
  --no-mcp                       Disable MCP server integration
  --mcp-config <path>            Load additional MCP config file (highest precedence)
                                 Also: FIR_MCP_CONFIG env var (CLI flag wins)
  --wait-mcp                     Block until all MCP servers have finished
                                 their initial handshake (up to 30s) before the
                                 first prompt. Applies in all modes — print/JSON
                                 always waits; interactive and ACP opt in here.
  --tools <tools>                Comma-separated list of tools to enable
                                 Available: read, bash, edit, write, grep, find, ls
  --thinking <level>             Set thinking level: off, minimal, low, medium, high, xhigh, max
  --no-extensions                Disable all extensions (overrides config)
  --extension <name>, -e <name>  Enable a specific extension by name (repeatable; overrides config)
                                 When any --extension flag is set, only named extensions are started
  --disable-extension <name>,    Disable a specific extension by name (repeatable)
    -d <name>
  --skill <path>                 Load a skill file or directory
  --no-skills                    Disable skills
  --prompt-template <path>       Load a prompt template file or directory
  --no-prompt-templates          Disable prompt template discovery
  --theme <path>                 Load a theme file or directory
  --no-themes                    Disable theme discovery
  --export <file>                Export session file to HTML and exit
  --list-models [search]         List available models (with optional fuzzy search)
  --verbose                      Force verbose startup
  --debug                        Enable debug logging to file
  --debug-log-file <path>        Debug log path (default: ~/.config/fir/debug.log)
  --help, -h                     Show this help
  --version, -v                  Show version number

Examples:
  # Interactive mode
  %s

  # Non-interactive mode (process and exit)
  %s -p "List all .ts files in src/"

  # Include files in initial message
  %s @prompt.md @image.png "What color is the sky?"

  # Continue previous session
  %s --continue "What did we discuss?"

  # Use different model
  %s --provider openai --model gpt-4o-mini "Help me refactor this code"

  # Start with a specific thinking level
  %s --thinking high "Solve this complex problem"

  # Update to the latest release
  %s update

Environment Variables:
%s
`, appName, appName, appName, appName, appName, appName, appName, appName, appName, appName, appName, appName, appName, appName, appName, appName, appName, appName, appName, appName, appName, appName, envvars.FormatHelpText())
}
