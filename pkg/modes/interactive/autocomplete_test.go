package interactive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tuicomp "github.com/kfet/fir/pkg/tui/components"
)

func testCommands() []SlashCommand {
	return []SlashCommand{
		{Name: "help", Description: "Show help"},
		{Name: "theme", Description: "Select color theme"},
		{Name: "thinking", Description: "Select thinking level"},
		{Name: "model", Description: "Select model"},
		{Name: "quit", Description: "Quit fir"},
		{Name: "login", Description: "Login with OAuth"},
		{Name: "logout", Description: "Logout from OAuth"},
		{Name: "clear", Description: "Start new session"},
		{Name: "compact", Description: "Compact session"},
		{Name: "settings", Description: "Open settings"},
	}
}

func TestAutocomplete_ThemeCommandSuggested(t *testing.T) {
	p := NewCombinedAutocompleteProvider(testCommands(), "/tmp", nil)

	// "/th" should match both "theme" and "thinking"
	result := p.GetSuggestions([]string{"/th"}, 0, 3)
	if result == nil {
		t.Fatal("expected suggestions for /th")
	}
	foundTheme := false
	for _, item := range result.Items {
		if item.Value == "theme" {
			foundTheme = true
			break
		}
	}
	if !foundTheme {
		t.Errorf("expected 'theme' in suggestions for /th, got %v", result.Items)
	}
}

func TestAutocomplete_SlashCommandSuggestions(t *testing.T) {
	p := NewCombinedAutocompleteProvider(testCommands(), "/tmp", nil)

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
	p := NewCombinedAutocompleteProvider(testCommands(), "/tmp", nil)

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
	p := NewCombinedAutocompleteProvider(testCommands(), "/tmp", nil)

	// "/zzz" should match nothing
	result := p.GetSuggestions([]string{"/zzz"}, 0, 4)
	if result != nil {
		t.Errorf("expected nil for /zzz, got %d items", len(result.Items))
	}
}

func TestAutocomplete_NormalTextNoSuggestions(t *testing.T) {
	p := NewCombinedAutocompleteProvider(testCommands(), "/tmp", nil)

	// Regular text should not trigger suggestions
	result := p.GetSuggestions([]string{"hello world"}, 0, 11)
	if result != nil {
		t.Error("expected no suggestions for normal text")
	}
}

func TestAutocomplete_EmptyLineNoSuggestions(t *testing.T) {
	p := NewCombinedAutocompleteProvider(testCommands(), "/tmp", nil)

	result := p.GetSuggestions([]string{""}, 0, 0)
	if result != nil {
		t.Error("expected no suggestions for empty line")
	}
}

func TestAutocomplete_ApplySlashCommand(t *testing.T) {
	p := NewCombinedAutocompleteProvider(testCommands(), "/tmp", nil)

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

	p := NewCombinedAutocompleteProvider(testCommands(), dir, nil)

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

	p := NewCombinedAutocompleteProvider(testCommands(), dir, nil)

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

	p := NewCombinedAutocompleteProvider(testCommands(), dir, nil)

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

func TestAutocomplete_AtFuzzySubdirectory(t *testing.T) {
	dir := t.TempDir()
	// Create nested structure:
	//   src/
	//     main.go
	//     utils/
	//       helpers.go
	//   docs/
	//     readme.md
	os.MkdirAll(filepath.Join(dir, "src", "utils"), 0755)
	os.MkdirAll(filepath.Join(dir, "docs"), 0755)
	os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(dir, "src", "utils", "helpers.go"), []byte("package utils"), 0644)
	os.WriteFile(filepath.Join(dir, "docs", "readme.md"), []byte("# readme"), 0644)

	p := NewCombinedAutocompleteProvider(testCommands(), dir, nil)

	// "@helpers" should find src/utils/helpers.go
	result := p.GetSuggestions([]string{"@helpers"}, 0, 8)
	if result == nil {
		t.Fatal("expected suggestions for @helpers")
	}
	found := false
	for _, item := range result.Items {
		if strings.Contains(item.Value, "src/utils/helpers.go") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected src/utils/helpers.go in suggestions, got %v", result.Items)
	}
}

