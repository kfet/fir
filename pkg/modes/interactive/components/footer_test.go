package components

import (
	"strings"
	"testing"
)

func TestFormatTokens_Small(t *testing.T) {
	if formatTokens(500) != "500" {
		t.Errorf("expected '500', got %q", formatTokens(500))
	}
}

func TestFormatTokens_Thousands(t *testing.T) {
	result := formatTokens(2500)
	if result != "2.5k" {
		t.Errorf("expected '2.5k', got %q", result)
	}
}

func TestFormatTokens_LargeThousands(t *testing.T) {
	result := formatTokens(50000)
	if result != "50k" {
		t.Errorf("expected '50k', got %q", result)
	}
}

func TestFormatTokens_Millions(t *testing.T) {
	result := formatTokens(2500000)
	if result != "2.5M" {
		t.Errorf("expected '2.5M', got %q", result)
	}
}

func TestSanitizeStatusText(t *testing.T) {
	result := sanitizeStatusText("hello\n  world\ttab")
	if result != "hello world tab" {
		t.Errorf("expected 'hello world tab', got %q", result)
	}
}

func TestFooterComponent_Render(t *testing.T) {
	f := NewFooterComponent(func() FooterData {
		return FooterData{
			Pwd:           "/home/user/project",
			ModelID:       "claude-3.5-sonnet",
			TotalInput:    5000,
			TotalOutput:   1500,
			ContextWindow: 200000,
			AutoCompact:   true,
		}
	})

	lines := f.Render(80)
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines, got %d", len(lines))
	}
	// First line should contain the path
	if !strings.Contains(lines[0], "project") {
		t.Error("expected pwd in first line")
	}
}

func TestFooterComponent_WithGitBranch(t *testing.T) {
	f := NewFooterComponent(func() FooterData {
		return FooterData{
			Pwd:           "/project",
			GitBranch:     "main",
			ModelID:       "gpt-4",
			ContextWindow: 128000,
		}
	})

	lines := f.Render(80)
	found := false
	for _, line := range lines {
		if strings.Contains(line, "main") {
			found = true
		}
	}
	if !found {
		t.Error("expected git branch in output")
	}
}

func TestFooterComponent_ContextWarning(t *testing.T) {
	f := NewFooterComponent(func() FooterData {
		return FooterData{
			Pwd:           "/p",
			ModelID:       "model",
			TotalInput:    90000,
			ContextWindow: 100000,
		}
	})

	lines := f.Render(80)
	// Should have error coloring for >90% context
	found := false
	for _, line := range lines {
		if strings.Contains(line, "\x1b[") {
			found = true
		}
	}
	if !found {
		t.Error("expected ANSI escapes for warning/error context")
	}
}
