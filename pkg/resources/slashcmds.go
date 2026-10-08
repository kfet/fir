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
