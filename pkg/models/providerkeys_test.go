package models

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
)

// TestProviderAPIKeyResolution pins the resolution order for a literal API
// key contributed by an extension (ProviderSpec.api_key): it is the LAST
// resort, so a stored credential, the environment variable and a models.json
// stanza all outrank it, and it disappears when the extension unregisters.
//
// This is what lets a keyless-in-practice provider (a local llama.cpp server
// taking "no-key") authenticate with no env var and no models.json, while
// still letting a user override the shipped value by any of the usual means.
func TestProviderAPIKeyResolution(t *testing.T) {
	t.Setenv("FIR_NO_CATALOG_OVERLAY", "1")
	dir := t.TempDir()

	// Provider record so the env-var stage has an env key to read. This is
	// the shape pi_compat produces for an apiKey that looks like an env-var
	// name: both readings registered, precedence decides.
	ai.RegisterProvider(&ai.RegisteredProvider{
		ID:      "lit-prov",
		EnvKeys: ai.EnvKeySpec{Primary: "LIT_PROV_KEY"},
		Source:  "ext:test",
	})
	t.Cleanup(func() { ai.UnregisterProvider("lit-prov") })

	modelsJSON := filepath.Join(dir, "models.json")
	storage := auth.NewAuthStorage(filepath.Join(dir, "auth.json"))
	r := NewModelRegistry(storage, modelsJSON)

	// Nothing registered anywhere yet.
	if got := r.GetApiKeyForProvider("lit-prov"); got != "" {
		t.Fatalf("unexpected key before any registration: %q", got)
	}

	// Extension ships a literal key → used as the last resort.
	RegisterProviderAPIKey("lit-prov", "no-key")
	t.Cleanup(func() { UnregisterProviderAPIKey("lit-prov") })

	if got := r.GetApiKeyForProvider("lit-prov"); got != "no-key" {
		t.Errorf("extension literal not resolved: got %q, want %q", got, "no-key")
	}
	if !storage.HasAuth("lit-prov") {
		t.Error("HasAuth should be true once an extension ships a literal key")
	}

	// The environment variable outranks it.
	t.Setenv("LIT_PROV_KEY", "from-env")
	if got := r.GetApiKeyForProvider("lit-prov"); got != "from-env" {
		t.Errorf("env var should outrank the extension literal: got %q", got)
	}

	// A stored credential outranks the environment.
	if err := storage.Set("lit-prov", auth.AuthCredential{
		Type: auth.CredentialTypeAPIKey, Key: "from-auth-json",
	}); err != nil {
		t.Fatal(err)
	}
	if got := r.GetApiKeyForProvider("lit-prov"); got != "from-auth-json" {
		t.Errorf("auth.json should outrank the env var: got %q", got)
	}

	// Withdrawing the extension's key removes exactly that stage.
	if err := storage.Remove("lit-prov"); err != nil {
		t.Fatal(err)
	}
	os.Unsetenv("LIT_PROV_KEY")
	UnregisterProviderAPIKey("lit-prov")
	if got := r.GetApiKeyForProvider("lit-prov"); got != "" {
		t.Errorf("key survived unregister: %q", got)
	}
}

// TestProviderAPIKeyModelsJSONWins covers the one ordering that is easy to
// get backwards: an explicit models.json stanza must beat the key an
// extension ships, so a user can always redirect a provider's credential.
func TestProviderAPIKeyModelsJSONWins(t *testing.T) {
	t.Setenv("FIR_NO_CATALOG_OVERLAY", "1")
	dir := t.TempDir()

	modelsJSON := filepath.Join(dir, "models.json")
	if err := os.WriteFile(modelsJSON, []byte(`{
	  "providers": {
	    "mj-prov": {
	      "apiKey": "from-models-json",
	      "baseUrl": "http://127.0.0.1:8080/v1",
	      "api": "openai-completions",
	      "models": [{"id": "m1"}]
	    }
	  }
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	storage := auth.NewAuthStorage(filepath.Join(dir, "auth.json"))
	r := NewModelRegistry(storage, modelsJSON)

	RegisterProviderAPIKey("mj-prov", "no-key")
	t.Cleanup(func() { UnregisterProviderAPIKey("mj-prov") })

	if got := r.GetApiKeyForProvider("mj-prov"); got != "from-models-json" {
		t.Errorf("models.json should outrank the extension literal: got %q", got)
	}
}

// TestProviderAPIKeyRegistryGuards covers the trivial rejections: an empty
// provider id or key is a no-op rather than a poisoned entry.
func TestProviderAPIKeyRegistryGuards(t *testing.T) {
	RegisterProviderAPIKey("", "k")
	RegisterProviderAPIKey("guard-prov", "")
	if got := GetProviderAPIKey("guard-prov"); got != "" {
		t.Errorf("empty key stored: %q", got)
	}
	if got := GetProviderAPIKey(""); got != "" {
		t.Errorf("empty provider stored: %q", got)
	}
}
