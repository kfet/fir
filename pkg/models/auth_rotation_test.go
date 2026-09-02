package models

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kfet/fir/pkg/auth"
)

// TestRefreshApiKeyForProvider_PicksUpExternalRotation covers the wiring the
// agent loop depends on after an HTTP 401: the registry must resolve the
// credential as it is on DISK, not as this process cached it. `fir auth
// refresh` (or another fir session) rotates the credential out from under a
// live session, and Anthropic revokes the previous access token immediately —
// so a cached-value answer here is a permanently wedged session.
func TestRefreshApiKeyForProvider_PicksUpExternalRotation(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")

	write := func(key string) {
		t.Helper()
		b, err := json.MarshalIndent(auth.AuthStorageData{
			"rotating": {Type: auth.CredentialTypeAPIKey, Key: key},
		}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(authPath, b, 0600); err != nil {
			t.Fatal(err)
		}
	}

	write("key-before")
	registry := NewModelRegistry(auth.NewAuthStorage(authPath), "")

	if got := registry.GetApiKeyForProvider("rotating"); got != "key-before" {
		t.Fatalf("precondition: key = %q, want key-before", got)
	}

	write("key-after")
	if got := registry.RefreshApiKeyForProvider("rotating"); got != "key-after" {
		t.Errorf("RefreshApiKeyForProvider = %q, want key-after — the post-401 "+
			"path must re-read auth.json from disk", got)
	}
}
