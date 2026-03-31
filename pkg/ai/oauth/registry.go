// Ported from: packages/ai/src/utils/oauth/index.ts
// Upstream hash: c99b9940
package oauth

import (
	"sort"
	"sync"
	"time"
)

var (
	// registry stores Provider values keyed by provider ID.
	registry sync.Map // string → Provider

	// builtInProviders stores the original built-in providers for reset.
	builtInProviders sync.Map // string → Provider
)

// RegisterProvider registers a custom OAuth provider.
func RegisterProvider(p Provider) {
	registry.Store(p.ID(), p)
}

// GetProvider returns the OAuth provider with the given ID, or nil.
func GetProvider(id string) Provider {
	v, ok := registry.Load(id)
	if !ok {
		return nil
	}
	return v.(Provider)
}

// GetProviders returns all registered OAuth providers, sorted by ID for stable ordering.
func GetProviders() []Provider {
	var result []Provider
	registry.Range(func(_, v any) bool {
		result = append(result, v.(Provider))
		return true
	})
	sort.Slice(result, func(i, j int) bool {
		return result[i].ID() < result[j].ID()
	})
	return result
}

// UnregisterProvider removes a custom OAuth provider.
// If the provider is built-in, restores the built-in implementation.
func UnregisterProvider(id string) {
	if p, ok := builtInProviders.Load(id); ok {
		registry.Store(id, p)
	} else {
		registry.Delete(id)
	}
}

// ResetProviders resets the registry to built-in providers only.
func ResetProviders() {
	// Delete all entries.
	registry.Range(func(k, _ any) bool {
		registry.Delete(k)
		return true
	})
	// Restore built-ins.
	builtInProviders.Range(func(k, v any) bool {
		registry.Store(k, v)
		return true
	})
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
