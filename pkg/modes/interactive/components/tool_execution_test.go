package components

import (
	"fmt"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/agent"
)

func TestToolExecution_ReadPending(t *testing.T) {
	args := map[string]any{"path": "/tmp/test.txt"}
	comp := NewToolExecutionComponent("read", args, nil, nil)
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
	comp := NewToolExecutionComponent("read", args, nil, nil)
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
	comp := NewToolExecutionComponent("bash", args, nil, nil)
	lines := comp.Render(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "echo hello") {
		t.Errorf("expected command in output, got %q", joined)
	}
}

func TestToolExecution_BashWithResult(t *testing.T) {
	args := map[string]any{"command": "ls"}
	comp := NewToolExecutionComponent("bash", args, nil, nil)
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
	comp := NewToolExecutionComponent("edit", args, nil, nil)
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
	comp := NewToolExecutionComponent("write", args, nil, nil)
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
	comp := NewToolExecutionComponent("read", args, nil, nil)

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

func TestToolExecution_DisplayHint(t *testing.T) {
	hint := &agent.ToolDisplayHint{
		TitleArgs: []agent.TitleArg{
			{Name: "url", Style: "accent"},
			{Name: "method", Label: "via"},
		},
		ResultMaxLines: 3,
	}
	args := map[string]any{"url": "https://example.com", "method": "GET"}
	comp := NewToolExecutionComponent("http_fetch", args, nil, hint)
	lines := comp.Render(120)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "http_fetch") {
		t.Errorf("expected tool name in output, got %q", joined)
	}
	if !strings.Contains(joined, "example.com") {
		t.Errorf("expected url in output, got %q", joined)
	}
	if !strings.Contains(joined, "GET") {
		t.Errorf("expected method in output, got %q", joined)
	}

	// With result, should truncate to 3 lines
	comp.UpdateResult(&ToolResultData{
		Content: []ToolContentBlock{
			{Type: "text", Text: "line1\nline2\nline3\nline4\nline5"},
		},
	}, false)
	lines = comp.Render(120)
	joined = strings.Join(lines, "\n")
	if !strings.Contains(joined, "line3") {
		t.Errorf("expected line3 visible, got %q", joined)
	}
	if strings.Contains(joined, "line4") {
		t.Errorf("line4 should be truncated, got %q", joined)
	}
	if !strings.Contains(joined, "2 more lines") {
		t.Errorf("expected truncation hint, got %q", joined)
	}
}

func TestToolExecution_DisplayHintUseBox(t *testing.T) {
	hint := &agent.ToolDisplayHint{UseBox: true}
	args := map[string]any{"input": "test"}
	comp := NewToolExecutionComponent("custom_tool", args, nil, hint)
	if !comp.useBox {
		t.Error("expected useBox=true when DisplayHint.UseBox is set")
	}
}

func TestToolExecution_GenericFallbackWithoutHint(t *testing.T) {
	args := map[string]any{"foo": "bar"}
	comp := NewToolExecutionComponent("unknown_tool", args, nil, nil)
	lines := comp.Render(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "unknown_tool") {
		t.Errorf("expected tool name, got %q", joined)
	}
	// Without hint, should show raw JSON args
	if !strings.Contains(joined, "foo") {
		t.Errorf("expected raw args in generic output, got %q", joined)
	}
}

func TestToolExecution_ToolOutputDetails(t *testing.T) {
	hint := &agent.ToolDisplayHint{
		TitleArgs: []agent.TitleArg{{Name: "title", Style: "accent"}},
	}
	args := map[string]any{"title": "check files"}
	comp := NewToolExecutionComponent("aside", args, nil, hint)

	comp.UpdateResult(&ToolResultData{
		Content: []ToolContentBlock{
			{Type: "text", Text: "synthesis result"},
		},
		Details: map[string]any{
			"tool_outputs": []any{
				map[string]any{
					"name":     "Read",
					"title":    "read go.mod contents",
					"output":   "file contents here\nline2\nline3",
					"is_error": false,
				},
				map[string]any{
					"name":     "Bash",
					"output":   "command failed",
					"is_error": true,
				},
			},
		},
	}, false)

	lines := comp.Render(120)
	joined := strings.Join(lines, "\n")

	// Should show tool output headers
	if !strings.Contains(joined, "Read: read go.mod contents") {
		t.Errorf("expected Read tool output with title, got %q", joined)
	}
	if !strings.Contains(joined, "Bash") {
		t.Errorf("expected Bash tool output, got %q", joined)
	}
	if !strings.Contains(joined, "[ERROR]") {
		t.Errorf("expected ERROR tag for failed tool, got %q", joined)
	}
	// Should show synthesis
	if !strings.Contains(joined, "synthesis result") {
		t.Errorf("expected synthesis in output, got %q", joined)
	}
}

func TestToolExecution_ToolOutputDetailsLineTruncation(t *testing.T) {
	args := map[string]any{"title": "test"}
	comp := NewToolExecutionComponent("aside", args, nil, nil)

	// Generate 20 lines — should be truncated to 10
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, fmt.Sprintf("line-%d", i))
	}
	comp.UpdateResult(&ToolResultData{
		Content: []ToolContentBlock{{Type: "text", Text: "done"}},
		Details: map[string]any{
			"tool_outputs": []any{
				map[string]any{
					"name":     "Read",
					"output":   strings.Join(lines, "\n"),
					"is_error": false,
				},
			},
		},
	}, false)

	rendered := comp.Render(120)
	joined := strings.Join(rendered, "\n")
	if !strings.Contains(joined, "line-9") {
		t.Errorf("expected line-9 visible, got %q", joined)
	}
	if strings.Contains(joined, "line-10") {
		t.Errorf("line-10 should be truncated, got %q", joined)
	}
	if !strings.Contains(joined, "10 more lines") {
		t.Errorf("expected truncation hint, got %q", joined)
	}
}
