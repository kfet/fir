package resources

import (
	"github.com/kfet/fir/pkg/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- ParseCommandArgs ---

func TestParseCommandArgs_Simple(t *testing.T) {
	args := ParseCommandArgs("hello world")
	if len(args) != 2 || args[0] != "hello" || args[1] != "world" {
		t.Errorf("got %v", args)
	}
}

func TestParseCommandArgs_Quoted(t *testing.T) {
	args := ParseCommandArgs(`"hello world" foo`)
	if len(args) != 2 || args[0] != "hello world" || args[1] != "foo" {
		t.Errorf("got %v", args)
	}
}

func TestParseCommandArgs_SingleQuotes(t *testing.T) {
	args := ParseCommandArgs("'hello world' foo")
	if len(args) != 2 || args[0] != "hello world" || args[1] != "foo" {
		t.Errorf("got %v", args)
	}
}

func TestParseCommandArgs_Empty(t *testing.T) {
	args := ParseCommandArgs("")
	if len(args) != 0 {
		t.Errorf("got %v, want empty", args)
	}
}

func TestParseCommandArgs_Tabs(t *testing.T) {
	args := ParseCommandArgs("a\tb\tc")
	if len(args) != 3 {
		t.Errorf("got %v", args)
	}
}

func TestParseCommandArgs_MultipleSpaces(t *testing.T) {
	args := ParseCommandArgs("a   b")
	if len(args) != 2 || args[0] != "a" || args[1] != "b" {
		t.Errorf("got %v", args)
	}
}

// --- SubstituteArgs ---

func TestSubstituteArgs_Positional(t *testing.T) {
	result := SubstituteArgs("Hello $1, meet $2", []string{"Alice", "Bob"})
	if result != "Hello Alice, meet Bob" {
		t.Errorf("got %q", result)
	}
}

func TestSubstituteArgs_MissingPositional(t *testing.T) {
	result := SubstituteArgs("Hello $1 and $3", []string{"Alice"})
	if result != "Hello Alice and " {
		t.Errorf("got %q", result)
	}
}

func TestSubstituteArgs_AllArgs(t *testing.T) {
	result := SubstituteArgs("All: $@", []string{"a", "b", "c"})
	if result != "All: a b c" {
		t.Errorf("got %q", result)
	}
}

func TestSubstituteArgs_Arguments(t *testing.T) {
	result := SubstituteArgs("All: $ARGUMENTS", []string{"a", "b"})
	if result != "All: a b" {
		t.Errorf("got %q", result)
	}
}

func TestSubstituteArgs_Slice(t *testing.T) {
	result := SubstituteArgs("Tail: ${@:2}", []string{"a", "b", "c"})
	if result != "Tail: b c" {
		t.Errorf("got %q", result)
	}
}

func TestSubstituteArgs_SliceWithLength(t *testing.T) {
	result := SubstituteArgs("Mid: ${@:2:1}", []string{"a", "b", "c"})
	if result != "Mid: b" {
		t.Errorf("got %q", result)
	}
}

func TestSubstituteArgs_NoArgs(t *testing.T) {
	result := SubstituteArgs("Hello $1 $@", []string{})
	if result != "Hello  " {
		t.Errorf("got %q", result)
	}
}

// --- LoadPromptTemplates ---

func TestLoadPromptTemplates_FromDir(t *testing.T) {
	dir := t.TempDir()
	promptsDir := filepath.Join(dir, "prompts")
	os.MkdirAll(promptsDir, 0755)

	os.WriteFile(filepath.Join(promptsDir, "test.md"), []byte("---\ndescription: A test template\n---\nHello $1!"), 0644)

	templates := LoadPromptTemplates(LoadPromptTemplatesOptions{
		AgentDir:        dir,
		IncludeDefaults: true,
	})

	if len(templates) != 1 {
		t.Fatalf("expected 1 template, got %d", len(templates))
	}
	if templates[0].Name != "test" {
		t.Errorf("name = %q, want test", templates[0].Name)
	}
	if !strings.Contains(templates[0].Description, "A test template") {
		t.Errorf("description = %q", templates[0].Description)
	}
	if templates[0].Content != "Hello $1!" {
		t.Errorf("content = %q", templates[0].Content)
	}
	if templates[0].Source != "user" {
		t.Errorf("source = %q, want user", templates[0].Source)
	}
}

func TestLoadPromptTemplates_ProjectDir(t *testing.T) {
	cwd := t.TempDir()
	projectDir := filepath.Join(cwd, config.ConfigDirName, "prompts")
	os.MkdirAll(projectDir, 0755)

	os.WriteFile(filepath.Join(projectDir, "greet.md"), []byte("Hello!"), 0644)

	templates := LoadPromptTemplates(LoadPromptTemplatesOptions{
		Cwd:             cwd,
		IncludeDefaults: true,
	})

	if len(templates) != 1 {
		t.Fatalf("expected 1 template, got %d", len(templates))
	}
	if templates[0].Name != "greet" {
		t.Errorf("name = %q, want greet", templates[0].Name)
	}
	if templates[0].Source != "project" {
		t.Errorf("source = %q, want project", templates[0].Source)
	}
}

func TestLoadPromptTemplates_ExplicitPath(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "custom.md")
	os.WriteFile(filePath, []byte("---\ndescription: Custom\n---\nContent"), 0644)

	templates := LoadPromptTemplates(LoadPromptTemplatesOptions{
		PromptPaths:     []string{filePath},
		IncludeDefaults: false,
	})

	if len(templates) != 1 {
		t.Fatalf("expected 1 template, got %d", len(templates))
	}
	if templates[0].Source != "path" {
		t.Errorf("source = %q, want path", templates[0].Source)
	}
}

