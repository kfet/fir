package interactive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tuicomp "github.com/kfet/pi-go/pkg/tui/components"
)

func testCommands() []SlashCommand {
	return []SlashCommand{
		{Name: "help", Description: "Show help"},
		{Name: "hotkeys", Description: "Show keyboard shortcuts"},
		{Name: "model", Description: "Select model"},
		{Name: "thinking", Description: "Select thinking level"},
		{Name: "quit", Description: "Quit pi"},
		{Name: "login", Description: "Login with OAuth"},
		{Name: "logout", Description: "Logout from OAuth"},
		{Name: "clear", Description: "Start new session"},
		{Name: "compact", Description: "Compact session"},
		{Name: "settings", Description: "Open settings"},
	}
}

func TestAutocomplete_SlashCommandSuggestions(t *testing.T) {
	p := NewCombinedAutocompleteProvider(testCommands(), "/tmp")

	// Typing "/" should suggest all commands
	result := p.GetSuggestions([]string{"/"}, 0, 1)
	if result == nil {
		t.Fatal("expected suggestions for /")
	}
	if len(result.Items) != len(testCommands()) {
		t.Errorf("expected %d items, got %d", len(testCommands()), len(result.Items))
	}
	if result.Prefix != "/" {
		t.Errorf("expected prefix '/', got %q", result.Prefix)
	}
}

func TestAutocomplete_SlashCommandFuzzy(t *testing.T) {
	p := NewCombinedAutocompleteProvider(testCommands(), "/tmp")

	// "/he" should match "help"
	result := p.GetSuggestions([]string{"/he"}, 0, 3)
	if result == nil {
		t.Fatal("expected suggestions for /he")
	}
	if len(result.Items) == 0 {
		t.Fatal("expected at least one match for /he")
	}
	found := false
	for _, item := range result.Items {
		if item.Value == "help" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'help' in suggestions for /he")
	}
}

func TestAutocomplete_SlashCommandNoMatch(t *testing.T) {
	p := NewCombinedAutocompleteProvider(testCommands(), "/tmp")

	// "/zzz" should match nothing
	result := p.GetSuggestions([]string{"/zzz"}, 0, 4)
	if result != nil {
		t.Errorf("expected nil for /zzz, got %d items", len(result.Items))
	}
}

func TestAutocomplete_NormalTextNoSuggestions(t *testing.T) {
	p := NewCombinedAutocompleteProvider(testCommands(), "/tmp")

	// Regular text should not trigger suggestions
	result := p.GetSuggestions([]string{"hello world"}, 0, 11)
	if result != nil {
		t.Error("expected no suggestions for normal text")
	}
}

func TestAutocomplete_EmptyLineNoSuggestions(t *testing.T) {
	p := NewCombinedAutocompleteProvider(testCommands(), "/tmp")

	result := p.GetSuggestions([]string{""}, 0, 0)
	if result != nil {
		t.Error("expected no suggestions for empty line")
	}
}

func TestAutocomplete_ApplySlashCommand(t *testing.T) {
	p := NewCombinedAutocompleteProvider(testCommands(), "/tmp")

	item := tuicomp.SelectItem{Value: "help", Label: "help", Description: "Show help"}
	result := p.ApplyCompletion(
		[]string{"/he"}, 0, 3,
		item, "/he",
	)

	if len(result.Lines) != 1 {
		t.Fatal("expected 1 line")
	}
	if result.Lines[0] != "/help " {
		t.Errorf("expected '/help ', got %q", result.Lines[0])
	}
	if result.CursorCol != 6 {
		t.Errorf("expected cursor at 6, got %d", result.CursorCol)
	}
}

func TestAutocomplete_FileSuggestions(t *testing.T) {
	// Create a temp directory with some files
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi"), 0644)
	os.Mkdir(filepath.Join(dir, "src"), 0755)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644)

	p := NewCombinedAutocompleteProvider(testCommands(), dir)

	// "./R" should match README.md
	result := p.GetSuggestions([]string{"./R"}, 0, 3)
	if result == nil {
		t.Fatal("expected file suggestions for ./R")
	}
	found := false
	for _, item := range result.Items {
		if strings.Contains(item.Label, "README") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected README.md in file suggestions")
	}
}

func TestAutocomplete_AtFileSuggestions(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("content"), 0644)

	p := NewCombinedAutocompleteProvider(testCommands(), dir)

	// "@t" should find test.txt via fuzzy matching
	result := p.GetSuggestions([]string{"@t"}, 0, 2)
	if result == nil {
		t.Fatal("expected suggestions for @t")
	}
	found := false
	for _, item := range result.Items {
		if strings.Contains(item.Label, "test") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected test.txt in @ suggestions")
	}
}

func TestAutocomplete_DirectorySortedFirst(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "adir"), 0755)
	os.WriteFile(filepath.Join(dir, "afile.txt"), []byte("x"), 0644)

	p := NewCombinedAutocompleteProvider(testCommands(), dir)

	result := p.GetSuggestions([]string{"./a"}, 0, 3)
	if result == nil {
		t.Fatal("expected suggestions for ./a")
	}
	if len(result.Items) < 2 {
		t.Fatal("expected at least 2 items")
	}
	// Directory should come first
	if !strings.HasSuffix(result.Items[0].Label, "/") {
		t.Errorf("expected directory first, got label %q", result.Items[0].Label)
	}
}
