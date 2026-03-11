package acp

import (
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/envkeys"
	"github.com/kfet/fir/pkg/models"
	"github.com/kfet/fir/pkg/auth"
)

func TestParseModelID(t *testing.T) {
	tests := []struct {
		input       string
		provider    string
		modelID     string
		expectError bool
	}{
		{"anthropic/claude-3", "anthropic", "claude-3", false},
		{"openai/gpt-4o", "openai", "gpt-4o", false},
		{"provider/model/with/slashes", "provider", "model/with/slashes", false},
		{"noslash", "", "", true},
	}
	for _, tt := range tests {
		p, m, err := ParseModelID(tt.input)
		if tt.expectError {
			if err == nil {
				t.Errorf("ParseModelID(%q): expected error", tt.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseModelID(%q): unexpected error: %v", tt.input, err)
			continue
		}
		if p != tt.provider || m != tt.modelID {
			t.Errorf("ParseModelID(%q) = (%q, %q), want (%q, %q)", tt.input, p, m, tt.provider, tt.modelID)
		}
	}
}

func TestExtractPromptContent(t *testing.T) {
	blocks := []acpsdk.ContentBlock{
		{Text: &acpsdk.ContentBlockText{Text: "hello", Type: "text"}},
		{Text: &acpsdk.ContentBlockText{Text: "world", Type: "text"}},
		{Image: &acpsdk.ContentBlockImage{Data: "abc", MimeType: "image/png", Type: "image"}},
		{ResourceLink: &acpsdk.ContentBlockResourceLink{Uri: "file:///foo/bar", Type: "resource_link"}},
	}

	text, images := ExtractPromptContent(blocks)
	if text != "hello\nworld\n@/foo/bar" {
		t.Errorf("text = %q", text)
	}
	if len(images) != 1 || images[0].Data != "abc" {
		t.Errorf("images = %v", images)
	}
}

func TestMapToolKind(t *testing.T) {
	cases := map[string]acpsdk.ToolKind{
		"read": "read", "write": "edit", "edit": "edit",
		"bash": "execute", "grep": "search", "unknown": "other",
	}
	for name, want := range cases {
		if got := MapToolKind(name); got != want {
			t.Errorf("MapToolKind(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestBuildToolTitle(t *testing.T) {
	tests := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"bash", map[string]any{"command": "ls -la"}, "`ls -la`"},
		{"read", map[string]any{"path": "/tmp/foo"}, "Read /tmp/foo"},
		{"read", map[string]any{"path": "/tmp/foo", "offset": float64(10)}, "Read /tmp/foo from line 10"},
		{"write", map[string]any{"path": "/tmp/bar"}, "Write /tmp/bar"},
		{"edit", map[string]any{"path": "/tmp/baz"}, "Edit /tmp/baz"},
		{"grep", map[string]any{"pattern": "TODO", "path": "src/"}, `grep "TODO" in src/`},
		{"find", map[string]any{"pattern": "*.go"}, `find "*.go"`},
		{"ls", map[string]any{"path": "/tmp"}, "ls /tmp"},
		{"ls", nil, "List files"},
		{"bash_output", map[string]any{"command_id": "abc"}, "Get output: abc"},
		{"bash_kill", map[string]any{"command_id": "abc"}, "Kill: abc"},
		{"unknown_tool", nil, "unknown_tool"},
	}
	for _, tt := range tests {
		got := BuildToolTitle(tt.tool, tt.args)
		if got != tt.want {
			t.Errorf("BuildToolTitle(%q, %v) = %q, want %q", tt.tool, tt.args, got, tt.want)
		}
	}
}

func TestBuildToolLocations(t *testing.T) {
	locs := BuildToolLocations("read", map[string]any{"path": "/tmp/f", "offset": float64(42)})
	if len(locs) != 1 || locs[0].Path != "/tmp/f" || *locs[0].Line != 42 {
		t.Errorf("unexpected locations: %v", locs)
	}

	locs = BuildToolLocations("bash", map[string]any{"command": "ls"})
	if len(locs) != 0 {
		t.Errorf("bash should have no locations, got %v", locs)
	}
}

func TestMarkdownEscape(t *testing.T) {
	got := MarkdownEscape("hello world")
	want := "```\nhello world\n```"
	if got != want {
		t.Errorf("MarkdownEscape = %q, want %q", got, want)
	}

	// Content with triple backticks
	got = MarkdownEscape("```code```")
	if got[:4] != "````" {
		t.Errorf("should use 4+ backticks for content with ```, got %q", got)
	}
}

func TestParseDiffForAcp(t *testing.T) {
	diff := " 1 context\n-2 old line\n+2 new line\n 3 context"
	content, locations := ParseDiffForAcp(diff, "test.go", 2)
	if len(content) != 1 || len(locations) != 1 {
		t.Fatalf("expected 1 content + 1 location, got %d + %d", len(content), len(locations))
	}
	if locations[0].Path != "test.go" || *locations[0].Line != 2 {
		t.Errorf("location = %v", locations[0])
	}

	// Empty input
	c, l := ParseDiffForAcp("", "", 0)
	if len(c) != 0 || len(l) != 0 {
		t.Error("empty input should return empty")
	}
}

func TestIsPathWithinDirectory(t *testing.T) {
	if !IsPathWithinDirectory("/home/user/project/file.go", "/home/user/project") {
		t.Error("file within directory should return true")
	}
	if IsPathWithinDirectory("/etc/passwd", "/home/user/project") {
		t.Error("file outside directory should return false")
	}
	if IsPathWithinDirectory("/home/user/project/../etc/passwd", "/home/user/project") {
		t.Error("path traversal should return false")
	}
}

func TestBuildToolInitialContent(t *testing.T) {
	// Bash command
	content := BuildToolInitialContent("bash", map[string]any{"command": "echo hi"})
	if len(content) != 1 || content[0].Content == nil {
		t.Error("bash should produce text content")
	}

	// Write with path and content
	content = BuildToolInitialContent("write", map[string]any{"path": "foo.go", "content": "package main"})
	if len(content) != 1 || content[0].Diff == nil {
		t.Error("write should produce diff content")
	}

	// Unknown tool
	content = BuildToolInitialContent("unknown", nil)
	if len(content) != 0 {
		t.Error("unknown tool should produce no content")
	}
}

func TestBuildToolCallContent_Error(t *testing.T) {
	result := map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "error message"}},
	}
	content, _ := BuildToolCallContent("bash", nil, result, true)
	if len(content) != 1 || content[0].Content == nil {
		t.Error("error should produce text content")
	}
}