func TestLoadPromptTemplates_ExplicitDir(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.md"), []byte("A content"), 0644)
	os.WriteFile(filepath.Join(dir, "b.md"), []byte("B content"), 0644)

	templates := LoadPromptTemplates(LoadPromptTemplatesOptions{
		PromptPaths:     []string{dir},
		IncludeDefaults: false,
	})

	if len(templates) != 2 {
		t.Fatalf("expected 2 templates, got %d", len(templates))
	}
}

func TestLoadPromptTemplates_NonExistentPath(t *testing.T) {
	templates := LoadPromptTemplates(LoadPromptTemplatesOptions{
		PromptPaths:     []string{"/nonexistent"},
		IncludeDefaults: false,
	})

	if len(templates) != 0 {
		t.Errorf("expected 0 templates, got %d", len(templates))
	}
}

func TestLoadPromptTemplates_DescriptionFromFirstLine(t *testing.T) {
	dir := t.TempDir()
	promptsDir := filepath.Join(dir, "prompts")
	os.MkdirAll(promptsDir, 0755)

	// No frontmatter — description comes from first line
	os.WriteFile(filepath.Join(promptsDir, "test.md"), []byte("This is the first line\nAnd body"), 0644)

	templates := LoadPromptTemplates(LoadPromptTemplatesOptions{
		AgentDir:        dir,
		IncludeDefaults: true,
	})

	if len(templates) != 1 {
		t.Fatalf("expected 1 template, got %d", len(templates))
	}
	if !strings.Contains(templates[0].Description, "This is the first line") {
		t.Errorf("description = %q", templates[0].Description)
	}
}

func TestLoadPromptTemplates_LongFirstLineTruncated(t *testing.T) {
	dir := t.TempDir()
	promptsDir := filepath.Join(dir, "prompts")
	os.MkdirAll(promptsDir, 0755)

	longLine := strings.Repeat("x", 100)
	os.WriteFile(filepath.Join(promptsDir, "test.md"), []byte(longLine), 0644)

	templates := LoadPromptTemplates(LoadPromptTemplatesOptions{
		AgentDir:        dir,
		IncludeDefaults: true,
	})

	if len(templates) != 1 {
		t.Fatalf("expected 1 template, got %d", len(templates))
	}
	if !strings.HasSuffix(templates[0].Description, "(user)") {
		t.Errorf("description = %q, should end with (user)", templates[0].Description)
	}
	// The description portion before the source label should be truncated
	parts := strings.Split(templates[0].Description, " (user)")
	if len(parts[0]) > 70 { // 60 + "..."
		t.Errorf("description too long: %d chars", len(parts[0]))
	}
}

// --- ExpandPromptTemplate ---

func TestExpandPromptTemplate_Match(t *testing.T) {
	templates := []PromptTemplate{
		{Name: "greet", Content: "Hello $1!"},
	}
	result := ExpandPromptTemplate("/greet World", templates)
	if result != "Hello World!" {
		t.Errorf("got %q", result)
	}
}

