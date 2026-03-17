// Ported from: packages/ai/src/utils/oauth/index.ts
// Upstream hash: c99b9940
package oauth

import (
	"sort"
	"sync"
	"time"
)

var (
	registryMu sync.RWMutex
	registry   = map[string]Provider{}
)

// builtInProviders holds the original built-in providers for reset.
var builtInProviders = map[string]Provider{}

func init() {
	// Register all built-in OAuth providers.
	RegisterProvider(&AnthropicProvider{})
	RegisterProvider(&GitHubCopilotProvider{})
	// GeminiCLIProvider is now provided by the gemini_cli_auth builtin extension.
	RegisterProvider(&AntigravityProvider{})
	RegisterProvider(&OpenAICodexProvider{})

	// Snapshot built-ins after registration.
	registryMu.RLock()
	for k, v := range registry {
		builtInProviders[k] = v
	}
	registryMu.RUnlock()
}

// RegisterProvider registers a custom OAuth provider.
func RegisterProvider(p Provider) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[p.ID()] = p
}

// GetProvider returns the OAuth provider with the given ID, or nil.
func GetProvider(id string) Provider {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return registry[id]
}

// GetProviders returns all registered OAuth providers, sorted by ID for stable ordering.
func GetProviders() []Provider {
	registryMu.RLock()
	defer registryMu.RUnlock()
	result := make([]Provider, 0, len(registry))
	for _, p := range registry {
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].ID() < result[j].ID()
	})
	return result
}

// UnregisterProvider removes a custom OAuth provider.
// If the provider is built-in, restores the built-in implementation.
func UnregisterProvider(id string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if p, ok := builtInProviders[id]; ok {
		registry[id] = p
		return
	}
	delete(registry, id)
}

// ResetProviders resets the registry to built-in providers only.
func ResetProviders() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = make(map[string]Provider, len(builtInProviders))
	for k, v := range builtInProviders {
		registry[k] = v
	}
}

// GetProviderInfoList returns info for all registered OAuth providers.
func GetProviderInfoList() []ProviderInfo {
	providers := GetProviders()
	result := make([]ProviderInfo, len(providers))
	for i, p := range providers {
		result[i] = ProviderInfo{
			ID:        p.ID(),
			Name:      p.Name(),
			Available: true,
		}
	}
	return result
}

// GetOAuthAPIKey refreshes credentials if expired and returns the API key.
func GetOAuthAPIKey(providerID string, allCreds map[string]*Credentials) (apiKey string, newCreds *Credentials, err error) {
	p := GetProvider(providerID)
	if p == nil {
		return "", nil, nil
	}

	creds, ok := allCreds[providerID]
	if !ok || creds == nil {
		return "", nil, nil
	}

	// Refresh if expired
	if isExpired(creds) {
		newCreds, err := p.RefreshToken(creds)
		if err != nil {
			return "", nil, err
		}
		creds = newCreds
	}

	return p.GetAPIKey(creds), creds, nil
}

func isExpired(creds *Credentials) bool {
	// Expires is stored in milliseconds since epoch
	return creds.Expires > 0 && time.Now().UnixMilli() >= creds.Expires
}