func TestAutocomplete_AtFuzzyMatchesFilename(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "pkg", "core"), 0755)
	os.WriteFile(filepath.Join(dir, "pkg", "core", "config.go"), []byte("package core"), 0644)
	os.WriteFile(filepath.Join(dir, "pkg", "core", "config_test.go"), []byte("package core"), 0644)

	p := NewCombinedAutocompleteProvider(testCommands(), dir, nil)

	// "@config" should find both config files
	result := p.GetSuggestions([]string{"@config"}, 0, 7)
	if result == nil {
		t.Fatal("expected suggestions for @config")
	}
	configCount := 0
	for _, item := range result.Items {
		if strings.Contains(item.Value, "config") {
			configCount++
		}
	}
	if configCount < 2 {
		t.Errorf("expected at least 2 config matches, got %d: %v", configCount, result.Items)
	}
}

func TestAutocomplete_AtFuzzyPathSegments(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "pkg", "modes", "interactive"), 0755)
	os.WriteFile(filepath.Join(dir, "pkg", "modes", "interactive", "autocomplete.go"), []byte("package interactive"), 0644)

	p := NewCombinedAutocompleteProvider(testCommands(), dir, nil)

	// "@auto" should fuzzy-match autocomplete.go deep in the tree
	result := p.GetSuggestions([]string{"@auto"}, 0, 5)
	if result == nil {
		t.Fatal("expected suggestions for @auto")
	}
	found := false
	for _, item := range result.Items {
		if strings.Contains(item.Value, "autocomplete.go") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected autocomplete.go in suggestions, got %v", result.Items)
	}
}

func TestAutocomplete_AtFuzzySkipsHiddenDirs(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git", "objects"), 0755)
	os.WriteFile(filepath.Join(dir, ".git", "objects", "secret.go"), []byte("hidden"), 0644)
	os.WriteFile(filepath.Join(dir, "visible.go"), []byte("package main"), 0644)

	p := NewCombinedAutocompleteProvider(testCommands(), dir, nil)

	// "@secret" should NOT find .git/objects/secret.go
	result := p.GetSuggestions([]string{"@secret"}, 0, 7)
	if result != nil {
		for _, item := range result.Items {
			if strings.Contains(item.Value, ".git") {
				t.Errorf("should not suggest files inside .git: %q", item.Value)
			}
		}
	}

	// "@visible" should still work
	result = p.GetSuggestions([]string{"@visible"}, 0, 8)
	if result == nil {
		t.Fatal("expected suggestions for @visible")
	}
	found := false
	for _, item := range result.Items {
		if strings.Contains(item.Value, "visible.go") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected visible.go in suggestions")
	}
}

func TestAutocomplete_AtFuzzySkipsNodeModules(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "node_modules", "lodash"), 0755)
	os.WriteFile(filepath.Join(dir, "node_modules", "lodash", "index.js"), []byte("module.exports"), 0644)
	os.WriteFile(filepath.Join(dir, "index.ts"), []byte("import"), 0644)

	p := NewCombinedAutocompleteProvider(testCommands(), dir, nil)

	// "@index" should find index.ts but not node_modules/lodash/index.js
	result := p.GetSuggestions([]string{"@index"}, 0, 6)
	if result == nil {
		t.Fatal("expected suggestions for @index")
	}
	for _, item := range result.Items {
		if strings.Contains(item.Value, "node_modules") {
			t.Errorf("should not suggest files inside node_modules: %q", item.Value)
		}
	}
	found := false
	for _, item := range result.Items {
		if strings.Contains(item.Value, "index.ts") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected index.ts in suggestions")
	}
}

