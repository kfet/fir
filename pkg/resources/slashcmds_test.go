// Ported from: packages/coding-agent/src/core/slash-commands.ts
// Upstream hash: 1caadb2e
package resources

import "testing"

func TestBuiltinSlashCommands_NotEmpty(t *testing.T) {
	if len(BuiltinSlashCommands) == 0 {
		t.Error("expected non-empty builtin slash commands")
	}
}

func TestBuiltinSlashCommands_AllHaveNames(t *testing.T) {
	for _, cmd := range BuiltinSlashCommands {
		if cmd.Name == "" {
			t.Error("found slash command with empty name")
		}
		if cmd.Description == "" {
			t.Errorf("slash command %q has empty description", cmd.Name)
		}
	}
}

func TestBuiltinSlashCommands_ContainsKey(t *testing.T) {
	expected := map[string]bool{
		"settings": true,
		"model":    true,
		"quit":     true,
		"new":      true,
		"compact":  true,
	}
	found := make(map[string]bool)
	for _, cmd := range BuiltinSlashCommands {
		found[cmd.Name] = true
	}
	for name := range expected {
		if !found[name] {
			t.Errorf("expected builtin command %q not found", name)
		}
	}
}

func TestBuiltinSlashCommands_NoDuplicates(t *testing.T) {
	seen := make(map[string]bool)
	for _, cmd := range BuiltinSlashCommands {
		if seen[cmd.Name] {
			t.Errorf("duplicate slash command: %q", cmd.Name)
		}
		seen[cmd.Name] = true
	}
}

func TestSlashCommandSource_Values(t *testing.T) {
	if SlashCommandSourceExtension != "extension" {
		t.Error("unexpected extension value")
	}
	if SlashCommandSourcePrompt != "prompt" {
		t.Error("unexpected prompt value")
	}
	if SlashCommandSourceSkill != "skill" {
		t.Error("unexpected skill value")
	}
}

func TestSlashCommandLocation_Values(t *testing.T) {
	if SlashCommandLocationUser != "user" {
		t.Error("unexpected user value")
	}
	if SlashCommandLocationProject != "project" {
		t.Error("unexpected project value")
	}
	if SlashCommandLocationPath != "path" {
		t.Error("unexpected path value")
	}
}
