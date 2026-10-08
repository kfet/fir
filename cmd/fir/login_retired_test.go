package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/auth"
)

// A stored credential for a retired provider (google-gemini-cli) must be
// listed without error and flagged so the user knows to remove it.
func TestPrintStoredAccounts_FlagsRetiredProvider(t *testing.T) {
	st := tempAuthStorage(t, auth.AuthStorageData{
		"google-gemini-cli": staleOAuth("old"),
		"anthropic":         staleOAuth("a"),
	})
	r, w, _ := os.Pipe()
	orig := os.Stdout
	os.Stdout = w
	printStoredAccounts(st)
	os.Stdout = orig
	w.Close()
	b, _ := io.ReadAll(r)
	out := string(b)
	if !strings.Contains(out, "google-gemini-cli") || !strings.Contains(out, "fir logout google-gemini-cli") {
		t.Fatalf("retired provider not flagged:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "anthropic") && strings.Contains(line, "retired") {
			t.Fatalf("live provider wrongly flagged: %s", line)
		}
	}
}

func TestRunLogin_RetiredProviderExplains(t *testing.T) {
	err := runLogin("google-gemini-cli", auth.LoginOptions{})
	if err == nil || !strings.Contains(err.Error(), "google-antigravity") {
		t.Fatalf("want retired hint, got %v", err)
	}
}