func TestExpandPromptTemplate_NoMatch(t *testing.T) {
	templates := []PromptTemplate{
		{Name: "greet", Content: "Hello"},
	}
	result := ExpandPromptTemplate("/unknown", templates)
	if result != "/unknown" {
		t.Errorf("got %q, want /unknown", result)
	}
}

func TestExpandPromptTemplate_NotSlashCommand(t *testing.T) {
	templates := []PromptTemplate{
		{Name: "greet", Content: "Hello"},
	}
	result := ExpandPromptTemplate("greet", templates)
	if result != "greet" {
		t.Errorf("got %q", result)
	}
}

func TestExpandPromptTemplate_NoArgs(t *testing.T) {
	templates := []PromptTemplate{
		{Name: "greet", Content: "Hello World"},
	}
	result := ExpandPromptTemplate("/greet", templates)
	if result != "Hello World" {
		t.Errorf("got %q", result)
	}
}

func TestExpandPromptTemplate_AllArgs(t *testing.T) {
	templates := []PromptTemplate{
		{Name: "echo", Content: "You said: $@"},
	}
	result := ExpandPromptTemplate("/echo hello world", templates)
	if result != "You said: hello world" {
		t.Errorf("got %q", result)
	}
}

// --- inferPromptSource ---

func TestInferPromptSource_User(t *testing.T) {
	source, label := inferPromptSource("/home/user/.config/fir/prompts/fix.md", "/home/user/.config/fir/prompts", "/project/.fir/prompts")
	if source != "user" {
		t.Errorf("source = %q, want user", source)
	}
	if label != "(user)" {
		t.Errorf("label = %q, want (user)", label)
	}
}

func TestInferPromptSource_Project(t *testing.T) {
	source, label := inferPromptSource("/project/.fir/prompts/review.md", "/home/user/.config/fir/prompts", "/project/.fir/prompts")
	if source != "project" {
		t.Errorf("source = %q, want project", source)
	}
	if label != "(project)" {
		t.Errorf("label = %q, want (project)", label)
	}
}

func TestInferPromptSource_Path(t *testing.T) {
	source, label := inferPromptSource("/custom/dir/something.md", "/home/user/.config/fir/prompts", "/project/.fir/prompts")
	if source != "path" {
		t.Errorf("source = %q, want path", source)
	}
	if label != "(path:something)" {
		t.Errorf("label = %q, want (path:something)", label)
	}
}

func TestInferPromptSource_DirMatch(t *testing.T) {
	source, _ := inferPromptSource("/home/user/.config/fir/prompts", "/home/user/.config/fir/prompts", "")
	if source != "user" {
		t.Errorf("source = %q, want user (exact dir match)", source)
	}
}

func TestLoadPromptTemplates_ExplicitUserDir(t *testing.T) {
	// When an explicit path matches the agentDir/prompts, source should be "user"
	dir := t.TempDir()
	promptsDir := filepath.Join(dir, "prompts")
	os.MkdirAll(promptsDir, 0755)
	os.WriteFile(filepath.Join(promptsDir, "fix.md"), []byte("Fix it!"), 0644)

	templates := LoadPromptTemplates(LoadPromptTemplatesOptions{
		AgentDir:        dir,
		PromptPaths:     []string{promptsDir},
		IncludeDefaults: false,
	})

	if len(templates) != 1 {
		t.Fatalf("expected 1 template, got %d", len(templates))
	}
	if templates[0].Source != "user" {
		t.Errorf("source = %q, want user", templates[0].Source)
	}
}

func TestLoadPromptTemplates_ExplicitProjectDir(t *testing.T) {
	// When an explicit path matches the cwd/.fir/prompts, source should be "project"
	cwd := t.TempDir()
	promptsDir := filepath.Join(cwd, config.ConfigDirName, "prompts")
	os.MkdirAll(promptsDir, 0755)
	os.WriteFile(filepath.Join(promptsDir, "review.md"), []byte("Review it!"), 0644)

	templates := LoadPromptTemplates(LoadPromptTemplatesOptions{
		Cwd:             cwd,
		PromptPaths:     []string{promptsDir},
		IncludeDefaults: false,
	})

	if len(templates) != 1 {
		t.Fatalf("expected 1 template, got %d", len(templates))
	}
	if templates[0].Source != "project" {
		t.Errorf("source = %q, want project", templates[0].Source)
	}
}
