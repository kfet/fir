// Ported from: packages/ai/src/env-api-keys.ts
package envkeys

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

// clearVertexEnv removes every variable the vertex and ADC paths consult, so
// each test states its own preconditions in full. t.Setenv restores the
// developer's real environment afterwards.
func clearVertexEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"GOOGLE_CLOUD_API_KEY",
		"GOOGLE_APPLICATION_CREDENTIALS",
		"GOOGLE_CLOUD_PROJECT",
		"GCLOUD_PROJECT",
		"GOOGLE_CLOUD_LOCATION",
	} {
		t.Setenv(k, "")
	}
}

// TestKnownApiKeyEnvVars pins the contract the helper exists for: a sorted,
// deduplicated list of every variable the lookup can read, with no empty
// entry. Tests use it to build a hermetic environment, so a missing name
// means a test that silently reads the developer's real credentials.
func TestKnownApiKeyEnvVars(t *testing.T) {
	got := KnownApiKeyEnvVars()
	if len(got) == 0 {
		t.Fatal("KnownApiKeyEnvVars returned nothing")
	}

	if !sort.StringsAreSorted(got) {
		t.Errorf("result is not sorted: %v", got)
	}
	seen := map[string]bool{}
	for _, k := range got {
		if k == "" {
			t.Error("result contains an empty variable name")
		}
		if seen[k] {
			t.Errorf("duplicate entry %q", k)
		}
		seen[k] = true
	}

	// Every primary and fallback declared in the registry must be present —
	// this is the property, not a hardcoded list.
	for _, p := range ai.GetRegisteredProviders() {
		if p.EnvKeys.Primary != "" && !seen[p.EnvKeys.Primary] {
			t.Errorf("provider %s primary %q missing from the result", p.ID, p.EnvKeys.Primary)
		}
		for _, fb := range p.EnvKeys.Fallbacks {
			if !seen[fb] {
				t.Errorf("provider %s fallback %q missing from the result", p.ID, fb)
			}
		}
	}

	// Spot-check both kinds of entry so the property test above cannot pass
	// vacuously against an empty registry.
	if !seen["OPENAI_API_KEY"] {
		t.Error("OPENAI_API_KEY (a primary) missing")
	}
	if !seen["GH_TOKEN"] {
		t.Error("GH_TOKEN (a fallback) missing")
	}
}

// TestAnthropicOAuthTokenWins pins the precedence that makes `fir auth login`
// work while a stale ANTHROPIC_API_KEY is still exported: the OAuth token is
// preferred, and the static key is used only when it is absent.
func TestAnthropicOAuthTokenWins(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-static")
	t.Setenv("ANTHROPIC_OAUTH_TOKEN", "oauth-token")
	if got := GetEnvApiKey("anthropic"); got != "oauth-token" {
		t.Errorf("GetEnvApiKey = %q, want the OAuth token", got)
	}

	t.Setenv("ANTHROPIC_OAUTH_TOKEN", "")
	if got := GetEnvApiKey("anthropic"); got != "sk-ant-static" {
		t.Errorf("with no OAuth token, GetEnvApiKey = %q, want the API key", got)
	}

	t.Setenv("ANTHROPIC_API_KEY", "")
	if got := GetEnvApiKey("anthropic"); got != "" {
		t.Errorf("with neither set, GetEnvApiKey = %q, want empty", got)
	}
}

// TestVertexApiKeyBeatsADC pins that an explicit key short-circuits the ADC
// probe entirely — including the filesystem check, which must not run.
func TestVertexApiKeyBeatsADC(t *testing.T) {
	clearVertexEnv(t)
	t.Setenv("GOOGLE_CLOUD_API_KEY", "vertex-key")
	// Deliberately hostile ADC state: a credentials path that does not exist
	// and no project/location. The explicit key must still win.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "nope.json"))

	if got := GetEnvApiKey("google-vertex"); got != "vertex-key" {
		t.Errorf("GetEnvApiKey = %q, want the explicit key", got)
	}
}

