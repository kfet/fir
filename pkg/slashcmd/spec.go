// Package slashcmd is the single, mode-neutral registry of fir's builtin slash
// commands.
//
// Every builtin command — its name, description, subcommands and the modes it
// is available in — is declared once in Specs. Handlers are bound to a
// Registry: shared handlers (pkg/slashcmd/builtin) run unchanged in every
// mode and talk to the user only through the Output interface, while each
// mode (TUI, ACP) is a thin adapter that implements Output and binds handlers
// for the commands declared mode-only. Registry.Validate enforces parity: a
// command or subcommand declared for a mode must have a handler in that mode.
//
// This mirrors how extensions register commands (a name + description spec,
// dispatched by name with string args) so builtin and extension commands are
// described the same way to every client.
//
// The package is a leaf: it must not import other fir packages, so that the
// extension manager (which session depends on) can use it to reject command
// name collisions.
package slashcmd

import (
	"fmt"
	"strings"
)

// Mode is a bitmask of the front-ends a command is available in.
type Mode uint8

const (
	// TUI is the interactive terminal mode.
	TUI Mode = 1 << iota
	// ACP is the Agent Client Protocol mode.
	ACP

	// All marks a command available in every mode. It is the default when a
	// Spec or Sub leaves Modes unset.
	All = TUI | ACP
)

// Modes lists every concrete mode, for iteration.
var Modes = []Mode{TUI, ACP}

func (m Mode) String() string {
	switch m {
	case TUI:
		return "tui"
	case ACP:
		return "acp"
	case All:
		return "all"
	}
	return fmt.Sprintf("mode(%d)", uint8(m))
}

// Has reports whether m includes mode.
func (m Mode) Has(mode Mode) bool { return m.orAll()&mode != 0 }

func (m Mode) orAll() Mode {
	if m == 0 {
		return All
	}
	return m
}

// Sub declares a subcommand: `/<cmd> <sub> [args]`. A subcommand exists in
// exactly the modes its parent does.
type Sub struct {
	Name        string
	Description string
}

// Spec declares one builtin slash command.
type Spec struct {
	Name        string
	Description string
	// Usage is a short argument synopsis, e.g. "[provider-id]".
	Usage string
	// Modes restricts the command to some front-ends; zero means All. A
	// command available in fewer than all modes is "mode-only" and the parity
	// test accepts it existing in just those modes.
	Modes Mode
	// Aliases are hidden alternate names (e.g. "exit" for "quit").
	Aliases []string
	Subs    []Sub
}

// Available reports whether the command exists in mode.
func (s Spec) Available(mode Mode) bool { return s.Modes.Has(mode) }

// Specs is the single source of truth for builtin slash commands. Order is
// the display order used by autocomplete, /help and ACP available_commands.
var Specs = []Spec{
	{Name: "help", Description: "Show available commands and keyboard shortcuts"},
	{Name: "theme", Description: "Select color theme", Modes: TUI},
	{Name: "thinking", Description: "Select thinking level", Modes: TUI},
	{Name: "model", Description: "Select model (opens selector UI)", Usage: "[search]", Modes: TUI},
	{Name: "settings", Description: "Open settings menu", Modes: TUI},
	{Name: "session", Description: "Show session info and stats"},
	{Name: "new", Description: "Start a new session (optionally with an initial prompt)", Usage: "[prompt]", Modes: TUI},
	{Name: "compact", Description: "Compact the session context to save tokens", Usage: "[instructions]"},
	{Name: "resume", Description: "Resume a different session", Usage: "[number|path] [--at <entry-id>]"},
	{Name: "continue", Description: "Continue the most recent session", Modes: ACP},
	{Name: "tree", Description: "Navigate session tree (switch branches)", Modes: TUI},
	{Name: "export", Description: "Export session to an HTML file", Usage: "[path]"},
	{Name: "share", Description: "Share session as a secret GitHub gist with a preview link"},
	{Name: "name", Description: "Show or set the session display name", Usage: "[name]"},
	{Name: "changelog", Description: "Show recent changelog entries", Usage: "[N|all]"},
	{Name: "login", Description: "Login with OAuth provider", Usage: "[provider-id]"},
	{Name: "logout", Description: "Logout from OAuth provider", Usage: "[provider-id|all]"},
	{Name: "reload", Description: "Reload extensions, skills, themes, MCP servers, and provider auth"},
	{Name: "skills", Description: "List loaded skills, show one, or install a builtin skill", Usage: "[<name>]", Subs: []Sub{
		{Name: "list", Description: "List loaded skills"},
		{Name: "install", Description: "Install a builtin skill: install <name> [--user] [--force]"},
	}},
	{Name: "update", Description: "Update fir to the latest version in-place and restart", Modes: TUI},
	{Name: "reexec", Description: "Re-exec into the current or a specified binary, preserving the session, message queue, and pending input. Usage: /reexec [<path>] [-- <prompt>]  |  /reexec <prompt>", Modes: TUI},
	{Name: "queue", Description: "Show the follow-up message queue", Modes: TUI},
	{Name: "dequeue", Description: "Restore queued messages to the editor (/dequeue [N] removes item N)", Usage: "[N]", Modes: TUI},
	{Name: "plan", Description: "Show/hide the current session plan", Modes: TUI},
	{Name: "sections", Description: "Show the persistent extension sections fir injects"},
	{Name: "mcp", Description: "Show MCP servers; /mcp <name> for details", Usage: "[<name>]", Subs: []Sub{
		{Name: "reload", Description: "Reload MCP server configs"},
		{Name: "login", Description: "Authenticate a remote MCP server: login <server>"},
		{Name: "logout", Description: "Remove stored MCP credentials: logout <server>"},
	}},
	{Name: "quit", Description: "Quit fir", Modes: TUI, Aliases: []string{"exit"}},
}

