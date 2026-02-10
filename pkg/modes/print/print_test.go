package print

import (
	"testing"
)

func TestModeConstants(t *testing.T) {
	if ModeText != "text" {
		t.Errorf("expected 'text', got %s", ModeText)
	}
	if ModeJSON != "json" {
		t.Errorf("expected 'json', got %s", ModeJSON)
	}
}

func TestOptions_Defaults(t *testing.T) {
	opts := Options{
		Mode:           ModeText,
		InitialMessage: "hello",
	}
	if opts.Mode != ModeText {
		t.Error("expected text mode")
	}
	if opts.InitialMessage != "hello" {
		t.Error("expected initial message")
	}
	if len(opts.Messages) != 0 {
		t.Error("expected empty messages")
	}
	if len(opts.InitialImages) != 0 {
		t.Error("expected empty images")
	}
}
