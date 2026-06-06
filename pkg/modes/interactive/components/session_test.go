package components

import (
	"strings"
	"testing"
)

func TestSessionComponent_RendersLines(t *testing.T) {
	lines := []string{"Session Info", "", "Version: 1.2.3", "ID: abc123"}
	c := NewSessionComponent(lines)
	joined := strings.Join(c.Render(80), "\n")

	for _, want := range []string{"Session Info", "Version: 1.2.3", "ID: abc123"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected rendered output to contain %q, got:\n%s", want, joined)
		}
	}
}

func TestSessionComponent_SetLines(t *testing.T) {
	c := NewSessionComponent([]string{"old line"})
	if !strings.Contains(strings.Join(c.Render(80), "\n"), "old line") {
		t.Fatal("expected initial line")
	}

	c.SetLines([]string{"new line"})
	joined := strings.Join(c.Render(80), "\n")
	if strings.Contains(joined, "old line") {
		t.Fatalf("did not expect old line after SetLines, got:\n%s", joined)
	}
	if !strings.Contains(joined, "new line") {
		t.Fatalf("expected new line after SetLines, got:\n%s", joined)
	}
}

func TestSessionComponent_Empty(t *testing.T) {
	c := NewSessionComponent(nil)
	// Should render without panicking even with no lines.
	_ = c.Render(80)
}