func TestAutocomplete_AtDirectoryListsDirect(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src"), 0755)
	os.WriteFile(filepath.Join(dir, "src", "a.go"), []byte("a"), 0644)
	os.WriteFile(filepath.Join(dir, "src", "b.go"), []byte("b"), 0644)

	p := NewCombinedAutocompleteProvider(testCommands(), dir, nil)

	// "@src/" should list direct children only (not recursive)
	result := p.GetSuggestions([]string{"@src/"}, 0, 5)
	if result == nil {
		t.Fatal("expected suggestions for @src/")
	}
	if len(result.Items) != 2 {
		t.Errorf("expected 2 items for @src/, got %d: %v", len(result.Items), result.Items)
	}
}

func TestAutocomplete_AtEmptyListsAll(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sub"), 0755)
	os.WriteFile(filepath.Join(dir, "root.txt"), []byte("r"), 0644)
	os.WriteFile(filepath.Join(dir, "sub", "deep.txt"), []byte("d"), 0644)

	p := NewCombinedAutocompleteProvider(testCommands(), dir, nil)

	// "@" alone should list all files recursively
	result := p.GetSuggestions([]string{"@"}, 0, 1)
	if result == nil {
		t.Fatal("expected suggestions for @")
	}
	foundRoot := false
	foundDeep := false
	for _, item := range result.Items {
		if strings.Contains(item.Value, "root.txt") {
			foundRoot = true
		}
		if strings.Contains(item.Value, "sub/deep.txt") {
			foundDeep = true
		}
	}
	if !foundRoot {
		t.Error("expected root.txt in @ suggestions")
	}
	if !foundDeep {
		t.Error("expected sub/deep.txt in @ suggestions")
	}
}

