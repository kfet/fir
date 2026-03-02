package tools

import (
	"testing"
)

func TestStripAnsi(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"hello", "hello"},
		{"\x1b[32mgreen\x1b[0m", "green"},
		{"\x1b[1;31mbold red\x1b[0m text", "bold red text"},
		{"no escapes here", "no escapes here"},
	}
	for _, tt := range tests {
		got := StripAnsi(tt.input)
		if got != tt.want {
			t.Errorf("StripAnsi(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestAppendColorEnv(t *testing.T) {
	env := AppendColorEnv([]string{"PATH=/usr/bin"})
	hasCLI, hasCLIForce, hasForce := false, false, false
	for _, e := range env {
		if e == "CLICOLOR=1" {
			hasCLI = true
		}
		if e == "CLICOLOR_FORCE=3" {
			hasCLIForce = true
		}
		if e == "FORCE_COLOR=1" {
			hasForce = true
		}
	}
	if !hasCLI {
		t.Error("missing CLICOLOR=1")
	}
	if !hasCLIForce {
		t.Error("missing CLICOLOR_FORCE=3")
	}
	if !hasForce {
		t.Error("missing FORCE_COLOR=1")
	}
}

func TestAppendColorEnv_NoOverwrite(t *testing.T) {
	env := AppendColorEnv([]string{"CLICOLOR=0", "CLICOLOR_FORCE=0", "FORCE_COLOR=0"})
	for _, e := range env {
		if e == "CLICOLOR=1" {
			t.Error("should not overwrite existing CLICOLOR")
		}
		if e == "CLICOLOR_FORCE=3" {
			t.Error("should not overwrite existing CLICOLOR_FORCE")
		}
		if e == "FORCE_COLOR=1" {
			t.Error("should not overwrite existing FORCE_COLOR")
		}
	}
}
