package tui

import "testing"

func TestIsWhitespaceChar(t *testing.T) {
	if !IsWhitespaceChar(' ') {
		t.Error("space should be whitespace")
	}
	if IsWhitespaceChar('a') {
		t.Error("'a' should not be whitespace")
	}
}

func TestIsPunctuationChar(t *testing.T) {
	if !IsPunctuationChar('.') {
		t.Error("'.' should be punctuation")
	}
	if IsPunctuationChar('a') {
		t.Error("'a' should not be punctuation")
	}
}

func TestIsPureASCIIPrintable(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", true},
		{"simple ascii", "hello world", true},
		{"all printable", " ~", true}, // 0x20 and 0x7e
		{"with tab", "a\tb", false},
		{"with newline", "a\nb", false},
		{"with escape", "\x1b[31m", false},
		{"with null", "\x00", false},
		{"with DEL", "\x7f", false},
		{"unicode", "héllo", false},
		{"emoji", "hi 👋", false},
		{"digits and punctuation", "foo_bar-123!@#", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPureASCIIPrintable(tt.in); got != tt.want {
				t.Errorf("isPureASCIIPrintable(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
