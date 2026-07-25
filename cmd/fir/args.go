// Ported from: packages/coding-agent/src/cli/args.ts
// Upstream hash: 1caadb2e
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kfet/agent"
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
	AgentDir           string
	Models             []string
	Tools              []string
	NoTools            bool
	NoMCP              bool
	MCPConfig          string
	WaitMCP            bool
	// ACPSessionIdleTTL is how long an ACP in-memory session may sit idle
	// before the background reaper tears it down. Defaults to 1h; 0 disables.
	ACPSessionIdleTTL  time.Duration
	Extensions         []string
	DisabledExtensions []string
	NoExtensions       bool
	Print              bool
	Export             string
	NoSkills           bool
	Skills             []string
	Themes             []string
	NoThemes           bool
	ListModels         any // true (bool) or string (search pattern)
	ListAvailModels    any // true (bool) or string (search pattern)
	Verbose            bool
	// VerboseCount is the number of -v occurrences. 0=normal, 1=Debug,
	// 2+=Trace. Values >2 are clamped to 2 with a warning.
	VerboseCount int
	// LegacyVersionHint is set when the user appears to have invoked the
	// old `-v` alias for --version (i.e. lone `-v` with nothing else). The
	// app surfaces a migration note pointing at -V / --version.
	LegacyVersionHint bool
	Debug             bool
	DebugLogFile      string
	Login             string
	Messages          []string
	FileArgs          []string

	// Seen records which CLI flags appeared in the argv that produced this
	// Args. Used by --continue / --resume merge logic to distinguish "field
	// is at zero value because user didn't pass the flag" from "user
	// explicitly passed it". Keys are canonical long-form flag names
	// (e.g. "--model", "--mcp-config", "--no-extensions").
	Seen map[string]bool

	// NoRestoreConfig disables the default `-c` / `-r` behaviour of
	// re-applying the persisted SessionInvocation from the resumed session.
	NoRestoreConfig bool
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

// isAllVs reports whether s consists entirely of the byte 'v' (len >= 1).
func isAllVs(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] != 'v' {
			return false
		}
	}
	return true
}

