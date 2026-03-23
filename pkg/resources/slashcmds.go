// Ported from: packages/coding-agent/src/core/slash-commands.ts
// Upstream hash: 1caadb2e
package resources

// SlashCommandSource identifies where a slash command came from.
type SlashCommandSource string

const (
	SlashCommandSourceExtension SlashCommandSource = "extension"
	SlashCommandSourcePrompt    SlashCommandSource = "prompt"
	SlashCommandSourceSkill     SlashCommandSource = "skill"
)

// SlashCommandLocation identifies the scope of a slash command.
type SlashCommandLocation string

const (
	SlashCommandLocationUser    SlashCommandLocation = "user"
	SlashCommandLocationProject SlashCommandLocation = "project"
	SlashCommandLocationPath    SlashCommandLocation = "path"
)

// SlashCommandInfo describes a slash command, including user-defined ones.
type SlashCommandInfo struct {
	Name        string               `json:"name"`
	Description string               `json:"description,omitempty"`
	Source      SlashCommandSource   `json:"source"`
	Location    SlashCommandLocation `json:"location,omitempty"`
	Path        string               `json:"path,omitempty"`
}

// BuiltinSlashCommand describes a built-in slash command.
type BuiltinSlashCommand struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// BuiltinSlashCommands is the list of all built-in slash commands.
// This is the single source of truth: both autocomplete and command dispatch
// are derived from it via IsBuiltinSlashCommandName.
var BuiltinSlashCommands = []BuiltinSlashCommand{
	{Name: "help", Description: "Show help"},
	{Name: "theme", Description: "Select color theme"},
	{Name: "thinking", Description: "Select thinking level"},
	{Name: "model", Description: "Select model (opens selector UI)"},
	{Name: "settings", Description: "Open settings menu"},
	{Name: "session", Description: "Show session info and stats"},
	{Name: "new", Description: "Start a new session (optionally with a name)"},
	{Name: "compact", Description: "Manually compact the session context"},
	{Name: "resume", Description: "Resume a different session"},
	{Name: "tree", Description: "Navigate session tree (switch branches)"},
	{Name: "export", Description: "Export session to HTML file"},
	{Name: "share", Description: "Share session as a secret GitHub gist"},
	{Name: "name", Description: "Set session display name"},
	{Name: "changelog", Description: "Show changelog entries"},
	{Name: "login", Description: "Login with OAuth provider"},
	{Name: "logout", Description: "Logout from OAuth provider"},
	{Name: "reload", Description: "Reload extensions, skills, prompts, and themes"},
	{Name: "skills", Description: "List loaded skills, or install a builtin skill"},
	{Name: "update", Description: "Update fir to the latest version in-place and restart"},
	{Name: "reexec", Description: "Re-exec into the current or a specified binary, preserving the session, message queue, and pending input"},
	{Name: "queue", Description: "Show the follow-up message queue"},
	{Name: "dequeue", Description: "Restore queued messages to the editor (/dequeue [N] removes item N)"},
	{Name: "plan", Description: "Show/hide the current session plan"},
	{Name: "quit", Description: "Quit fir"},
}

// builtinAliases are command names that are recognized as builtins but are not
// surfaced in autocomplete (they are undocumented aliases for listed commands).
var builtinAliases = map[string]bool{
	"exit": true, // alias for /quit
}

// builtinSlashCommandSet is the full lookup set: all listed commands + aliases.
var builtinSlashCommandSet = func() map[string]bool {
	m := make(map[string]bool, len(BuiltinSlashCommands)+len(builtinAliases))
	for _, cmd := range BuiltinSlashCommands {
		m[cmd.Name] = true
	}
	for name := range builtinAliases {
		m[name] = true
	}
	return m
}()

// IsBuiltinSlashCommandName reports whether name (without leading "/") is a
// recognized builtin slash command or alias.
func IsBuiltinSlashCommandName(name string) bool {
	return builtinSlashCommandSet[name]
}
