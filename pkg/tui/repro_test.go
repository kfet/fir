package tui

import "testing"

func TestMatchesKey_CtrlO(t *testing.T) {
	if !MatchesKey("\x0f", "ctrl+o") {
		t.Error("expected \\x0f to match ctrl+o")
	}
}
