// Ported from: packages/ai/src/utils/oauth/index.ts
// Upstream hash: 1caadb2e
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

func init() {
	// Register all built-in OAuth providers.
	RegisterProvider(&AnthropicProvider{})
	RegisterProvider(&GitHubCopilotProvider{})
	RegisterProvider(&GeminiCLIProvider{})
	RegisterProvider(&AntigravityProvider{})
	RegisterProvider(&OpenAICodexProvider{})
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
