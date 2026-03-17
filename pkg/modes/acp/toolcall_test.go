package acp

import (
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
)

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

func TestBuildToolInitialContent(t *testing.T) {
	content := BuildToolInitialContent("bash", map[string]any{"command": "echo hi"})
	if len(content) != 1 || content[0].Content == nil {
		t.Error("bash should produce text content")
	}

	content = BuildToolInitialContent("write", map[string]any{"path": "foo.go", "content": "package main"})
	if len(content) != 1 || content[0].Diff == nil {
		t.Error("write should produce diff content")
	}

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

