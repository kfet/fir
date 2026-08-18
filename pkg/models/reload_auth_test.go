package models

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
)

// TestRefresh_PicksUpProviderAuthenticatedAfterStartup covers the /reload case:
// a provider gains credentials in the shared auth storage *after* the session
// started (fir login, a sibling session, another tool). Refresh must re-read
// auth.json so the provider's models become available without a restart.
func TestRefresh_PicksUpProviderAuthenticatedAfterStartup(t *testing.T) {
	const provider = "test-provider-lateauth"
	const modelID = "test-lateauth-model"

	ai.RegisterModel(&ai.Model{
		ID:            modelID,
		Name:          "Late Auth Model",
		API:           ai.ApiAnthropicMessages,
		Provider:      provider,
		BaseURL:       "https://api.test.com",
		Input:         []ai.InputModality{ai.InputText},
		ContextWindow: 200000,
		MaxTokens:     4096,
	})

	tmpDir := t.TempDir()
	authPath := filepath.Join(tmpDir, "auth.json")

	// Session starts with no credentials at all.
	authStorage := auth.NewAuthStorage(authPath)
	registry := NewModelRegistry(authStorage, "")

	if hasModel(registry.GetAvailable(), modelID) {
		t.Fatalf("model %q should not be available before authentication", modelID)
	}

	// Another process writes credentials for the provider.
	writeAuthFile(t, authPath, `{"`+provider+`":{"type":"api_key","key":"sk-test-late"}}`)

	// Without a reload the running session still sees the startup state.
	if hasModel(registry.GetAvailable(), modelID) {
		t.Fatalf("model %q became available without a refresh — test no longer exercises the cache", modelID)
	}

	// /reload -> ModelRegistry.Refresh().
	registry.Refresh()

	if !hasModel(registry.GetAvailable(), modelID) {
		t.Errorf("model %q not available after Refresh; provider auth was not re-read from disk", modelID)
	}
	if !authStorage.HasAuth(provider) {
		t.Errorf("HasAuth(%q) = false after Refresh, want true", provider)
	}
}

// TestRefresh_DropsProviderDeauthenticatedAfterStartup is the mirror case: a
// credential removed externally must disappear on reload too.
func TestRefresh_DropsProviderDeauthenticatedAfterStartup(t *testing.T) {
	const provider = "test-provider-lateunauth"
	const modelID = "test-lateunauth-model"

	ai.RegisterModel(&ai.Model{
		ID:            modelID,
		Name:          "Late Unauth Model",
		API:           ai.ApiAnthropicMessages,
		Provider:      provider,
		BaseURL:       "https://api.test.com",
		Input:         []ai.InputModality{ai.InputText},
		ContextWindow: 200000,
		MaxTokens:     4096,
	})

	tmpDir := t.TempDir()
	authPath := filepath.Join(tmpDir, "auth.json")
	writeAuthFile(t, authPath, `{"`+provider+`":{"type":"api_key","key":"sk-test-going"}}`)

	authStorage := auth.NewAuthStorage(authPath)
	registry := NewModelRegistry(authStorage, "")

	if !hasModel(registry.GetAvailable(), modelID) {
		t.Fatalf("model %q should be available at startup", modelID)
	}

	writeAuthFile(t, authPath, `{}`)
	registry.Refresh()

	if hasModel(registry.GetAvailable(), modelID) {
		t.Errorf("model %q still available after credentials were removed and Refresh ran", modelID)
	}
}

// TestRefresh_PreservesRuntimeApiKeys guards the safety requirement: re-reading
// auth.json must not clobber keys injected at runtime (e.g. --api-key), which
// are held outside the on-disk map.
func TestRefresh_PreservesRuntimeApiKeys(t *testing.T) {
	const provider = "test-provider-runtimekey"

	tmpDir := t.TempDir()
	authPath := filepath.Join(tmpDir, "auth.json")
	authStorage := auth.NewAuthStorage(authPath)
	registry := NewModelRegistry(authStorage, "")

	authStorage.SetRuntimeApiKey(provider, "sk-runtime-only")
	if !authStorage.HasAuth(provider) {
		t.Fatalf("runtime API key not visible before Refresh")
	}

	registry.Refresh()

	if !authStorage.HasAuth(provider) {
		t.Errorf("runtime API key lost after Refresh re-read auth.json")
	}
}

func writeAuthFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
}

func hasModel(models []*ai.Model, id string) bool {
	for _, m := range models {
		if m.ID == id {
			return true
		}
	}
	return false
}