var byName = mustIndex(Specs)

func mustIndex(specs []Spec) map[string]*Spec {
	m, err := index(specs)
	if err != nil {
		panic(err)
	}
	return m
}

// index maps every name and alias to its spec, rejecting duplicates.
func index(specs []Spec) (map[string]*Spec, error) {
	m := make(map[string]*Spec)
	for i := range specs {
		s := &specs[i]
		for _, n := range append([]string{s.Name}, s.Aliases...) {
			if _, dup := m[n]; dup {
				return nil, fmt.Errorf("duplicate builtin slash command: %q", n)
			}
			m[n] = s
		}
		seen := map[string]bool{}
		for _, sub := range s.Subs {
			if seen[sub.Name] {
				return nil, fmt.Errorf("duplicate subcommand %q in /%s", sub.Name, s.Name)
			}
			seen[sub.Name] = true
		}
	}
	return m, nil
}

// Lookup resolves a command name or alias (without the leading "/").
func Lookup(name string) (Spec, bool) {
	s, ok := byName[name]
	if !ok {
		return Spec{}, false
	}
	return *s, true
}

// IsBuiltinName reports whether name (without "/") is a builtin command or
// alias in any mode. Extensions may not register these names.
func IsBuiltinName(name string) bool {
	_, ok := byName[name]
	return ok
}

// ForMode returns the commands available in mode, in display order.
func ForMode(mode Mode) []Spec {
	var out []Spec
	for _, s := range Specs {
		if s.Available(mode) {
			out = append(out, s)
		}
	}
	return out
}

// FullDescription is the description with usage and subcommands folded in,
// for clients (ACP available_commands, autocomplete) that show one line.
func (s Spec) FullDescription(mode Mode) string {
	desc := s.Description
	if s.Usage != "" {
		desc += " (usage: /" + s.Name + " " + s.Usage + ")"
	}
	var subs []string
	for _, sub := range s.Subs {
		subs = append(subs, sub.Name)
	}
	if len(subs) > 0 {
		desc += " (subcommands: " + strings.Join(subs, ", ") + ")"
	}
	return desc
}

// HelpText renders the command list for mode, one command per line followed
// by indented subcommands.
func HelpText(mode Mode) string {
	specs := ForMode(mode)
	width := 0
	heads := make([]string, len(specs))
	for i, s := range specs {
		h := "/" + s.Name
		if s.Usage != "" {
			h += " " + s.Usage
		}
		heads[i] = h
		if len(h) > width {
			width = len(h)
		}
	}
	var sb strings.Builder
	sb.WriteString("Available commands:\n")
	for i, s := range specs {
		fmt.Fprintf(&sb, "  %-*s - %s\n", width, heads[i], s.Description)
		for _, sub := range s.Subs {
			fmt.Fprintf(&sb, "  %-*s - %s\n", width, "  "+sub.Name, sub.Description)
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}
