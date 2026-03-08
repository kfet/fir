// Ported from: packages/coding-agent/src/core/tools/truncate.ts
// Upstream hash: 1caadb2e
package tools

import (
	"strings"
	"testing"
)

func TestFormatSize(t *testing.T) {
	tests := []struct {
		bytes int
		want  string
	}{
		{0, "0B"},
		{512, "512B"},
		{1023, "1023B"},
		{1024, "1.0KB"},
		{1536, "1.5KB"},
		{51200, "50.0KB"},
		{1048576, "1.0MB"},
		{2621440, "2.5MB"},
	}
	for _, tt := range tests {
		got := FormatSize(tt.bytes)
		if got != tt.want {
			t.Errorf("FormatSize(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}

// --- TruncateHead ---

func TestTruncateHead_NoTruncation(t *testing.T) {
	content := "line1\nline2\nline3"
	r := TruncateHead(content, TruncationOptions{})
	if r.Truncated {
		t.Error("expected no truncation")
	}
	if r.Content != content {
		t.Errorf("content mismatch")
	}
	if r.TotalLines != 3 {
		t.Errorf("TotalLines = %d, want 3", r.TotalLines)
	}
}

func TestTruncateHead_LineLimitHit(t *testing.T) {
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = "x"
	}
	content := strings.Join(lines, "\n")

	r := TruncateHead(content, TruncationOptions{MaxLines: 10})
	if !r.Truncated {
		t.Fatal("expected truncation")
	}
	if r.TruncatedBy != "lines" {
		t.Errorf("TruncatedBy = %q, want lines", r.TruncatedBy)
	}
	if r.OutputLines != 10 {
		t.Errorf("OutputLines = %d, want 10", r.OutputLines)
	}
	if r.TotalLines != 100 {
		t.Errorf("TotalLines = %d, want 100", r.TotalLines)
	}
}

func TestTruncateHead_ByteLimitHit(t *testing.T) {
	// 3 lines, each 20 bytes + newlines
	content := strings.Repeat("a", 20) + "\n" + strings.Repeat("b", 20) + "\n" + strings.Repeat("c", 20)

	r := TruncateHead(content, TruncationOptions{MaxBytes: 30})
	if !r.Truncated {
		t.Fatal("expected truncation")
	}
	if r.TruncatedBy != "bytes" {
		t.Errorf("TruncatedBy = %q, want bytes", r.TruncatedBy)
	}
	// First line is 20 bytes, second line needs +1 (newline) + 20 = 41 > 30
	if r.OutputLines != 1 {
		t.Errorf("OutputLines = %d, want 1", r.OutputLines)
	}
}

func TestTruncateHead_FirstLineExceedsLimit(t *testing.T) {
	content := strings.Repeat("x", 100) + "\nline2"

	r := TruncateHead(content, TruncationOptions{MaxBytes: 50})
	if !r.Truncated {
		t.Fatal("expected truncation")
	}
	if !r.FirstLineExceedsLimit {
		t.Error("expected FirstLineExceedsLimit=true")
	}
	if r.Content != "" {
		t.Errorf("Content = %q, want empty", r.Content)
	}
	if r.OutputLines != 0 {
		t.Errorf("OutputLines = %d, want 0", r.OutputLines)
	}
}

func TestTruncateHead_EmptyInput(t *testing.T) {
	r := TruncateHead("", TruncationOptions{})
	if r.Truncated {
		t.Error("expected no truncation for empty input")
	}
	if r.TotalLines != 1 { // strings.Split("", "\n") returns [""]
		t.Errorf("TotalLines = %d, want 1", r.TotalLines)
	}
}

// --- TruncateTail ---

func TestTruncateTail_NoTruncation(t *testing.T) {
	content := "line1\nline2\nline3"
	r := TruncateTail(content, TruncationOptions{})
	if r.Truncated {
		t.Error("expected no truncation")
	}
	if r.Content != content {
		t.Errorf("content mismatch")
	}
}

func TestTruncateTail_LineLimitHit(t *testing.T) {
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = "x"
	}
	content := strings.Join(lines, "\n")

	r := TruncateTail(content, TruncationOptions{MaxLines: 10})
	if !r.Truncated {
		t.Fatal("expected truncation")
	}
	if r.TruncatedBy != "lines" {
		t.Errorf("TruncatedBy = %q, want lines", r.TruncatedBy)
	}
	if r.OutputLines != 10 {
		t.Errorf("OutputLines = %d, want 10", r.OutputLines)
	}
	// Should keep the LAST 10 lines
	outLines := strings.Split(r.Content, "\n")
	if len(outLines) != 10 {
		t.Errorf("output line count = %d, want 10", len(outLines))
	}
}

func TestTruncateTail_ByteLimitHit(t *testing.T) {
	content := strings.Repeat("a", 20) + "\n" + strings.Repeat("b", 20) + "\n" + strings.Repeat("c", 20)

	r := TruncateTail(content, TruncationOptions{MaxBytes: 30})
	if !r.Truncated {
		t.Fatal("expected truncation")
	}
	if r.TruncatedBy != "bytes" {
		t.Errorf("TruncatedBy = %q, want bytes", r.TruncatedBy)
	}
	// Last line is 20 bytes, previous + newline = 41 > 30
	if r.OutputLines != 1 {
		t.Errorf("OutputLines = %d, want 1", r.OutputLines)
	}
	if r.Content != strings.Repeat("c", 20) {
		t.Errorf("Content = %q, want 'cccc...'", r.Content)
	}
}

func TestTruncateTail_LastLinePartial(t *testing.T) {
	// Single line exceeds byte limit — should take the tail portion
	content := strings.Repeat("x", 100)

	r := TruncateTail(content, TruncationOptions{MaxBytes: 30})
	if !r.Truncated {
		t.Fatal("expected truncation")
	}
	if !r.LastLinePartial {
		t.Error("expected LastLinePartial=true")
	}
	if len(r.Content) > 30 {
		t.Errorf("Content length = %d, want <= 30", len(r.Content))
	}
	// Should be the tail of the string
	if !strings.HasSuffix(strings.Repeat("x", 100), r.Content) {
		t.Errorf("Content should be suffix of original")
	}
}

func TestTruncateTail_EmptyInput(t *testing.T) {
	r := TruncateTail("", TruncationOptions{})
	if r.Truncated {
		t.Error("expected no truncation for empty input")
	}
}

// --- TruncateLine ---

func TestTruncateLine_Short(t *testing.T) {
	text, truncated := TruncateLine("hello world", 20)
	if truncated {
		t.Error("expected not truncated")
	}
	if text != "hello world" {
		t.Errorf("text = %q", text)
	}
}

func TestTruncateLine_Long(t *testing.T) {
	text, truncated := TruncateLine(strings.Repeat("x", 600), 0) // default GrepMaxLineLength=500
	if !truncated {
		t.Error("expected truncated")
	}
	if !strings.Contains(text, "[truncated]") {
		t.Error("expected [truncated] suffix")
	}
	// Should have 500 x's + suffix
	runes := []rune(text)
	if runes[499] != 'x' {
		t.Error("expected first 500 chars to be 'x'")
	}
}

func TestTruncateLine_ExactLimit(t *testing.T) {
	text, truncated := TruncateLine("12345", 5)
	if truncated {
		t.Error("expected not truncated at exact limit")
	}
	if text != "12345" {
		t.Errorf("text = %q", text)
	}
}

// --- UTF-8 handling ---

func TestTruncateStringToBytesFromEnd_Ascii(t *testing.T) {
	s := "hello world"
	got := truncateStringToBytesFromEnd(s, 5)
	if got != "world" {
		t.Errorf("got %q, want 'world'", got)
	}
}

func TestTruncateStringToBytesFromEnd_UTF8(t *testing.T) {
	// "héllo" = h(1) é(2) l(1) l(1) o(1) = 6 bytes
	s := "héllo"
	got := truncateStringToBytesFromEnd(s, 3)
	// Last 3 bytes = "llo"
	if got != "llo" {
		t.Errorf("got %q, want 'llo'", got)
	}
}

func TestTruncateStringToBytesFromEnd_UTF8Boundary(t *testing.T) {
	// "aé" = a(1) é(2) = 3 bytes. maxBytes=2 should skip the é and return just "é" or "a"
	// Actually: we start at byte index 3-2=1, but byte 1 is middle of é, so we advance to byte 3 → empty result... no.
	// Wait: "aé" bytes = [0x61, 0xC3, 0xA9]. start = 3-2 = 1. s[1]=0xC3 which IS a RuneStart. So result = "é"
	s := "aé"
	got := truncateStringToBytesFromEnd(s, 2)
	if got != "é" {
		t.Errorf("got %q, want 'é'", got)
	}
}

func TestTruncateStringToBytesFromEnd_FitsExactly(t *testing.T) {
	s := "abc"
	got := truncateStringToBytesFromEnd(s, 10)
	if got != "abc" {
		t.Errorf("got %q, want 'abc'", got)
	}
}
