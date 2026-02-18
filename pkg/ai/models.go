// Ported from: packages/ai/src/models.ts + models.generated.ts
// Upstream hash: 1caadb2e
package ai

import (
	"strings"
	"sync"
)

var (
	modelRegistryMu sync.RWMutex
	modelRegistry   = make(map[Provider]map[string]*Model) // provider -> modelID -> Model
)

// RegisterModel adds a model to the built-in registry.
// Called by init() in models_generated.go and can be called for custom models.
func RegisterModel(m *Model) {
	modelRegistryMu.Lock()
	defer modelRegistryMu.Unlock()
	if _, ok := modelRegistry[m.Provider]; !ok {
		modelRegistry[m.Provider] = make(map[string]*Model)
	}
	modelRegistry[m.Provider][m.ID] = m
}

// GetModel returns a model by provider and model ID, or nil if not found.
func GetModel(provider Provider, modelID string) *Model {
	modelRegistryMu.RLock()
	defer modelRegistryMu.RUnlock()
	providerModels := modelRegistry[provider]
	if providerModels == nil {
		return nil
	}
	return providerModels[modelID]
}

// GetProviders returns all known provider names.
func GetProviders() []Provider {
	modelRegistryMu.RLock()
	defer modelRegistryMu.RUnlock()
	providers := make([]Provider, 0, len(modelRegistry))
	for p := range modelRegistry {
		providers = append(providers, p)
	}
	return providers
}

// GetModels returns all models for a given provider.
func GetModels(provider Provider) []*Model {
	modelRegistryMu.RLock()
	defer modelRegistryMu.RUnlock()
	providerModels := modelRegistry[provider]
	if providerModels == nil {
		return nil
	}
	models := make([]*Model, 0, len(providerModels))
	for _, m := range providerModels {
		models = append(models, m)
	}
	return models
}

// CalculateCost calculates and sets the cost fields on usage based on model pricing.
// Returns the cost breakdown.
func CalculateCost(model *Model, usage *Usage) UsageCost {
	usage.Cost.Input = (model.Cost.Input / 1_000_000) * float64(usage.Input)
	usage.Cost.Output = (model.Cost.Output / 1_000_000) * float64(usage.Output)
	usage.Cost.CacheRead = (model.Cost.CacheRead / 1_000_000) * float64(usage.CacheRead)
	usage.Cost.CacheWrite = (model.Cost.CacheWrite / 1_000_000) * float64(usage.CacheWrite)
	usage.Cost.Total = usage.Cost.Input + usage.Cost.Output + usage.Cost.CacheRead + usage.Cost.CacheWrite
	return usage.Cost
}

// SupportsXhigh checks if a model supports the xhigh thinking level.
func SupportsXhigh(model *Model) bool {
	if model == nil {
		return false
	}
	if strings.Contains(model.ID, "gpt-5.2") || strings.Contains(model.ID, "gpt-5.3") {
		return true
	}
	if model.Api == ApiAnthropicMessages {
		if strings.Contains(model.ID, "opus-4-6") || strings.Contains(model.ID, "opus-4.6") {
			return true
		}
	}
	return false
}

// ModelsAreEqual checks if two models are equal by comparing ID and provider.
func ModelsAreEqual(a, b *Model) bool {
	if a == nil || b == nil {
		return false
	}
	return a.ID == b.ID && a.Provider == b.Provider
}

// boolRef returns a pointer to b. Used in model registration for Compat fields.
func boolRef(b bool) *bool { return &b }
