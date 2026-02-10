package components

import (
	"strings"
	"testing"
)

func TestToolExecution_ReadPending(t *testing.T) {
	args := map[string]any{"path": "/tmp/test.txt"}
	comp := NewToolExecutionComponent("read", args, nil)
	lines := comp.Render(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "read") {
		t.Errorf("expected 'read' in output, got %q", joined)
	}
	if !strings.Contains(joined, "test.txt") {
		t.Errorf("expected file path in output, got %q", joined)
	}
}

func TestToolExecution_ReadWithResult(t *testing.T) {
	args := map[string]any{"path": "/tmp/test.txt"}
	comp := NewToolExecutionComponent("read", args, nil)
	comp.UpdateResult(&ToolResultData{
		Content: []ToolContentBlock{
			{Type: "text", Text: "line1\nline2\nline3"},
		},
	}, false)
	lines := comp.Render(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "line1") {
		t.Errorf("expected content in output, got %q", joined)
	}
}

func TestToolExecution_BashPending(t *testing.T) {
	args := map[string]any{"command": "echo hello"}
	comp := NewToolExecutionComponent("bash", args, nil)
	lines := comp.Render(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "echo hello") {
		t.Errorf("expected command in output, got %q", joined)
	}
}

func TestToolExecution_BashWithResult(t *testing.T) {
	args := map[string]any{"command": "ls"}
	comp := NewToolExecutionComponent("bash", args, nil)
	comp.UpdateResult(&ToolResultData{
		Content: []ToolContentBlock{
			{Type: "text", Text: "file1.txt\nfile2.txt"},
		},
	}, false)
	lines := comp.Render(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "file1.txt") {
		t.Errorf("expected output content, got %q", joined)
	}
}

func TestToolExecution_EditWithDiff(t *testing.T) {
	args := map[string]any{"path": "/tmp/test.go", "oldText": "foo", "newText": "bar"}
	comp := NewToolExecutionComponent("edit", args, nil)
	comp.UpdateResult(&ToolResultData{
		Content: []ToolContentBlock{
			{Type: "text", Text: "ok"},
		},
		Details: map[string]any{
			"diff": "- 1 foo\n+ 1 bar",
		},
	}, false)
	lines := comp.Render(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "edit") {
		t.Errorf("expected 'edit' in output, got %q", joined)
	}
}

func TestToolExecution_WriteDisplay(t *testing.T) {
	args := map[string]any{"path": "/tmp/test.txt", "content": "hello world"}
	comp := NewToolExecutionComponent("write", args, nil)
	lines := comp.Render(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "write") {
		t.Errorf("expected 'write' in output, got %q", joined)
	}
	if !strings.Contains(joined, "hello world") {
		t.Errorf("expected content in output, got %q", joined)
	}
}

func TestToolExecution_Expanded(t *testing.T) {
	args := map[string]any{"path": "/tmp/test.txt"}
	comp := NewToolExecutionComponent("read", args, nil)

	// Create a long output
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, "line content")
	}
	comp.UpdateResult(&ToolResultData{
		Content: []ToolContentBlock{
			{Type: "text", Text: strings.Join(lines, "\n")},
		},
	}, false)

	// Collapsed should have fewer rendered lines
	collapsedLines := comp.Render(80)

	comp.SetExpanded(true)
	expandedLines := comp.Render(80)

	if len(expandedLines) <= len(collapsedLines) {
		t.Errorf("expected expanded to have more lines than collapsed: expanded=%d collapsed=%d",
			len(expandedLines), len(collapsedLines))
	}
}

func TestShortenPath(t *testing.T) {
	// Just test that it doesn't panic
	result := shortenPath("/tmp/test.txt")
	if result != "/tmp/test.txt" {
		t.Logf("shortenPath('/tmp/test.txt') = %q", result)
	}
	result = shortenPath("")
	if result != "" {
		t.Errorf("expected empty string for empty input, got %q", result)
	}
}

func TestFormatSize(t *testing.T) {
	tests := []struct {
		input    int
		expected string
	}{
		{500, "500B"},
		{1024, "1.0KB"},
		{51200, "50.0KB"},
		{1048576, "1.0MB"},
	}
	for _, tc := range tests {
		got := formatSize(tc.input)
		if got != tc.expected {
			t.Errorf("formatSize(%d) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}
