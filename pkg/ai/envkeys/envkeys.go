// Ported from: packages/ai/src/env-api-keys.ts
// Upstream hash: 036bde0a
//
// API-key sourcing from environment variables.  Most providers use a single
// env var, declared in their RegisteredProvider record (pkg/ai).  A handful
// have bespoke detection logic (multi-var combinations, filesystem checks)
// that can't be expressed as data — those keep their inline switch arms
// below.
package envkeys

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/kfet/fir/pkg/ai"
)

// KnownApiKeyEnvVars returns the sorted list of all environment variable
// names that GetEnvApiKey (or HasAuth) inspects to determine provider
// authentication.  Useful for tests that need a hermetic environment.
func KnownApiKeyEnvVars() []string {
	seen := map[string]struct{}{}
	for _, p := range ai.GetRegisteredProviders() {
		if p.EnvKeys.Primary != "" {
			seen[p.EnvKeys.Primary] = struct{}{}
		}
		for _, fb := range p.EnvKeys.Fallbacks {
			seen[fb] = struct{}{}
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ProviderEnvVar returns the primary API key environment variable name for a
// provider.  Returns "" if the provider has no simple env var mapping (e.g.
// bedrock, vertex — both Authenticated).
func ProviderEnvVar(provider string) string {
	p := ai.GetProviderRecord(ai.Provider(provider))
	if p == nil {
		return ""
	}
	return p.EnvKeys.Primary
}

// GetEnvApiKey returns the API key for a provider from known environment
// variables.  Returns "" if no key is found.  Bespoke detection logic for
// providers with non-trivial auth shapes lives inline below; everything else
// is a registry-driven Primary→Fallbacks lookup.
func GetEnvApiKey(provider string) string {
	switch provider {
	case string(ai.ProviderAnthropic):
		// OAuth-token fallback wins over the static API key.
		if v := os.Getenv("ANTHROPIC_OAUTH_TOKEN"); v != "" {
			return v
		}
		return os.Getenv("ANTHROPIC_API_KEY")

	case string(ai.ProviderGoogleVertex):
		// Explicit API key takes precedence over ADC.
		if v := os.Getenv("GOOGLE_CLOUD_API_KEY"); v != "" {
			return v
		}
		if hasVertexADCCredentials() &&
			(os.Getenv("GOOGLE_CLOUD_PROJECT") != "" || os.Getenv("GCLOUD_PROJECT") != "") &&
			os.Getenv("GOOGLE_CLOUD_LOCATION") != "" {
			return "<authenticated>"
		}
		return ""

	case string(ai.ProviderAmazonBedrock):
		if os.Getenv("AWS_PROFILE") != "" ||
			(os.Getenv("AWS_ACCESS_KEY_ID") != "" && os.Getenv("AWS_SECRET_ACCESS_KEY") != "") ||
			os.Getenv("AWS_BEARER_TOKEN_BEDROCK") != "" ||
			os.Getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI") != "" ||
			os.Getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI") != "" ||
			os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE") != "" {
			return "<authenticated>"
		}
		return ""
	}

	p := ai.GetProviderRecord(ai.Provider(provider))
	if p == nil {
		return ""
	}
	if p.EnvKeys.Primary != "" {
		if v := os.Getenv(p.EnvKeys.Primary); v != "" {
			return v
		}
	}
	for _, fb := range p.EnvKeys.Fallbacks {
		if v := os.Getenv(fb); v != "" {
			return v
		}
	}
	return ""
}

// hasVertexADCCredentials checks if Google Application Default Credentials exist.
func hasVertexADCCredentials() bool {
	gacPath := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	if gacPath != "" {
		_, err := os.Stat(gacPath)
		return err == nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	adcPath := filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
	_, err = os.Stat(adcPath)
	return err == nil
}