func TestBuildToolCallContent_Write(t *testing.T) {
	content, _ := BuildToolCallContent("write", nil, nil, false)
	if len(content) != 0 {
		t.Error("write should produce no content")
	}
}

func TestBuildToolCallContent_Read(t *testing.T) {
	result := map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "file contents"}},
	}
	content, _ := BuildToolCallContent("read", nil, result, false)
	if len(content) != 1 || content[0].Content == nil {
		t.Error("read should produce text content")
	}
}

func TestParseIntHelpers(t *testing.T) {
	if parseInt("42") != 42 {
		t.Error("parseInt(42)")
	}
	if parseInt("abc") != 0 {
		t.Error("parseInt(abc)")
	}
	if parseInt("") != 0 {
		t.Error("parseInt empty")
	}
	if parseInt(" 3 ") != 3 {
		t.Error("parseInt with spaces")
	}
}

func TestBuildModelState(t *testing.T) {
	// Isolate from any real API keys set in the environment.
	for _, key := range envkeys.KnownApiKeyEnvVars() {
		t.Setenv(key, "")
	}

	authStore := auth.NewInMemoryAuthStorage(nil)
	reg := models.NewModelRegistry(authStore, "")

	t.Run("nil current model returns nil", func(t *testing.T) {
		state := BuildModelState(reg, nil)
		if state != nil {
			t.Errorf("expected nil, got %+v", state)
		}
	})

	t.Run("with current model but no auth returns empty available list", func(t *testing.T) {
		current := &ai.Model{ID: "claude-3-7-sonnet-20250219", Provider: "anthropic", Name: "Claude 3.7 Sonnet"}
		state := BuildModelState(reg, current)
		if state == nil {
			t.Fatal("expected non-nil state")
		}
		wantCurrentID := acpsdk.ModelId("anthropic/claude-3-7-sonnet-20250219")
		if state.CurrentModelId != wantCurrentID {
			t.Errorf("CurrentModelId = %q, want %q", state.CurrentModelId, wantCurrentID)
		}
		// No auth configured → no available models.
		if len(state.AvailableModels) != 0 {
			t.Errorf("AvailableModels should be empty with no auth, got %d models", len(state.AvailableModels))
		}
	})

	t.Run("with auth configured returns models for that provider", func(t *testing.T) {
		auth2 := auth.NewInMemoryAuthStorage(nil)
		auth2.SetRuntimeApiKey("anthropic", "test-api-key")
		reg2 := models.NewModelRegistry(auth2, "")
		current := &ai.Model{ID: "claude-3-7-sonnet-20250219", Provider: "anthropic", Name: "Claude 3.7 Sonnet"}
		state := BuildModelState(reg2, current)
		if state == nil {
			t.Fatal("expected non-nil state")
		}
		if len(state.AvailableModels) == 0 {
			t.Error("AvailableModels is empty; expected anthropic models since auth is set")
		}
		for _, m := range state.AvailableModels {
			if m.ModelId == "" {
				t.Errorf("AvailableModels entry has empty ModelId: %+v", m)
			}
			if m.Name == "" {
				t.Errorf("AvailableModels entry has empty Name: %+v", m)
			}
			if !strings.HasPrefix(string(m.ModelId), "anthropic/") {
				t.Errorf("got model from provider without auth: %s", m.ModelId)
			}
		}
	})
}
