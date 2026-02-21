// Ported from: packages/coding-agent/src/core/slash-commands.ts
// Upstream hash: 1caadb2e
package core

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
var BuiltinSlashCommands = []BuiltinSlashCommand{
	{Name: "settings", Description: "Open settings menu"},
	{Name: "model", Description: "Select model (opens selector UI)"},
	{Name: "scoped-models", Description: "Enable/disable models for Ctrl+P cycling"},
	{Name: "export", Description: "Export session to HTML file"},
	{Name: "share", Description: "Share session as a secret GitHub gist"},
	{Name: "copy", Description: "Copy last agent message to clipboard"},
	{Name: "name", Description: "Set session display name"},
	{Name: "session", Description: "Show session info and stats"},
	{Name: "changelog", Description: "Show changelog entries"},
	{Name: "hotkeys", Description: "Show all keyboard shortcuts"},
	{Name: "fork", Description: "Create a new fork from a previous message"},
	{Name: "tree", Description: "Navigate session tree (switch branches)"},
	{Name: "login", Description: "Login with OAuth provider"},
	{Name: "logout", Description: "Logout from OAuth provider"},
	{Name: "new", Description: "Start a new session"},
	{Name: "compact", Description: "Manually compact the session context"},
	{Name: "resume", Description: "Resume a different session"},
	{Name: "reload", Description: "Reload extensions, skills, prompts, and themes"},
	{Name: "quit", Description: "Quit fir"},
}
