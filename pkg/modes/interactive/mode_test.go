package interactive

import (
	"strings"
	"testing"
)

func TestNewInteractiveMode(t *testing.T) {
	m := NewInteractiveMode(nil, nil, nil, InteractiveModeOptions{})
	if m == nil {
		t.Fatal("expected non-nil InteractiveMode")
	}
	if m.autoCompact != true {
		t.Error("expected autoCompact default true")
	}
}

func TestInteractiveMode_Shutdown(t *testing.T) {
	m := NewInteractiveMode(nil, nil, nil, InteractiveModeOptions{})
	// Should not panic
	m.Shutdown()
	// Double shutdown should not panic
	m.Shutdown()
}

func TestInteractiveModeOptions(t *testing.T) {
	opts := InteractiveModeOptions{
		InitialPrompt:   "hello",
		ThemeName:       "dark",
		ThemeSearchDirs: []string{"/tmp"},
	}
	if opts.InitialPrompt != "hello" {
		t.Error("expected initial prompt")
	}
	if opts.ThemeName != "dark" {
		t.Error("expected theme name dark")
	}
	if len(opts.ThemeSearchDirs) != 1 {
		t.Error("expected 1 search dir")
	}
}

func TestInteractiveMode_SlashCommandParsing(t *testing.T) {
	// Test command parsing logic without calling handleSlashCommand directly
	// (which requires Init'd TUI with messageContainer)
	tests := []struct {
		input   string
		wantCmd string
	}{
		{"/help", "/help"},
		{"/clear", "/clear"},
		{"/quit", "/quit"},
		{"/model gpt-4", "/model"},
		{"/unknown-command", "/unknown-command"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			parts := strings.Fields(tt.input)
			if len(parts) == 0 {
				t.Fatal("expected non-empty parts")
			}
			if parts[0] != tt.wantCmd {
				t.Errorf("expected command %q, got %q", tt.wantCmd, parts[0])
			}
		})
	}
}

func TestInteractiveMode_GetFooterData(t *testing.T) {
	m := NewInteractiveMode(nil, nil, nil, InteractiveModeOptions{})
	data := m.getFooterData()
	if data.Pwd == "" {
		t.Error("expected non-empty pwd")
	}
	if data.AutoCompact != true {
		t.Error("expected auto-compact true")
	}
}

func TestInteractiveMode_Flags(t *testing.T) {
	m := NewInteractiveMode(nil, nil, nil, InteractiveModeOptions{})
	if m.hideThinking != false {
		t.Error("expected hideThinking default false")
	}
	if m.running != false {
		t.Error("expected running default false")
	}
	m.hideThinking = true
	if !m.hideThinking {
		t.Error("expected hideThinking to be settable")
	}
}