// ParseArgs parses CLI arguments into an Args struct.
func ParseArgs(args []string) *Args {
	result := &Args{
		Messages:          []string{},
		FileArgs:          []string{},
		Seen:              make(map[string]bool),
		ACPSessionIdleTTL: 15 * time.Minute,
	}
	mark := func(flag string) { result.Seen[flag] = true }

	for i := 0; i < len(args); i++ {
		arg := args[i]

		switch {
		case arg == "--help" || arg == "-h":
			result.Help = true

		case arg == "--version" || arg == "-V":
			result.Version = true

		// Repeated short verbose flag: -v / -vv / -vvv ... Each `v` adds
		// one level. Pure-v short flags only (so we don't shadow -vvv-debug
		// or similar) — and never the long --version (handled above).
		case len(arg) >= 2 && arg[0] == '-' && arg[1] != '-' && isAllVs(arg[1:]):
			result.VerboseCount += len(arg) - 1

		case arg == "--mode" && i+1 < len(args):
			i++
			mode := args[i]
			if mode == "text" || mode == "json" || mode == "acp" {
				result.OutputMode = Mode(mode)
			}

		case arg == "--continue" || arg == "-c":
			result.Continue = true
			mark("--continue")

		case arg == "--resume" || arg == "-r":
			result.Resume = true
			mark("--resume")

		case arg == "--no-restore-config":
			result.NoRestoreConfig = true
			mark("--no-restore-config")

		case arg == "--provider" && i+1 < len(args):
			i++
			result.Provider = args[i]
			mark("--provider")

		case arg == "--model" && i+1 < len(args):
			i++
			result.Model = args[i]
			mark("--model")

		case arg == "--api-key" && i+1 < len(args):
			i++
			result.ApiKey = args[i]
			mark("--api-key")

		case arg == "--system-prompt" && i+1 < len(args):
			i++
			result.SystemPrompt = args[i]
			mark("--system-prompt")

		case arg == "--append-system-prompt" && i+1 < len(args):
			i++
			result.AppendSystemPrompt = args[i]
			mark("--append-system-prompt")

		case arg == "--no-session":
			result.NoSession = true
			mark("--no-session")

		case arg == "--session" && i+1 < len(args):
			i++
			result.Session = args[i]
			mark("--session")

		case (arg == "--session-name" || arg == "-n") && i+1 < len(args):
			i++
			result.SessionName = args[i]
			mark("--session-name")

		case arg == "--session-dir" && i+1 < len(args):
			i++
			result.SessionDir = args[i]
			mark("--session-dir")

		case arg == "--agent-dir" && i+1 < len(args):
			i++
			result.AgentDir = args[i]
			mark("--agent-dir")

		case strings.HasPrefix(arg, "--agent-dir="):
			result.AgentDir = strings.TrimPrefix(arg, "--agent-dir=")
			mark("--agent-dir")

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
			mark("--models")

		case arg == "--no-tools":
			result.NoTools = true
			mark("--no-tools")

		case arg == "--no-mcp":
			result.NoMCP = true
			mark("--no-mcp")

		case arg == "--mcp-config" && i+1 < len(args):
			i++
			result.MCPConfig = args[i]
			mark("--mcp-config")

		case arg == "--wait-mcp":
			result.WaitMCP = true
			mark("--wait-mcp")

		case arg == "--acp-session-idle-ttl" && i+1 < len(args):
			i++
			if d, err := time.ParseDuration(args[i]); err == nil {
				result.ACPSessionIdleTTL = d
				mark("--acp-session-idle-ttl")
			} else {
				fmt.Fprintf(os.Stderr, "Warning: invalid --acp-session-idle-ttl %q (want a Go duration like 1h, 30m, 0): %v\n", args[i], err)
			}

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
			mark("--tools")

		case arg == "--thinking" && i+1 < len(args):
			i++
			level := args[i]
			if IsValidThinkingLevel(level) {
				result.Thinking = agent.ThinkingLevel(level)
				mark("--thinking")
			} else {
				fmt.Fprintf(os.Stderr, "Warning: Invalid thinking level %q. Valid values: %s\n",
					level, strings.Join(ValidThinkingLevels, ", "))
			}

		case arg == "--print" || arg == "-p":
			result.Print = true
			mark("--print")

		case arg == "--export" && i+1 < len(args):
			i++
			result.Export = args[i]
			mark("--export")

		case arg == "--no-extensions":
			result.NoExtensions = true
			mark("--no-extensions")

		case (arg == "--extension" || arg == "-e") && i+1 < len(args):
			i++
			result.Extensions = append(result.Extensions, args[i])
			mark("--extension")

		case (arg == "--disable-extension" || arg == "-d") && i+1 < len(args):
			i++
			result.DisabledExtensions = append(result.DisabledExtensions, args[i])
			mark("--disable-extension")

		case arg == "--skill" && i+1 < len(args):
			i++
			result.Skills = append(result.Skills, args[i])
			mark("--skill")

		case arg == "--theme" && i+1 < len(args):
			i++
			result.Themes = append(result.Themes, args[i])
			mark("--theme")

		case arg == "--no-skills":
			result.NoSkills = true
			mark("--no-skills")

		case arg == "--no-themes":
			result.NoThemes = true
			mark("--no-themes")

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

	// Legacy migration: `fir -v` used to print the version. Now it enables
	// verbose logging. Detect a lone `-v` (single v, no positional args,
	// no actions) and flag it so the app can print a migration hint.
	if result.VerboseCount == 1 && len(args) == 1 && args[0] == "-v" {
		result.LegacyVersionHint = true
	}

	return result
}

// PrintHelp prints the CLI usage information.
func PrintHelp() {
	appName := "fir"

	// Header + subcommand list (registry-driven — add new cmds in subcommands.go).
	fmt.Printf("%s - AI coding assistant with read, bash, edit, write tools\n\nUsage:\n", appName)
	fmt.Printf("  %s [options] [@files...] [messages...]\n", appName)
	const syntaxW = 31
	fmt.Printf("  %-*s %s\n", syntaxW, appName+" /<skill> [task...]", "Invoke a skill by name with the given task")
	for _, sc := range subcommands {
		for _, row := range sc.Help {
			syntax, summary := row[0], row[1]
			if syntax == "" {
				fmt.Printf("  %-*s %s\n", syntaxW, "", summary)
				continue
			}
			fmt.Printf("  %-*s %s\n", syntaxW, syntax, summary)
		}
	}

	fmt.Printf(`
Options:
  -C <dir>                       Run as if %s was started in <dir>
  --agent-dir <dir>              Override the fir config/session root
                                 (same as FIR_AGENT_DIR; CLI flag wins)
  --provider <name>              Provider name (default: from settings, else first provider with a valid API key)
  --model <id>                   Model ID
  --api-key <key>                API key (defaults to env vars)
  --system-prompt <text>         System prompt (default: coding assistant prompt)
  --append-system-prompt <text>  Append text or file contents to the system prompt
  --mode <mode>                  Output mode: text (default), json, or acp
  --print, -p                    Non-interactive mode: process prompt and exit
  --continue, -c                 Continue previous session (restores its
                                 original --mcp-config / --extension /
                                 --model / etc. by default; pass
                                 --no-restore-config to opt out)
  --resume, -r                   Select a session to resume
  --no-restore-config            With -c / -r / /resume, do not re-apply
                                 the resumed session's recorded invocation
                                 config (start with only the current argv)
  --session <path>               Use specific session file
  --session-name <name>, -n      Set display name for the session
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
  --acp-session-idle-ttl <dur>   ACP mode: tear down in-memory sessions idle
                                 longer than <dur> (e.g. 1h, 30m). Default 15m;
                                 0 disables the idle reaper. Idle sessions cost
                                 zero sidecars; the next prompt re-hydrates them
                                 transparently under the same sessionId.
  --tools <tools>                Comma-separated list of tools to enable
                                 Available: %s
  --thinking <level>             Set thinking level: off, minimal, low, medium, high, xhigh, max
  --no-extensions                Disable all extensions (overrides config)
  --extension <name>, -e <name>  Enable a specific extension by name (repeatable; overrides config)
                                 When any --extension flag is set, only named extensions are started
  --disable-extension <name>,    Disable a specific extension by name (repeatable)
    -d <name>
  --skill <path>                 Load a skill file or directory
  --no-skills                    Disable skills
  --theme <path>                 Load a theme file or directory
  --no-themes                    Disable theme discovery
  --export <file>                Export session file to HTML and exit
  --list-models [search]         List available models (with optional fuzzy search)
                                 Add --verbose to show each model's origin
                                 (builtin / overlay / user)
  --verbose                      Force verbose startup
  -v, -vv                        Increase log verbosity (-v: Debug, -vv: Trace).
                                 Also: FIR_LOG_LEVEL=info|debug|trace
  --debug                        Enable debug logging to file (same as -v)
  --debug-log-file <path>        Debug log path (default: <agent-dir>/debug.log)
  --help, -h                     Show this help
  -V, --version                  Show version number

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
`, appName, strings.Join(allToolNames(), ", "),
		appName, appName, appName, appName, appName, appName, appName,
		envvars.FormatHelpText())
}
