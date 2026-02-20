// Ported from: packages/coding-agent/src/cli/args.ts
// Upstream hash: 1caadb2e
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/kfet/tau/pkg/agent"
)

// Mode is the output mode for tau.
type Mode string

const (
	ModeText Mode = "text"
	ModeJSON Mode = "json"
	ModeRPC  Mode = "rpc"
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
	SessionDir         string
	Models             []string
	Tools              []string
	NoTools            bool
	Extensions         []string
	NoExtensions       bool
	Print              bool
	Export             string
	NoSkills           bool
	Skills             []string
	PromptTemplates    []string
	NoPromptTemplates  bool
	Themes             []string
	NoThemes           bool
	ListModels         interface{} // true (bool) or string (search pattern)
	Verbose            bool
	Messages           []string
	FileArgs           []string
	UnknownFlags       map[string]interface{} // bool or string values; includes extension flags
}

// ValidThinkingLevels lists all valid thinking level values.
var ValidThinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh"}

// IsValidThinkingLevel checks if a string is a valid thinking level.
func IsValidThinkingLevel(level string) bool {
	for _, v := range ValidThinkingLevels {
		if v == level {
			return true
		}
	}
	return false
}

// ExtensionFlagDef describes an extension-registered flag.
type ExtensionFlagDef struct {
	Type string // "boolean" or "string"
}

// ParseArgs parses CLI arguments into an Args struct.
func ParseArgs(args []string, extensionFlags map[string]ExtensionFlagDef) *Args {
	result := &Args{
		Messages:     []string{},
		FileArgs:     []string{},
		UnknownFlags: make(map[string]interface{}),
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
			if mode == "text" || mode == "json" || mode == "rpc" || mode == "acp" {
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

		case (arg == "--extension" || arg == "-e") && i+1 < len(args):
			i++
			result.Extensions = append(result.Extensions, args[i])

		case arg == "--no-extensions":
			result.NoExtensions = true

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

		case arg == "--verbose":
			result.Verbose = true

		case strings.HasPrefix(arg, "@"):
			result.FileArgs = append(result.FileArgs, arg[1:]) // Remove @ prefix

		case strings.HasPrefix(arg, "--"):
			flagName := arg[2:]
			if extensionFlags != nil {
				if def, ok := extensionFlags[flagName]; ok {
					if def.Type == "boolean" {
						result.UnknownFlags[flagName] = true
					} else if def.Type == "string" && i+1 < len(args) {
						i++
						result.UnknownFlags[flagName] = args[i]
					}
					break
				}
			}
			// No extension flag definition — heuristically capture unknown flags.
			// If the next arg looks like a value (not a flag or file arg), consume it.
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && !strings.HasPrefix(args[i+1], "@") {
				i++
				result.UnknownFlags[flagName] = args[i]
			} else {
				result.UnknownFlags[flagName] = true
			}

		default:
			if !strings.HasPrefix(arg, "-") {
				result.Messages = append(result.Messages, arg)
			}
		}
	}

	return result
}

// PrintHelp prints the CLI usage information.
func PrintHelp() {
	appName := "tau"
	configDir := ".tau"
	envAgentDir := "TAU_AGENT_DIR"

	fmt.Printf(`%s - AI coding assistant with read, bash, edit, write tools

Usage:
  %s [options] [@files...] [messages...]

Options:
  --provider <name>              Provider name (default: google)
  --model <id>                   Model ID
  --api-key <key>                API key (defaults to env vars)
  --system-prompt <text>         System prompt (default: coding assistant prompt)
  --append-system-prompt <text>  Append text or file contents to the system prompt
  --mode <mode>                  Output mode: text (default), json, rpc, or acp
  --print, -p                    Non-interactive mode: process prompt and exit
  --continue, -c                 Continue previous session
  --resume, -r                   Select a session to resume
  --session <path>               Use specific session file
  --session-dir <dir>            Directory for session storage and lookup
  --no-session                   Don't save session (ephemeral)
  --models <patterns>            Comma-separated model patterns for cycling
  --no-tools                     Disable all built-in tools
  --tools <tools>                Comma-separated list of tools to enable
                                 Available: read, bash, edit, write, grep, find, ls
  --thinking <level>             Set thinking level: off, minimal, low, medium, high, xhigh
  --extension, -e <name>         Enable an extension by name (can be used multiple times)
  --no-extensions                Disable all extensions (overrides config)
  --skill <path>                 Load a skill file or directory
  --no-skills                    Disable skills
  --prompt-template <path>       Load a prompt template file or directory
  --no-prompt-templates          Disable prompt template discovery
  --theme <path>                 Load a theme file or directory
  --no-themes                    Disable theme discovery
  --export <file>                Export session file to HTML and exit
  --list-models [search]         List available models (with optional fuzzy search)
  --verbose                      Force verbose startup
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

Environment Variables:
  ANTHROPIC_API_KEY                - Anthropic Claude API key
  OPENAI_API_KEY                   - OpenAI GPT API key
  GEMINI_API_KEY                   - Google Gemini API key
  GROQ_API_KEY                     - Groq API key
  XAI_API_KEY                      - xAI Grok API key
  OPENROUTER_API_KEY               - OpenRouter API key
  MISTRAL_API_KEY                  - Mistral API key
  AWS_PROFILE                      - AWS profile for Amazon Bedrock
  %-32s - Session storage directory (default: ~/%s/agent)

Available Tools (default: read, bash, edit, write):
  read   - Read file contents
  bash   - Execute bash commands
  edit   - Edit files with find/replace
  write  - Write files (creates/overwrites)
  grep   - Search file contents (read-only, off by default)
  find   - Find files by glob pattern (read-only, off by default)
  ls     - List directory contents (read-only, off by default)
`, appName, appName, appName, appName, appName, appName, appName, appName, envAgentDir, configDir)
}
