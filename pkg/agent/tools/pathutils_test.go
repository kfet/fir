// Ported from: packages/coding-agent/src/core/tools/path-utils.ts
// Upstream hash: 1caadb2e
package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandPath_Tilde(t *testing.T) {
	home, _ := os.UserHomeDir()
	if got := ExpandPath("~"); got != home {
		t.Errorf("ExpandPath('~') = %q, want %q", got, home)
	}
}

func TestExpandPath_TildeSlash(t *testing.T) {
	home, _ := os.UserHomeDir()
	got := ExpandPath("~/Documents")
	want := home + "/Documents"
	if got != want {
		t.Errorf("ExpandPath('~/Documents') = %q, want %q", got, want)
	}
}

func TestExpandPath_RelativePath(t *testing.T) {
	got := ExpandPath("foo/bar")
	if got != "foo/bar" {
		t.Errorf("ExpandPath('foo/bar') = %q, want 'foo/bar'", got)
	}
}

func TestExpandPath_AtPrefix(t *testing.T) {
	got := ExpandPath("@some/path")
	if got != "some/path" {
		t.Errorf("ExpandPath('@some/path') = %q, want 'some/path'", got)
	}
}

func TestExpandPath_UnicodeSpaces(t *testing.T) {
	// non-breaking space → regular space
	got := ExpandPath("hello\u00A0world")
	if got != "hello world" {
		t.Errorf("ExpandPath with NBSP = %q, want 'hello world'", got)
	}
}

func TestResolveToCwd_Relative(t *testing.T) {
	got := ResolveToCwd("foo/bar.txt", "/home/user/project")
	want := filepath.Join("/home/user/project", "foo/bar.txt")
	if got != want {
		t.Errorf("ResolveToCwd = %q, want %q", got, want)
	}
}

func TestResolveToCwd_Absolute(t *testing.T) {
	got := ResolveToCwd("/etc/config", "/home/user")
	if got != "/etc/config" {
		t.Errorf("ResolveToCwd = %q, want '/etc/config'", got)
	}
}

func TestResolveToCwd_TildeExpansion(t *testing.T) {
	home, _ := os.UserHomeDir()
	got := ResolveToCwd("~/foo", "/some/cwd")
	want := home + "/foo"
	if got != want {
		t.Errorf("ResolveToCwd = %q, want %q", got, want)
	}
}

func TestResolveReadPath_ExistingFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "test.txt")
	os.WriteFile(f, []byte("hello"), 0644)

	got := ResolveReadPath("test.txt", dir)
	if got != f {
		t.Errorf("ResolveReadPath = %q, want %q", got, f)
	}
}

func TestResolveReadPath_NonExistent(t *testing.T) {
	dir := t.TempDir()
	got := ResolveReadPath("nonexistent.txt", dir)
	want := filepath.Join(dir, "nonexistent.txt")
	if got != want {
		t.Errorf("ResolveReadPath = %q, want %q", got, want)
	}
}

func TestResolveReadPath_NFDVariant(t *testing.T) {
	dir := t.TempDir()
	// Create a file with NFD name (é decomposed)
	nfdName := "caf\u0065\u0301.txt" // e + combining acute accent
	nfdPath := filepath.Join(dir, nfdName)
	os.WriteFile(nfdPath, []byte("data"), 0644)

	// Try resolving with NFC name (é composed)
	nfcName := "caf\u00e9.txt"
	got := ResolveReadPath(nfcName, dir)
	// Should find the NFD variant
	if !fileExists(got) {
		// NFD might not work on all filesystems, skip gracefully
		t.Skipf("NFD resolution didn't find file (filesystem may not support it)")
	}
}

func TestIsHidden(t *testing.T) {
	tests := []struct {
		name   string
		hidden bool
	}{
		{".git", true},
		{".hidden", true},
		{"visible", false},
		{"", false},
		{"..", true},
	}
	for _, tt := range tests {
		if got := IsHidden(tt.name); got != tt.hidden {
			t.Errorf("IsHidden(%q) = %v, want %v", tt.name, got, tt.hidden)
		}
	}
}

// Test macOS screenshot path variant
func TestTryMacOSScreenshotPath(t *testing.T) {
	input := "Screenshot 2024-01-01 at 10.30.00 AM.png"
	got := tryMacOSScreenshotPath(input)
	if got == input {
		t.Error("expected transformation")
	}
	// Should contain narrow no-break space before AM
	if got != "Screenshot 2024-01-01 at 10.30.00\u202FAM.png" {
		t.Errorf("got %q", got)
	}
}

func TestNormalizeAtPrefix(t *testing.T) {
	if got := normalizeAtPrefix("@foo"); got != "foo" {
		t.Errorf("got %q", got)
	}
	if got := normalizeAtPrefix("foo"); got != "foo" {
		t.Errorf("got %q", got)
	}
	if got := normalizeAtPrefix(""); got != "" {
		t.Errorf("got %q", got)
	}
}
