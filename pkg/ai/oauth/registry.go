// Ported from: packages/ai/src/utils/oauth/index.ts
// Upstream hash: 1caadb2e
package oauth

import (
	"sync"
	"time"
)

var (
	registryMu sync.RWMutex
	registry   = map[string]Provider{}
	builtins   []Provider
)

func init() {
	// Register all built-in OAuth providers.
	builtins = []Provider{
		&AnthropicProvider{},
		&GitHubCopilotProvider{},
		&GeminiCLIProvider{},
		&AntigravityProvider{},
		&OpenAICodexProvider{},
	}
	for _, p := range builtins {
		registry[p.ID()] = p
	}
}

// RegisterProvider registers a custom OAuth provider.
func RegisterProvider(p Provider) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[p.ID()] = p
}

// UnregisterProvider removes a provider. Built-in providers are restored
// to their default implementation rather than deleted.
func UnregisterProvider(id string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	for _, b := range builtins {
		if b.ID() == id {
			registry[id] = b
			return
		}
	}
	delete(registry, id)
}

// ResetProviders restores the registry to only the built-in providers.
func ResetProviders() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = make(map[string]Provider, len(builtins))
	for _, p := range builtins {
		registry[p.ID()] = p
	}
}

// GetProvider returns the OAuth provider with the given ID, or nil.
func GetProvider(id string) Provider {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return registry[id]
}

// GetProviders returns all registered OAuth providers.
func GetProviders() []Provider {
	registryMu.RLock()
	defer registryMu.RUnlock()
	result := make([]Provider, 0, len(registry))
	for _, p := range registry {
		result = append(result, p)
	}
	return result
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
