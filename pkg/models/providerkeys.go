package models

import "sync"

// --- Extension-contributed literal provider API keys ---
//
// An extension that registers a hosted provider at the init handshake may
// ship a literal API key alongside it (ProviderSpec.ApiKey). This is the
// pi-mono `apiKey` semantic: the value IS the credential, not the name of
// an environment variable holding it. It exists for providers that are
// unauthenticated-but-not-keyless — a local llama.cpp server accepts any
// token and the pi ecosystem conventionally sends "no-key" — and for
// extensions that mint a key from their own configuration.
//
// The key is deliberately kept OUT of ModelRegistry.customProviderApiKeys:
// that map is rebuilt from the models.json snapshot on every Refresh(), so
// an extension registration stored there would be silently dropped. It is
// consulted last, after models.json, so explicit user configuration always
// outranks an extension-shipped default.

var providerAPIKeys sync.Map // string (provider id) → string (literal key)

// RegisterProviderAPIKey records a literal API key contributed by an
// extension for one of its hosted providers. Last write wins. An empty
// provider or key is ignored.
func RegisterProviderAPIKey(provider, key string) {
	if provider == "" || key == "" {
		return
	}
	providerAPIKeys.Store(provider, key)
}

// UnregisterProviderAPIKey drops an extension-contributed literal API key.
func UnregisterProviderAPIKey(provider string) {
	providerAPIKeys.Delete(provider)
}

// GetProviderAPIKey returns the literal API key an extension contributed
// for the provider, or "" when none was registered.
func GetProviderAPIKey(provider string) string {
	v, ok := providerAPIKeys.Load(provider)
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}
