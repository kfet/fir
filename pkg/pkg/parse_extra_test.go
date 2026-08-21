package pkg

import (
	"strings"
	"testing"
)

// TestParseSourceWindowsAbsolute pins that a drive-letter path is treated as a
// local path and never as "host/path" — on a Windows host "C:/pkgs/x" would
// otherwise look like a bare git source.
func TestParseSourceWindowsAbsolute(t *testing.T) {
	for _, input := range []string{`C:\pkgs\thing`, "d:/pkgs/thing"} {
		src, err := ParseSource(input)
		if err != nil {
			t.Fatalf("ParseSource(%q): %v", input, err)
		}
		if src.Type != "local" {
			t.Errorf("ParseSource(%q).Type = %q, want local", input, src.Type)
		}
	}
	// Near-misses that must not be mistaken for drive letters.
	for _, input := range []string{"1:/x/y", "cc:/x/y", "c:x"} {
		if isWindowsAbsolute(input) {
			t.Errorf("isWindowsAbsolute(%q) = true, want false", input)
		}
	}
}

// TestParseSourceUnresolvableHome covers the home-expansion failure: with no
// home directory to expand "~" against, the error names the offending source
// rather than silently producing a path relative to the working directory.
func TestParseSourceUnresolvableHome(t *testing.T) {
	t.Setenv("HOME", "")

	for _, input := range []string{"~/pkgs/thing", "~"} {
		_, err := ParseSource(input)
		if err == nil {
			t.Fatalf("ParseSource(%q): expected an error with no HOME", input)
		}
		if !strings.Contains(err.Error(), "resolving local path") || !strings.Contains(err.Error(), input) {
			t.Errorf("ParseSource(%q) error = %v, want it to name the source", input, err)
		}
	}
}

// TestParseGitSchemeErrors pins the two rejections of the "git:" scheme.
// Both must fail loudly: falling through to a local path would try to install
// a directory literally named "git:...".
func TestParseGitSchemeErrors(t *testing.T) {
	tests := []struct {
		input   string
		wantErr string
	}{
		{"git:", "missing host/path"},
		{"git://", "missing host/path"},
		{"git:github.com", "missing path"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			_, err := ParseSource(tc.input)
			if err == nil {
				t.Fatalf("ParseSource(%q): expected an error", tc.input)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}
