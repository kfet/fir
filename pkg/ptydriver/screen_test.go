package ptydriver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScreenBasicWrite(t *testing.T) {
	s := NewScreen(24, 80)
	s.Write([]byte("hello world"))
	out := s.CaptureVisible()
	assert.Equal(t, "hello world", out)
}

func TestScreenNewline(t *testing.T) {
	s := NewScreen(24, 80)
	s.Write([]byte("line1\r\nline2\r\nline3"))
	out := s.CaptureVisible()
	assert.Equal(t, "line1\nline2\nline3", out)
}

func TestScreenCursorMovement(t *testing.T) {
	s := NewScreen(24, 80)
	s.Write([]byte("ABCDE"))
	// Move cursor back 3, overwrite
	s.Write([]byte("\x1b[3Dxyz"))
	out := s.CaptureVisible()
	assert.Equal(t, "ABxyz", out)
}

func TestScreenEraseInLine(t *testing.T) {
	s := NewScreen(24, 80)
	s.Write([]byte("hello world"))
	// Move to col 5, erase to end of line
	s.Write([]byte("\x1b[6G\x1b[K"))
	out := s.CaptureVisible()
	assert.Equal(t, "hello", out)
}

func TestScreenEraseDisplay(t *testing.T) {
	s := NewScreen(24, 80)
	s.Write([]byte("line1\r\nline2\r\nline3"))
	s.Write([]byte("\x1b[2J")) // clear all
	out := s.CaptureVisible()
	assert.Equal(t, "", out)
}

func TestScreenScrollback(t *testing.T) {
	s := NewScreen(3, 80) // tiny screen
	s.Write([]byte("a\r\nb\r\nc\r\nd\r\ne"))
	// Visible should be c, d, e (last 3 rows)
	vis := s.CaptureVisible()
	assert.Equal(t, "c\nd\ne", vis)
	// Capture with scrollback
	all := s.Capture(5)
	assert.Equal(t, "a\nb\nc\nd\ne", all)
}

func TestScreenCursorPosition(t *testing.T) {
	s := NewScreen(24, 80)
	// Move to row 3, col 5 (1-indexed) and write
	s.Write([]byte("\x1b[3;5Htest"))
	out := s.CaptureVisible()
	lines := splitLines(out)
	require.True(t, len(lines) >= 3)
	assert.Equal(t, "", lines[0])
	assert.Equal(t, "", lines[1])
	assert.Equal(t, "    test", lines[2])
}

func TestScreenSGRIgnored(t *testing.T) {
	s := NewScreen(24, 80)
	// Bold red text — SGR params should be ignored, text preserved
	s.Write([]byte("\x1b[1;31mhello\x1b[0m"))
	out := s.CaptureVisible()
	assert.Equal(t, "hello", out)
}

func TestScreenOSCIgnored(t *testing.T) {
	s := NewScreen(24, 80)
	// Set window title OSC, then text
	s.Write([]byte("\x1b]0;My Title\x07hello"))
	out := s.CaptureVisible()
	assert.Equal(t, "hello", out)
}

func TestScreenTab(t *testing.T) {
	s := NewScreen(24, 80)
	s.Write([]byte("a\tb"))
	out := s.CaptureVisible()
	assert.Equal(t, "a       b", out)
}

func TestScreenBackspace(t *testing.T) {
	s := NewScreen(24, 80)
	s.Write([]byte("abc\b\bd"))
	out := s.CaptureVisible()
	assert.Equal(t, "adc", out)
}

func TestScreenWrapAround(t *testing.T) {
	s := NewScreen(5, 10)
	s.Write([]byte("0123456789ABCDE"))
	vis := s.CaptureVisible()
	assert.Equal(t, "0123456789\nABCDE", vis)
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	lines = append(lines, s[start:])
	return lines
}