// TestVertexADC pins the three-part condition for reporting Vertex as
// authenticated: credentials, a project (from either variable), and a
// location. Each missing part must fail closed — a false positive here shows
// up as a confusing API error at request time instead of a login prompt.
func TestVertexADC(t *testing.T) {
	creds := filepath.Join(t.TempDir(), "adc.json")
	if err := os.WriteFile(creds, []byte(`{"type":"authorized_user"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		env      map[string]string
		want     string
		wantAuth bool
	}{
		{
			name: "credentials, project and location",
			env: map[string]string{
				"GOOGLE_APPLICATION_CREDENTIALS": creds,
				"GOOGLE_CLOUD_PROJECT":           "proj",
				"GOOGLE_CLOUD_LOCATION":          "us-central1",
			},
			want: "<authenticated>",
		},
		{
			name: "GCLOUD_PROJECT is an accepted spelling",
			env: map[string]string{
				"GOOGLE_APPLICATION_CREDENTIALS": creds,
				"GCLOUD_PROJECT":                 "proj",
				"GOOGLE_CLOUD_LOCATION":          "us-central1",
			},
			want: "<authenticated>",
		},
		{
			name: "no location",
			env: map[string]string{
				"GOOGLE_APPLICATION_CREDENTIALS": creds,
				"GOOGLE_CLOUD_PROJECT":           "proj",
			},
			want: "",
		},
		{
			name: "no project",
			env: map[string]string{
				"GOOGLE_APPLICATION_CREDENTIALS": creds,
				"GOOGLE_CLOUD_LOCATION":          "us-central1",
			},
			want: "",
		},
		{
			name: "credentials path does not exist",
			env: map[string]string{
				"GOOGLE_APPLICATION_CREDENTIALS": filepath.Join(t.TempDir(), "missing.json"),
				"GOOGLE_CLOUD_PROJECT":           "proj",
				"GOOGLE_CLOUD_LOCATION":          "us-central1",
			},
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearVertexEnv(t)
			// An empty HOME removes the well-known ADC location from play,
			// so each case tests only what it declares.
			t.Setenv("HOME", "")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if got := GetEnvApiKey("google-vertex"); got != tc.want {
				t.Errorf("GetEnvApiKey = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestVertexADCWellKnownLocation pins the fallback probe: with no explicit
// GOOGLE_APPLICATION_CREDENTIALS, `gcloud auth application-default login`'s
// file under the home directory counts as credentials.
func TestVertexADCWellKnownLocation(t *testing.T) {
	clearVertexEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GOOGLE_CLOUD_PROJECT", "proj")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "us-central1")

	if got := GetEnvApiKey("google-vertex"); got != "" {
		t.Fatalf("with no ADC file, GetEnvApiKey = %q, want empty", got)
	}

	adc := filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
	if err := os.MkdirAll(filepath.Dir(adc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(adc, []byte(`{"type":"authorized_user"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := GetEnvApiKey("google-vertex"); got != "<authenticated>" {
		t.Errorf("with the well-known ADC file present, GetEnvApiKey = %q", got)
	}
}

// TestVertexADCNoHomeDirectory pins that an unresolvable home directory is
// reported as "no credentials" rather than panicking or being treated as
// authenticated.
func TestVertexADCNoHomeDirectory(t *testing.T) {
	clearVertexEnv(t)
	t.Setenv("HOME", "")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "proj")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "us-central1")

	if got := GetEnvApiKey("google-vertex"); got != "" {
		t.Errorf("GetEnvApiKey = %q, want empty with no resolvable home", got)
	}
}

// TestRegistryProviderWithNoKeySet pins the tail of the registry-driven
// lookup: a known provider whose primary and every fallback are unset yields
// "" rather than a stray value from another provider's variable.
func TestRegistryProviderWithNoKeySet(t *testing.T) {
	rec := ai.GetProviderRecord(ai.Provider("github-copilot"))
	if rec == nil {
		t.Fatal("github-copilot should be a registered provider")
	}
	if rec.EnvKeys.Primary == "" || len(rec.EnvKeys.Fallbacks) == 0 {
		t.Fatalf("test assumes a provider with both a primary and fallbacks, got %+v", rec.EnvKeys)
	}

	t.Setenv(rec.EnvKeys.Primary, "")
	for _, fb := range rec.EnvKeys.Fallbacks {
		t.Setenv(fb, "")
	}
	// A different provider's key must not leak in.
	t.Setenv("OPENAI_API_KEY", "sk-openai")

	if got := GetEnvApiKey("github-copilot"); got != "" {
		t.Errorf("GetEnvApiKey = %q, want empty", got)
	}
}

// TestFallbackOrderIsDeclarationOrder pins that fallbacks are consulted in the
// order the provider record declares them, and only after the primary.
func TestFallbackOrderIsDeclarationOrder(t *testing.T) {
	rec := ai.GetProviderRecord(ai.Provider("github-copilot"))
	if rec == nil {
		t.Fatal("github-copilot should be a registered provider")
	}
	if len(rec.EnvKeys.Fallbacks) < 2 {
		t.Fatalf("github-copilot should declare GH_TOKEN and GITHUB_TOKEN as fallbacks, got %v", rec.EnvKeys.Fallbacks)
	}

	t.Setenv(rec.EnvKeys.Primary, "")
	for i, fb := range rec.EnvKeys.Fallbacks {
		t.Setenv(fb, "fallback-"+string(rune('a'+i)))
	}
	if got, want := GetEnvApiKey("github-copilot"), "fallback-a"; got != want {
		t.Errorf("GetEnvApiKey = %q, want the first declared fallback %q", got, want)
	}

	t.Setenv(rec.EnvKeys.Primary, "primary-key")
	if got := GetEnvApiKey("github-copilot"); got != "primary-key" {
		t.Errorf("GetEnvApiKey = %q, want the primary to win", got)
	}
}
