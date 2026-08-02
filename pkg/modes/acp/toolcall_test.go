package acp

import (
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/agent"
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

func TestBuildToolTitleWithHint(t *testing.T) {
	rexecHint := &agent.ToolDisplayHint{
		TitleArgs: []agent.TitleArg{
			{Name: "host", Style: "accent"},
			{Name: "command"},
		},
		UseBox: true,
	}
	asideHint := &agent.ToolDisplayHint{
		TitleArgs: []agent.TitleArg{
			{Name: "title", Style: "accent"},
			{Name: "escalate", Style: "warning", Label: "↑ escalated"},
			{Name: "delegate", Style: "muted", Label: "↓ delegated"},
		},
	}
	patternHint := &agent.ToolDisplayHint{
		TitleArgs: []agent.TitleArg{{Name: "q", Style: "pattern"}},
	}
	longCmd := strings.Repeat("x", 200)

	tests := []struct {
		name string
		tool string
		args map[string]any
		hint *agent.ToolDisplayHint
		want string
	}{
		{
			name: "hint absent falls back to tool name",
			tool: "rexec",
			args: map[string]any{"host": "zbox", "command": "uname -a"},
			hint: nil,
			want: "rexec",
		},
		{
			name: "rexec host + command",
			tool: "rexec",
			args: map[string]any{"host": "zboxserver", "command": "make all"},
			hint: rexecHint,
			want: "rexec zboxserver make all",
		},
		{
			name: "missing arg skipped",
			tool: "rexec",
			args: map[string]any{"command": "ls"},
			hint: rexecHint,
			want: "rexec ls",
		},
		{
			name: "boolean true renders label badge",
			tool: "aside",
			args: map[string]any{"title": "check", "escalate": true},
			hint: asideHint,
			want: "aside check ↑ escalated",
		},
		{
			name: "boolean false omitted",
			tool: "aside",
			args: map[string]any{"title": "check", "escalate": false, "delegate": true},
			hint: asideHint,
			want: "aside check ↓ delegated",
		},
		{
			name: "pattern style wrapped",
			tool: "search",
			args: map[string]any{"q": "TODO"},
			hint: patternHint,
			want: "search /TODO/",
		},
		{
			name: "long command truncated to 80 + ellipsis",
			tool: "rexec",
			args: map[string]any{"host": "h", "command": longCmd},
			hint: rexecHint,
			// "rexec h " is 8 chars; total truncated at 80.
			want: "rexec h " + strings.Repeat("x", 72) + "...",
		},
		{
			name: "backticks escaped",
			tool: "rexec",
			args: map[string]any{"host": "h", "command": "echo `id`"},
			hint: rexecHint,
			want: "rexec h echo \\`id\\`",
		},
	}
	for _, tt := range tests {
		got := BuildToolTitleWithHint(tt.tool, tt.args, tt.hint)
		if got != tt.want {
			t.Errorf("%s: BuildToolTitleWithHint(%q, %v) = %q, want %q", tt.name, tt.tool, tt.args, got, tt.want)
		}
	}
}

func TestMapToolKindForCall(t *testing.T) {
	rexecHint := &agent.ToolDisplayHint{TitleArgs: []agent.TitleArg{{Name: "host"}, {Name: "command"}}}
	tests := []struct {
		name string
		tool string
		args map[string]any
		hint *agent.ToolDisplayHint
		want acpsdk.ToolKind
	}{
		{"builtin unchanged", "bash", nil, nil, "execute"},
		{"command arg via args", "rexec", map[string]any{"command": "ls"}, nil, "execute"},
		{"command arg via hint only", "rexec", nil, rexecHint, "execute"},
		{"pattern arg", "mysearch", map[string]any{"pattern": "x"}, nil, "search"},
		{"path arg is read", "myread", map[string]any{"path": "/tmp/f"}, nil, "read"},
		{"content arg is edit", "mywrite", map[string]any{"path": "/tmp/f", "content": "x"}, nil, "edit"},
		{"no signal is other", "mystery", map[string]any{"foo": "bar"}, nil, "other"},
	}
	for _, tt := range tests {
		got := MapToolKindForCall(tt.tool, tt.args, tt.hint)
		if got != tt.want {
			t.Errorf("%s: MapToolKindForCall(%q, %v) = %q, want %q", tt.name, tt.tool, tt.args, got, tt.want)
		}
	}
}

func TestBuildToolInitialContentWithHint(t *testing.T) {
	rexecHint := &agent.ToolDisplayHint{
		TitleArgs: []agent.TitleArg{
			{Name: "host", Style: "accent"},
			{Name: "command"},
		},
		UseBox: true,
	}

	// Boxed hint with host + command produces a fenced block preceded by host.
	content := BuildToolInitialContentWithHint("rexec", map[string]any{"host": "zbox", "command": "make all"}, rexecHint)
	if len(content) != 1 || content[0].Content == nil {
		t.Fatalf("expected one text content, got %v", content)
	}
	got := content[0].Content.Content.Text.Text
	want := "host: zbox\n```\n$ make all\n```"
	if got != want {
		t.Errorf("rexec init content = %q, want %q", got, want)
	}

	// No command: context-only, no fenced block.
	content = BuildToolInitialContentWithHint("rexec", map[string]any{"host": "zbox"}, rexecHint)
	if len(content) != 1 || content[0].Content.Content.Text.Text != "host: zbox" {
		t.Errorf("host-only init content = %v", content)
	}

	// use_box false falls through to builtin behaviour (nil for unknown tools).
	noBox := &agent.ToolDisplayHint{TitleArgs: rexecHint.TitleArgs}
	content = BuildToolInitialContentWithHint("rexec", map[string]any{"host": "zbox", "command": "ls"}, noBox)
	if content != nil {
		t.Errorf("expected nil content when use_box is false, got %v", content)
	}

	// Builtin bash unchanged through the wrapper.
	content = BuildToolInitialContentWithHint("bash", map[string]any{"command": "echo hi"}, nil)
	if len(content) != 1 || content[0].Content == nil {
		t.Error("bash should still produce text content through the wrapper")
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
