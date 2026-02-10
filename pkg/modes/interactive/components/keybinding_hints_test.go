package components

import (
	"strings"
	"testing"

	tuicomp "github.com/kfet/pi-go/pkg/tui/components"
)

func TestFormatKeys_Empty(t *testing.T) {
	result := formatKeys(nil)
	if result != "" {
		t.Errorf("expected empty, got %q", result)
	}
}

func TestFormatKeys_Single(t *testing.T) {
	result := formatKeys([]string{"ctrl+c"})
	if result != "ctrl+c" {
		t.Errorf("expected 'ctrl+c', got %q", result)
	}
}

func TestFormatKeys_Multiple(t *testing.T) {
	result := formatKeys([]string{"ctrl+c", "escape"})
	if result != "ctrl+c/escape" {
		t.Errorf("expected 'ctrl+c/escape', got %q", result)
	}
}

func TestEditorKey(t *testing.T) {
	// EditorKey should return a non-empty string for known actions
	result := EditorKey(tuicomp.ActSelectConfirm)
	if result == "" {
		t.Error("expected non-empty editor key for selectConfirm")
	}
}

func TestKeyHint(t *testing.T) {
	result := KeyHint(tuicomp.ActSelectConfirm, "confirm")
	if !strings.Contains(result, "confirm") {
		t.Errorf("expected 'confirm' in hint, got %q", result)
	}
	if !strings.Contains(result, "\x1b[") {
		t.Error("expected ANSI escapes in hint")
	}
}

func TestRawKeyHint(t *testing.T) {
	result := RawKeyHint("↑↓", "navigate")
	if !strings.Contains(result, "↑↓") {
		t.Errorf("expected '↑↓' in hint, got %q", result)
	}
	if !strings.Contains(result, "navigate") {
		t.Errorf("expected 'navigate' in hint, got %q", result)
	}
}
