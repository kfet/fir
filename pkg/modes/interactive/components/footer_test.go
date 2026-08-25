package components

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kfet/tui"
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
			Pwd:             "/home/user/project",
			ModelID:         "claude-3.5-sonnet",
			TotalInput:      5000,
			TotalOutput:     1500,
			ContextWindow:   200000,
			AutoCompactMode: "client",
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
			Pwd:            "/p",
			ModelID:        "model",
			TotalInput:     90000,
			ContextWindow:  100000,
			ContextPercent: 95.0, // >90% triggers error coloring
			ContextTokens:  95000,
		}
	})

	lines := f.Render(80)
	// Should contain the percentage string
	found := false
	for _, line := range lines {
		if strings.Contains(line, "95.0%") {
			found = true
		}
	}
	if !found {
		t.Error("expected 95.0% in output")
	}

	// Should have ANSI coloring for >90% context
	hasANSI := false
	for _, line := range lines {
		if strings.Contains(line, "\x1b[") {
			hasANSI = true
		}
	}
	if !hasANSI {
		t.Error("expected ANSI escapes for error context coloring")
	}
}

func TestFooterComponent_WithProvider(t *testing.T) {
	f := NewFooterComponent(func() FooterData {
		return FooterData{
			Pwd:           "/project",
			ModelID:       "claude-3.5-sonnet",
			ModelProvider: "anthropic",
			ContextWindow: 200000,
		}
	})

	lines := f.Render(120)
	// Provider should be displayed even when MultipleProviders is false
	found := false
	for _, line := range lines {
		if strings.Contains(line, "anthropic") && strings.Contains(line, "claude-3.5-sonnet") {
			found = true
		}
	}
	if !found {
		t.Error("expected provider and model in output, got:", lines)
	}
}

func TestFooterComponent_PlanProgress(t *testing.T) {
	f := NewFooterComponent(func() FooterData {
		return FooterData{
			Pwd:             "/home/user",
			ModelID:         "test-model",
			ContextWindow:   128000,
			PlanTotal:       5,
			PlanCompleted:   2,
			PlanCurrentStep: "Write tests",
			PlanKeyHint:     "ctrl+r",
		}
	})

	lines := f.Render(120)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "📋") {
		t.Fatal("expected plan emoji in footer")
	}
	if !strings.Contains(joined, "2/5") {
		t.Fatal("expected plan progress count")
	}
	if !strings.Contains(joined, "Write tests") {
		t.Fatal("expected current step name")
	}
	if !strings.Contains(joined, "ctrl+r") {
		t.Fatal("expected keybinding hint")
	}
}

func TestFooterComponent_PlanCompleted(t *testing.T) {
	f := NewFooterComponent(func() FooterData {
		return FooterData{
			Pwd:           "/home/user",
			ModelID:       "test-model",
			ContextWindow: 128000,
			PlanTotal:     3,
			PlanCompleted: 3,
		}
	})

	lines := f.Render(120)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "3/3") {
		t.Fatal("expected completed plan count")
	}
}

func TestFooterComponent_PlanCompletedWithTitle(t *testing.T) {
	f := NewFooterComponent(func() FooterData {
		return FooterData{
			Pwd:           "/home/user",
			ModelID:       "test-model",
			ContextWindow: 128000,
			PlanTotal:     2,
			PlanCompleted: 2,
			PlanTitle:     "Implement caching",
		}
	})

	lines := f.Render(120)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "2/2") {
		t.Fatal("expected completed plan count")
	}
	if !strings.Contains(joined, "Implement caching") {
		t.Fatal("expected plan title in footer when plan is complete")
	}
}

func TestFooterComponent_NoPlan(t *testing.T) {
	f := NewFooterComponent(func() FooterData {
		return FooterData{
			Pwd:           "/home/user",
			ModelID:       "test-model",
			ContextWindow: 128000,
		}
	})

	lines := f.Render(120)
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "📋") {
		t.Fatal("should not show plan indicator when no plan")
	}
}

func TestUpdateBadge(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{"empty means up to date", "", ""},
		{"whitespace only", "   ", ""},
		{"plain version", "0.99.1", "⬆ 0.99.1 · fir update"},
		{"v prefix stripped", "v0.99.1", "⬆ 0.99.1 · fir update"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := updateBadge(tt.version); got != tt.want {
				t.Errorf("updateBadge(%q) = %q, want %q", tt.version, got, tt.want)
			}
		})
	}
}

func TestFooterComponent_UpdateAvailable(t *testing.T) {
	f := NewFooterComponent(func() FooterData {
		return FooterData{
			Pwd:           "/home/user",
			ModelID:       "test-model",
			ContextWindow: 128000,
			UpdateVersion: "0.99.1",
		}
	})

	joined := strings.Join(f.Render(120), "\n")
	if !strings.Contains(joined, "⬆ 0.99.1") {
		t.Fatalf("expected update indicator in footer, got %q", joined)
	}
	if !strings.Contains(joined, "fir update") {
		t.Fatalf("expected actionable 'fir update' hint in footer, got %q", joined)
	}
}

func TestFooterComponent_NoUpdateAvailable(t *testing.T) {
	f := NewFooterComponent(func() FooterData {
		return FooterData{
			Pwd:           "/home/user",
			ModelID:       "test-model",
			ContextWindow: 128000,
		}
	})

	joined := strings.Join(f.Render(120), "\n")
	if strings.Contains(joined, "⬆") {
		t.Fatalf("should not show update indicator when binary is current, got %q", joined)
	}
}

