package components

import (
	"strings"
	"testing"
)

func TestLoginDialogComponent_Render(t *testing.T) {
	comp := NewLoginDialogComponent(nil, "github", "GitHub", func(bool, string) {})
	lines := comp.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected rendered output")
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "GitHub") {
		t.Errorf("expected 'GitHub' in output, got %q", joined)
	}
}

func TestLoginDialogComponent_Cancel(t *testing.T) {
	completed := false
	success := true
	comp := NewLoginDialogComponent(nil, "github", "GitHub", func(s bool, msg string) {
		completed = true
		success = s
	})
	comp.Cancel()
	if !completed {
		t.Fatal("expected onComplete to be called")
	}
	if success {
		t.Fatal("expected success=false on cancel")
	}
}

func TestLoginDialogComponent_SetMessage(t *testing.T) {
	comp := NewLoginDialogComponent(nil, "github", "GitHub", func(bool, string) {})
	comp.SetMessage("Opening browser...")
	lines := comp.Render(80)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Opening browser") {
		t.Errorf("expected message in output, got %q", joined)
	}
}
