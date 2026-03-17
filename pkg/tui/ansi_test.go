package tui

import (
	"strings"
	"testing"
)

// --- ExtractAnsiCode ---

func TestExtractAnsiCode_SGR(t *testing.T) {
	s := "\x1b[31m"
	code, length := ExtractAnsiCode(s, 0)
	if code != "\x1b[31m" || length != 5 {
		t.Errorf("expected (\\x1b[31m, 5), got (%q, %d)", code, length)
	}
}

func TestExtractAnsiCode_NoCode(t *testing.T) {
	code, length := ExtractAnsiCode("hello", 0)
	if code != "" || length != 0 {
		t.Errorf("expected empty, got (%q, %d)", code, length)
	}
}

func TestExtractAnsiCode_OSC(t *testing.T) {
	s := "\x1b]8;;url\x07"
	code, length := ExtractAnsiCode(s, 0)
	if code != s || length != len(s) {
		t.Errorf("expected full OSC, got (%q, %d)", code, length)
	}
}

func TestExtractAnsiCode_APC(t *testing.T) {
	s := "\x1b_data\x07"
	code, length := ExtractAnsiCode(s, 0)
	if code != s || length != len(s) {
		t.Errorf("expected full APC, got (%q, %d)", code, length)
	}
}

func TestExtractAnsiCode_MidString(t *testing.T) {
	s := "abc\x1b[1mdef"
	code, length := ExtractAnsiCode(s, 3)
	if code != "\x1b[1m" || length != 4 {
		t.Errorf("expected (\\x1b[1m, 4), got (%q, %d)", code, length)
	}
}

// --- AnsiCodeTracker ---

func TestAnsiCodeTracker_Bold(t *testing.T) {
	var tr AnsiCodeTracker
	tr.Process("\x1b[1m")
	if !tr.HasActiveCodes() {
		t.Error("expected active codes")
	}
	if codes := tr.GetActiveCodes(); codes != "\x1b[1m" {
		t.Errorf("expected bold code, got %q", codes)
	}
}

func TestAnsiCodeTracker_Reset(t *testing.T) {
	var tr AnsiCodeTracker
	tr.Process("\x1b[1m")
	tr.Process("\x1b[0m")
	if tr.HasActiveCodes() {
		t.Error("expected no active codes after reset")
	}
}

func TestAnsiCodeTracker_FgColor(t *testing.T) {
	var tr AnsiCodeTracker
	tr.Process("\x1b[31m")
	codes := tr.GetActiveCodes()
	if codes != "\x1b[31m" {
		t.Errorf("expected fg color, got %q", codes)
	}
}

func TestAnsiCodeTracker_256Color(t *testing.T) {
	var tr AnsiCodeTracker
	tr.Process("\x1b[38;5;240m")
	codes := tr.GetActiveCodes()
	if !strings.Contains(codes, "38;5;240") {
		t.Errorf("expected 256-color code, got %q", codes)
	}
}

func TestAnsiCodeTracker_RGB(t *testing.T) {
	var tr AnsiCodeTracker
	tr.Process("\x1b[38;2;255;128;0m")
	codes := tr.GetActiveCodes()
	if !strings.Contains(codes, "38;2;255;128;0") {
		t.Errorf("expected RGB code, got %q", codes)
	}
}

func TestAnsiCodeTracker_Multiple(t *testing.T) {
	var tr AnsiCodeTracker
	tr.Process("\x1b[1m")
	tr.Process("\x1b[31m")
	codes := tr.GetActiveCodes()
	if !strings.Contains(codes, "1") || !strings.Contains(codes, "31") {
		t.Errorf("expected bold+red, got %q", codes)
	}
}

func TestAnsiCodeTracker_LineEndReset(t *testing.T) {
	var tr AnsiCodeTracker
	tr.Process("\x1b[4m")
	r := tr.GetLineEndReset()
	if r != "\x1b[24m" {
		t.Errorf("expected underline off, got %q", r)
	}

	tr.Reset()
	tr.Process("\x1b[1m")
	r = tr.GetLineEndReset()
	if r != "" {
		t.Errorf("expected empty for bold, got %q", r)
	}
}
