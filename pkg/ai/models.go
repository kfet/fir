// Ported from: packages/ai/src/models.ts + models.generated.ts
// Upstream hash: 036bde0a
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

// UnregisterModel removes a model from the registry. Used when an extension
// withdraws a provider that previously registered models. No-op if absent.
func UnregisterModel(provider Provider, modelID string) {
	modelRegistryMu.Lock()
	defer modelRegistryMu.Unlock()
	if pm := modelRegistry[provider]; pm != nil {
		delete(pm, modelID)
		if len(pm) == 0 {
			delete(modelRegistry, provider)
		}
	}
}

// UnregisterProviderModels removes every model under the given provider.
// Used when an extension shuts down to roll back its model contributions.
func UnregisterProviderModels(provider Provider) {
	modelRegistryMu.Lock()
	defer modelRegistryMu.Unlock()
	delete(modelRegistry, provider)
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

// SupportsXhigh reports whether a model supports the "xhigh" thinking level
// as a distinct tier (between "high" and "max"). For models that return
// false, callers should clamp "xhigh" down to whatever top tier the model
// does support (typically "high").
//
// Anthropic: Opus 4.7+ expose a separate xhigh tier. Opus 4.6 and
// Sonnet 4.6 have a "max" tier but no intermediate xhigh, so they clamp
// xhigh to "high" here and use SupportsMax for their top tier. The check
// works for first-party Anthropic IDs (`claude-opus-4-8`), Bedrock IDs
// (`anthropic.claude-opus-4-8`), and Vertex IDs alike, because the model
// family suffix is always present in the ID.
//
// OpenAI: gpt-5.2 and gpt-5.3 treat xhigh as a distinct effort value and
// have been supporting it since before Opus 4.7 shipped.
func SupportsXhigh(model *Model) bool {
	if model == nil {
		return false
	}
	id := model.ID
	if strings.Contains(id, "gpt-5.2") || strings.Contains(id, "gpt-5.3") || strings.Contains(id, "gpt-5.4") || strings.Contains(id, "gpt-5.5") {
		return true
	}
	// Anthropic Opus 4.7+ across first-party, Bedrock, Vertex, etc.
	if strings.Contains(id, "opus-4-8") || strings.Contains(id, "opus-4.8") || strings.Contains(id, "opus-4-7") || strings.Contains(id, "opus-4.7") {
		return true
	}
	return false
}

// SupportsMax reports whether a model supports the "max" thinking level as
// a distinct top tier. Anthropic adaptive-thinking models (Opus 4.6+,
// Sonnet 4.6+, including Opus 4.8) all support max across every surface
// (first-party, Bedrock, Vertex). For other models callers should clamp
// "max" down to the highest tier the model supports.
func SupportsMax(model *Model) bool {
	if model == nil {
		return false
	}
	id := model.ID
	return strings.Contains(id, "opus-4-6") || strings.Contains(id, "opus-4.6") ||
		strings.Contains(id, "opus-4-7") || strings.Contains(id, "opus-4.7") ||
		strings.Contains(id, "opus-4-8") || strings.Contains(id, "opus-4.8") ||
		strings.Contains(id, "sonnet-4-6") || strings.Contains(id, "sonnet-4.6")
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