func TestAutocomplete_NoDoubleSlashes(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src", "pkg"), 0755)
	os.WriteFile(filepath.Join(dir, "src", "pkg", "main.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(dir, "src", "app.go"), []byte("package src"), 0644)

	p := NewCombinedAutocompleteProvider(testCommands(), dir, nil)

	// Test various prefix patterns that could produce "//"
	tests := []struct {
		input     string
		cursorCol int
	}{
		{"@src/", 5},       // trailing slash in prefix
		{"@src/p", 6},      // prefix with dir + partial name
		{"@src/pkg/", 9},   // deeply nested trailing slash
		{"@src/pkg/m", 10}, // deeply nested partial
		{"@main", 5},       // plain fuzzy across subdirs
	}

	for _, tc := range tests {
		result := p.GetSuggestions([]string{tc.input}, 0, tc.cursorCol)
		if result == nil {
			continue
		}
		for _, item := range result.Items {
			if strings.Contains(item.Value, "//") {
				t.Errorf("double slash found in value for input %q: %q", tc.input, item.Value)
			}
			if strings.Contains(item.Label, "//") {
				t.Errorf("double slash found in label for input %q: %q", tc.input, item.Label)
			}
		}
	}
}

func TestAutocomplete_NoDoubleSlashesAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sub"), 0755)
	os.WriteFile(filepath.Join(dir, "sub", "file.go"), []byte("package sub"), 0644)

	p := NewCombinedAutocompleteProvider(testCommands(), dir, nil)

	// Absolute path ending with "/" that could produce "//" when name appended
	absPrefix := dir + "/sub/"
	result := p.GetSuggestions([]string{absPrefix}, 0, len(absPrefix))
	if result == nil {
		return // No suggestions from absolute-path code path is fine
	}
	for _, item := range result.Items {
		if strings.Contains(item.Value, "//") {
			t.Errorf("double slash in absolute path suggestion: %q", item.Value)
		}
	}
}

func TestCleanSlashes(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"a/b/c", "a/b/c"},
		{"a//b/c", "a/b/c"},
		{"a///b///c", "a/b/c"},
		{"/a/b", "/a/b"},
		{"//a//b", "/a/b"},
		{"src//pkg//main.go", "src/pkg/main.go"},
		{"", ""},
		{"/", "/"},
		{"//", "/"},
	}
	for _, tc := range tests {
		// Access cleanSlashes via the package (it's unexported but same package in test)
		got := cleanSlashes(tc.input)
		if got != tc.want {
			t.Errorf("cleanSlashes(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestGetSuggestions_CommandArgStaticCompletion(t *testing.T) {
	argSpecs := map[string]*CommandArgSpec{
		"skills": {
			SubCommands: map[string]*CommandArgSpec{
				"list":    {Type: ArgCompleteNone},
				"install": {Type: ArgCompleteStatic, Values: []string{"code-review", "testing"}},
			},
		},
	}
	p := NewCombinedAutocompleteProvider(testCommands(), "/tmp", argSpecs)

	// Complete subcommand
	s := p.GetSuggestions([]string{"/skills "}, 0, 8)
	if s == nil {
		t.Fatal("expected suggestions for /skills subcommands")
	}
	if len(s.Items) != 2 {
		t.Fatalf("expected 2 subcommand suggestions, got %d", len(s.Items))
	}

	// Fuzzy filter subcommand
	s = p.GetSuggestions([]string{"/skills ins"}, 0, 11)
	if s == nil {
		t.Fatal("expected suggestions for /skills ins")
	}
	if len(s.Items) != 1 || s.Items[0].Value != "install" {
		t.Fatalf("expected 'install', got %v", s.Items)
	}

	// Complete nested arg (skill names after /skills install)
	s = p.GetSuggestions([]string{"/skills install "}, 0, 16)
	if s == nil {
		t.Fatal("expected suggestions for /skills install args")
	}
	if len(s.Items) != 2 {
		t.Fatalf("expected 2 skill names, got %d", len(s.Items))
	}

	// Fuzzy filter nested arg
	s = p.GetSuggestions([]string{"/skills install test"}, 0, 20)
	if s == nil {
		t.Fatal("expected suggestions for /skills install test")
	}
	if len(s.Items) != 1 || s.Items[0].Value != "testing" {
		t.Fatalf("expected 'testing', got %v", s.Items)
	}
}

func TestGetSuggestions_CommandArgFileCompletion(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "subdir"), 0o755)
	os.WriteFile(filepath.Join(dir, "output.html"), []byte("x"), 0o644)

	argSpecs := map[string]*CommandArgSpec{
		"export": {Type: ArgCompleteFile},
	}
	p := NewCombinedAutocompleteProvider(testCommands(), dir, argSpecs)

	s := p.GetSuggestions([]string{"/export "}, 0, 8)
	if s == nil {
		t.Fatal("expected file suggestions for /export")
	}
	if len(s.Items) < 1 {
		t.Fatal("expected at least 1 file suggestion")
	}

	// Partial file match
	s = p.GetSuggestions([]string{"/export out"}, 0, 11)
	if s == nil {
		t.Fatal("expected file suggestions for /export out")
	}
	found := false
	for _, item := range s.Items {
		if strings.Contains(item.Value, "output.html") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected output.html in suggestions, got %v", s.Items)
	}
}

func TestApplyCompletion_CommandArg(t *testing.T) {
	argSpecs := map[string]*CommandArgSpec{
		"skills": {
			SubCommands: map[string]*CommandArgSpec{
				"install": {Type: ArgCompleteStatic, Values: []string{"testing"}},
			},
		},
	}
	p := NewCombinedAutocompleteProvider(testCommands(), "/tmp", argSpecs)

	// Apply subcommand completion
	result := p.ApplyCompletion(
		[]string{"/skills ins"}, 0, 11,
		tuicomp.SelectItem{Value: "install", Label: "install"},
		"/skills ins",
	)
	if result.Lines[0] != "/skills install " {
		t.Fatalf("expected '/skills install ', got %q", result.Lines[0])
	}

	// Apply nested arg completion — must preserve "/skills install "
	result = p.ApplyCompletion(
		[]string{"/skills install test"}, 0, 20,
		tuicomp.SelectItem{Value: "testing", Label: "testing"},
		"/skills install test",
	)
	if result.Lines[0] != "/skills install testing " {
		t.Fatalf("expected '/skills install testing ', got %q", result.Lines[0])
	}
}