func TestVersionLabel(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{"empty", "", ""},
		{"whitespace", "   ", ""},
		{"plain", "0.99.1", "fir 0.99.1"},
		{"leading v stripped", "v0.99.1", "fir 0.99.1"},
		{"dev", "dev", "fir dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := versionLabel(tt.version); got != tt.want {
				t.Errorf("versionLabel(%q) = %q, want %q", tt.version, got, tt.want)
			}
		})
	}
}

func TestFooterComponent_VersionShown(t *testing.T) {
	f := NewFooterComponent(func() FooterData {
		return FooterData{
			Pwd:           "/tmp/proj",
			ModelID:       "test-model",
			ContextWindow: 128000,
			Version:       "0.99.1",
		}
	})

	lines := f.Render(120)
	if !strings.Contains(lines[0], "fir 0.99.1") {
		t.Fatalf("expected version on pwd line, got %q", lines[0])
	}
	if w := tui.VisibleWidth(lines[0]); w > 120 {
		t.Fatalf("pwd line width %d exceeds 120: %q", w, lines[0])
	}
	// Right-aligned: version ends the line.
	if !strings.HasSuffix(strings.TrimSpace(tui.StripAnsi(lines[0])), "fir 0.99.1") {
		t.Fatalf("expected version right-aligned, got %q", lines[0])
	}
}

func TestFooterComponent_VersionAlongsideExtensionStatuses(t *testing.T) {
	f := NewFooterComponent(func() FooterData {
		return FooterData{
			Pwd:               "/tmp/proj",
			ModelID:           "test-model",
			ContextWindow:     128000,
			Version:           "0.99.1",
			ExtensionStatuses: map[string]string{"a": "ext-ok"},
		}
	})

	lines := f.Render(120)
	if !strings.Contains(lines[0], "ext-ok") {
		t.Fatalf("expected extension status retained, got %q", lines[0])
	}
	plain := strings.TrimSpace(tui.StripAnsi(lines[0]))
	if !strings.HasSuffix(plain, "ext-ok  fir 0.99.1") {
		t.Fatalf("expected version after extension status, got %q", plain)
	}
}

func TestFooterComponent_VersionDroppedWhenTooNarrow(t *testing.T) {
	data := func() FooterData {
		return FooterData{
			Pwd:               "/tmp/some/deeply/nested/project/dir",
			ModelID:           "test-model",
			ContextWindow:     128000,
			Version:           "0.99.1",
			ExtensionStatuses: map[string]string{"a": "ext-ok"},
		}
	}
	f := NewFooterComponent(data)

	lines := f.Render(40)
	if strings.Contains(lines[0], "fir 0.99.1") {
		t.Fatalf("version should be dropped before extension status, got %q", lines[0])
	}
	if !strings.Contains(lines[0], "ext-ok") {
		t.Fatalf("extension status should survive when version is dropped, got %q", lines[0])
	}
	if w := tui.VisibleWidth(lines[0]); w > 40 {
		t.Fatalf("pwd line width %d exceeds 40: %q", w, lines[0])
	}
}

func TestFooterComponent_VersionTinyWidths(t *testing.T) {
	f := NewFooterComponent(func() FooterData {
		return FooterData{
			Pwd:           "/tmp/proj",
			ModelID:       "test-model",
			ContextWindow: 128000,
			Version:       "0.99.1",
		}
	})

	for w := 0; w <= 25; w++ {
		lines := f.Render(w)
		if got := tui.VisibleWidth(lines[0]); got > w {
			t.Fatalf("width %d: pwd line width %d exceeds it: %q", w, got, lines[0])
		}
	}
}

func TestFooterComponent_NoVersion(t *testing.T) {
	f := NewFooterComponent(func() FooterData {
		return FooterData{
			Pwd:           "/tmp/proj",
			ModelID:       "test-model",
			ContextWindow: 128000,
		}
	})

	lines := f.Render(120)
	if strings.Contains(lines[0], "fir ") {
		t.Fatalf("expected no version text when Version is empty, got %q", lines[0])
	}
	if plain := strings.TrimSpace(tui.StripAnsi(lines[0])); plain != "/tmp/proj" {
		t.Fatalf("expected bare pwd with no separator, got %q", plain)
	}
}

// A multi-byte pwd must not be sliced mid-rune by the middle-ellipsis
// truncation, and must never render wider than the terminal.
func TestFooterComponent_UnicodePwdTruncation(t *testing.T) {
	f := NewFooterComponent(func() FooterData {
		return FooterData{
			Pwd:           "/home/user/Документы/проекты/очень/длинный/путь",
			ModelID:       "test-model",
			ContextWindow: 128000,
			Version:       "0.99.1",
		}
	})

	for w := 1; w <= 60; w++ {
		lines := f.Render(w)
		plain := tui.StripAnsi(lines[0])
		if !utf8.ValidString(plain) {
			t.Fatalf("width %d: pwd line is not valid UTF-8: %q", w, plain)
		}
		if got := tui.VisibleWidth(lines[0]); got > w {
			t.Fatalf("width %d: pwd line width %d exceeds it: %q", w, got, plain)
		}
	}
}
