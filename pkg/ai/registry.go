// Ported from: packages/ai/src/api-registry.ts
// Upstream hash: 1caadb2e
package ai

import (
	"fmt"
	"sync"
)

// ApiProvider represents a registered API provider with stream functions.
type ApiProvider struct {
	Api          Api
	Stream       StreamFunction
	StreamSimple SimpleStreamFunction
}

type registeredProvider struct {
	provider *ApiProvider
	sourceID string
}

// Registry manages registered API providers.
type Registry struct {
	mu        sync.RWMutex
	providers map[Api]*registeredProvider
}

// DefaultRegistry is the global provider registry.
var DefaultRegistry = NewRegistry()

// NewRegistry creates a new empty registry.
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[Api]*registeredProvider),
	}
}

// RegisterApiProvider registers a provider for the given API.
func (r *Registry) RegisterApiProvider(provider *ApiProvider, sourceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[provider.Api] = &registeredProvider{
		provider: provider,
		sourceID: sourceID,
	}
}

// GetApiProvider returns the provider for the given API, or nil.
func (r *Registry) GetApiProvider(api Api) *ApiProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry := r.providers[api]
	if entry == nil {
		return nil
	}
	return entry.provider
}

// GetApiProviders returns all registered providers.
func (r *Registry) GetApiProviders() []*ApiProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*ApiProvider, 0, len(r.providers))
	for _, entry := range r.providers {
		result = append(result, entry.provider)
	}
	return result
}

// UnregisterApiProviders removes all providers with the given sourceID.
func (r *Registry) UnregisterApiProviders(sourceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for api, entry := range r.providers {
		if entry.sourceID == sourceID {
			delete(r.providers, api)
		}
	}
}

// ClearApiProviders removes all registered providers.
func (r *Registry) ClearApiProviders() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers = make(map[Api]*registeredProvider)
}

// MustGetApiProvider returns the provider or panics if not registered.
func MustGetApiProvider(registry *Registry, api Api) *ApiProvider {
	p := registry.GetApiProvider(api)
	if p == nil {
		panic(fmt.Sprintf("no API provider registered for api: %s", api))
	}
	return p
}
