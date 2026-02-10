// Ported from: packages/coding-agent/src/core/model-registry.ts
// Upstream hash: 1caadb2e
package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/kfet/pi-go/pkg/ai"
)

// --- OpenAI Compatibility schemas (for JSON validation) ---

// OpenRouterRoutingConfig holds OpenRouter routing preferences from models.json.
type OpenRouterRoutingConfig struct {
	Only  []string `json:"only,omitempty"`
	Order []string `json:"order,omitempty"`
}

// VercelGatewayRoutingConfig holds Vercel AI Gateway routing preferences from models.json.
type VercelGatewayRoutingConfig struct {
	Only  []string `json:"only,omitempty"`
	Order []string `json:"order,omitempty"`
}

// CompatConfig holds OpenAI compatibility settings from models.json.
type CompatConfig struct {
	SupportsStore                    *bool                       `json:"supportsStore,omitempty"`
	SupportsDeveloperRole            *bool                       `json:"supportsDeveloperRole,omitempty"`
	SupportsReasoningEffort          *bool                       `json:"supportsReasoningEffort,omitempty"`
	SupportsUsageInStreaming          *bool                       `json:"supportsUsageInStreaming,omitempty"`
	MaxTokensField                   string                      `json:"maxTokensField,omitempty"`
	RequiresToolResultName           *bool                       `json:"requiresToolResultName,omitempty"`
	RequiresAssistantAfterToolResult *bool                       `json:"requiresAssistantAfterToolResult,omitempty"`
	RequiresThinkingAsText           *bool                       `json:"requiresThinkingAsText,omitempty"`
	RequiresMistralToolIds           *bool                       `json:"requiresMistralToolIds,omitempty"`
	ThinkingFormat                   string                      `json:"thinkingFormat,omitempty"`
	OpenRouterRouting                *OpenRouterRoutingConfig     `json:"openRouterRouting,omitempty"`
	VercelGatewayRouting             *VercelGatewayRoutingConfig  `json:"vercelGatewayRouting,omitempty"`
}

// toOpenAICompletionsCompat converts a CompatConfig to the ai package's OpenAICompletionsCompat.
func (c *CompatConfig) toOpenAICompletionsCompat() *ai.OpenAICompletionsCompat {
	if c == nil {
		return nil
	}
	compat := &ai.OpenAICompletionsCompat{
		SupportsStore:                    c.SupportsStore,
		SupportsDeveloperRole:            c.SupportsDeveloperRole,
		SupportsReasoningEffort:          c.SupportsReasoningEffort,
		SupportsUsageInStreaming:          c.SupportsUsageInStreaming,
		MaxTokensField:                   ai.MaxTokensField(c.MaxTokensField),
		RequiresToolResultName:           c.RequiresToolResultName,
		RequiresAssistantAfterToolResult: c.RequiresAssistantAfterToolResult,
		RequiresThinkingAsText:           c.RequiresThinkingAsText,
		RequiresMistralToolIds:           c.RequiresMistralToolIds,
		ThinkingFormat:                   ai.ThinkingFormat(c.ThinkingFormat),
	}
	if c.OpenRouterRouting != nil {
		compat.OpenRouterRouting = &ai.OpenRouterRouting{
			Only:  c.OpenRouterRouting.Only,
			Order: c.OpenRouterRouting.Order,
		}
	}
	if c.VercelGatewayRouting != nil {
		compat.VercelGatewayRouting = &ai.VercelGatewayRouting{
			Only:  c.VercelGatewayRouting.Only,
			Order: c.VercelGatewayRouting.Order,
		}
	}
	return compat
}

// --- Model definition from models.json ---

// ModelCostConfig holds cost information from models.json.
type ModelCostConfig struct {
	Input      *float64 `json:"input,omitempty"`
	Output     *float64 `json:"output,omitempty"`
	CacheRead  *float64 `json:"cacheRead,omitempty"`
	CacheWrite *float64 `json:"cacheWrite,omitempty"`
}

// ModelDefinition is a custom model definition in models.json.
type ModelDefinition struct {
	ID            string            `json:"id"`
	Name          string            `json:"name,omitempty"`
	Api           string            `json:"api,omitempty"`
	Reasoning     *bool             `json:"reasoning,omitempty"`
	Input         []string          `json:"input,omitempty"`
	Cost          *ModelCostConfig  `json:"cost,omitempty"`
	ContextWindow *int              `json:"contextWindow,omitempty"`
	MaxTokens     *int              `json:"maxTokens,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	Compat        *CompatConfig     `json:"compat,omitempty"`
}

// ModelOverride holds per-model overrides (all fields optional, merged with built-in model).
type ModelOverride struct {
	Name          string            `json:"name,omitempty"`
	Reasoning     *bool             `json:"reasoning,omitempty"`
	Input         []string          `json:"input,omitempty"`
	Cost          *ModelCostConfig  `json:"cost,omitempty"`
	ContextWindow *int              `json:"contextWindow,omitempty"`
	MaxTokens     *int              `json:"maxTokens,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	Compat        *CompatConfig     `json:"compat,omitempty"`
}

// ProviderConfig is the per-provider section in models.json.
type ProviderConfig struct {
	BaseURL        string                    `json:"baseUrl,omitempty"`
	ApiKey         string                    `json:"apiKey,omitempty"`
	Api            string                    `json:"api,omitempty"`
	Headers        map[string]string         `json:"headers,omitempty"`
	AuthHeader     *bool                     `json:"authHeader,omitempty"`
	Models         []ModelDefinition         `json:"models,omitempty"`
	ModelOverrides map[string]ModelOverride   `json:"modelOverrides,omitempty"`
}

// ModelsConfig is the top-level models.json structure.
type ModelsConfig struct {
	Providers map[string]ProviderConfig `json:"providers"`
}

// --- Provider override (baseUrl, headers, apiKey) for built-in models ---

// ProviderOverride holds provider-level overrides for built-in models.
type ProviderOverride struct {
	BaseURL string
	Headers map[string]string
	ApiKey  string
}

// --- Custom models result ---

// CustomModelsResult is the result of loading custom models from models.json.
type CustomModelsResult struct {
	Models         []*ai.Model
	Overrides      map[string]*ProviderOverride           // provider -> override
	ModelOverrides map[string]map[string]*ModelOverride     // provider -> modelId -> override
	Error          string
}

func emptyCustomModelsResult(errMsg string) *CustomModelsResult {
	return &CustomModelsResult{
		Overrides:      make(map[string]*ProviderOverride),
		ModelOverrides: make(map[string]map[string]*ModelOverride),
		Error:          errMsg,
	}
}

// --- Compat merging ---

func mergeCompat(base any, override *CompatConfig) any {
	if override == nil {
		return base
	}

	// Start with override values, overlay on base
	merged := &ai.OpenAICompletionsCompat{}

	// Copy base fields if it's OpenAICompletionsCompat
	if baseCompat, ok := base.(*ai.OpenAICompletionsCompat); ok && baseCompat != nil {
		*merged = *baseCompat
	}

	overrideCompat := override.toOpenAICompletionsCompat()

	// Overlay non-nil override fields
	if overrideCompat.SupportsStore != nil {
		merged.SupportsStore = overrideCompat.SupportsStore
	}
	if overrideCompat.SupportsDeveloperRole != nil {
		merged.SupportsDeveloperRole = overrideCompat.SupportsDeveloperRole
	}
	if overrideCompat.SupportsReasoningEffort != nil {
		merged.SupportsReasoningEffort = overrideCompat.SupportsReasoningEffort
	}
	if overrideCompat.SupportsUsageInStreaming != nil {
		merged.SupportsUsageInStreaming = overrideCompat.SupportsUsageInStreaming
	}
	if overrideCompat.MaxTokensField != "" {
		merged.MaxTokensField = overrideCompat.MaxTokensField
	}
	if overrideCompat.RequiresToolResultName != nil {
		merged.RequiresToolResultName = overrideCompat.RequiresToolResultName
	}
	if overrideCompat.RequiresAssistantAfterToolResult != nil {
		merged.RequiresAssistantAfterToolResult = overrideCompat.RequiresAssistantAfterToolResult
	}
	if overrideCompat.RequiresThinkingAsText != nil {
		merged.RequiresThinkingAsText = overrideCompat.RequiresThinkingAsText
	}
	if overrideCompat.RequiresMistralToolIds != nil {
		merged.RequiresMistralToolIds = overrideCompat.RequiresMistralToolIds
	}
	if overrideCompat.ThinkingFormat != "" {
		merged.ThinkingFormat = overrideCompat.ThinkingFormat
	}

	// Merge routing preferences
	if overrideCompat.OpenRouterRouting != nil {
		if merged.OpenRouterRouting == nil {
			merged.OpenRouterRouting = overrideCompat.OpenRouterRouting
		} else {
			if overrideCompat.OpenRouterRouting.Only != nil {
				merged.OpenRouterRouting.Only = overrideCompat.OpenRouterRouting.Only
			}
			if overrideCompat.OpenRouterRouting.Order != nil {
				merged.OpenRouterRouting.Order = overrideCompat.OpenRouterRouting.Order
			}
		}
	}
	if overrideCompat.VercelGatewayRouting != nil {
		if merged.VercelGatewayRouting == nil {
			merged.VercelGatewayRouting = overrideCompat.VercelGatewayRouting
		} else {
			if overrideCompat.VercelGatewayRouting.Only != nil {
				merged.VercelGatewayRouting.Only = overrideCompat.VercelGatewayRouting.Only
			}
			if overrideCompat.VercelGatewayRouting.Order != nil {
				merged.VercelGatewayRouting.Order = overrideCompat.VercelGatewayRouting.Order
			}
		}
	}

	return merged
}

// applyModelOverride deep merges a ModelOverride into a Model.
func applyModelOverride(model *ai.Model, override *ModelOverride) *ai.Model {
	result := *model // shallow copy

	if override.Name != "" {
		result.Name = override.Name
	}
	if override.Reasoning != nil {
		result.Reasoning = *override.Reasoning
	}
	if override.Input != nil {
		result.Input = override.Input
	}
	if override.ContextWindow != nil {
		result.ContextWindow = *override.ContextWindow
	}
	if override.MaxTokens != nil {
		result.MaxTokens = *override.MaxTokens
	}

	// Merge cost (partial override)
	if override.Cost != nil {
		cost := model.Cost
		if override.Cost.Input != nil {
			cost.Input = *override.Cost.Input
		}
		if override.Cost.Output != nil {
			cost.Output = *override.Cost.Output
		}
		if override.Cost.CacheRead != nil {
			cost.CacheRead = *override.Cost.CacheRead
		}
		if override.Cost.CacheWrite != nil {
			cost.CacheWrite = *override.Cost.CacheWrite
		}
		result.Cost = cost
	}

	// Merge headers
	if len(override.Headers) > 0 {
		resolvedHeaders := ResolveHeaders(override.Headers)
		if resolvedHeaders != nil {
			merged := make(map[string]string)
			for k, v := range model.Headers {
				merged[k] = v
			}
			for k, v := range resolvedHeaders {
				merged[k] = v
			}
			result.Headers = merged
		}
	}

	// Deep merge compat
	result.Compat = mergeCompat(model.Compat, override.Compat)

	return &result
}

// --- ModelRegistry ---

// OAuthProviderInterface allows OAuth providers to modify models.
// This is a simplified version; full OAuth flow is not yet implemented in Go.
type OAuthProviderInterface struct {
	ID           string
	ModifyModels func(models []*ai.Model, cred *AuthCredential) []*ai.Model
}

// ProviderConfigInput is the input type for RegisterProvider.
type ProviderConfigInput struct {
	BaseURL      string
	ApiKey       string
	Api          ai.Api
	StreamSimple ai.SimpleStreamFunction
	Headers      map[string]string
	AuthHeader   bool
	OAuth        *OAuthProviderInterface
	Models       []ProviderModelInput
}

// ProviderModelInput defines a model for dynamic provider registration.
type ProviderModelInput struct {
	ID            string
	Name          string
	Api           ai.Api
	Reasoning     bool
	Input         []string
	Cost          ai.ModelCost
	ContextWindow int
	MaxTokens     int
	Headers       map[string]string
	Compat        any
}

// ModelRegistry loads and manages models, resolves API keys via AuthStorage.
type ModelRegistry struct {
	mu                    sync.RWMutex
	models                []*ai.Model
	customProviderApiKeys map[string]string
	registeredProviders   map[string]*ProviderConfigInput
	loadError             string
	authStorage           *AuthStorage
	modelsJsonPath        string
}

// NewModelRegistry creates a new ModelRegistry.
// If modelsJsonPath is empty, no custom models are loaded.
func NewModelRegistry(authStorage *AuthStorage, modelsJsonPath string) *ModelRegistry {
	r := &ModelRegistry{
		customProviderApiKeys: make(map[string]string),
		registeredProviders:   make(map[string]*ProviderConfigInput),
		authStorage:           authStorage,
		modelsJsonPath:        modelsJsonPath,
	}

	// Set up fallback resolver for custom provider API keys
	authStorage.SetFallbackResolver(func(provider string) string {
		r.mu.RLock()
		defer r.mu.RUnlock()
		keyConfig, ok := r.customProviderApiKeys[provider]
		if !ok {
			return ""
		}
		return ResolveConfigValue(keyConfig)
	})

	// Load models
	r.loadModels()
	return r
}

// Refresh reloads models from disk (built-in + custom from models.json).
func (r *ModelRegistry) Refresh() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.customProviderApiKeys = make(map[string]string)
	r.loadError = ""
	r.loadModels()

	for providerName, config := range r.registeredProviders {
		r.applyProviderConfig(providerName, config)
	}
}

// GetError returns any error from loading models.json (empty string if no error).
func (r *ModelRegistry) GetError() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.loadError
}

func (r *ModelRegistry) loadModels() {
	var result *CustomModelsResult
	if r.modelsJsonPath != "" {
		result = r.loadCustomModels(r.modelsJsonPath)
	} else {
		result = emptyCustomModelsResult("")
	}

	if result.Error != "" {
		r.loadError = result.Error
		// Keep built-in models even if custom models failed to load
	}

	builtInModels := r.loadBuiltInModels(result.Overrides, result.ModelOverrides)
	combined := r.mergeCustomModels(builtInModels, result.Models)

	// TODO: Let OAuth providers modify their models
	r.models = combined
}

// loadBuiltInModels loads built-in models and applies provider/model overrides.
func (r *ModelRegistry) loadBuiltInModels(
	overrides map[string]*ProviderOverride,
	modelOverrides map[string]map[string]*ModelOverride,
) []*ai.Model {
	var result []*ai.Model

	for _, provider := range ai.GetProviders() {
		models := ai.GetModels(provider)
		providerOverride := overrides[provider]
		perModelOverrides := modelOverrides[provider]

		for _, m := range models {
			model := m

			// Apply provider-level baseUrl/headers override
			if providerOverride != nil {
				copy := *model
				if providerOverride.BaseURL != "" {
					copy.BaseURL = providerOverride.BaseURL
				}
				resolvedHeaders := ResolveHeaders(providerOverride.Headers)
				if resolvedHeaders != nil {
					merged := make(map[string]string)
					for k, v := range model.Headers {
						merged[k] = v
					}
					for k, v := range resolvedHeaders {
						merged[k] = v
					}
					copy.Headers = merged
				}
				model = &copy
			}

			// Apply per-model override
			if perModelOverrides != nil {
				if override, ok := perModelOverrides[m.ID]; ok {
					model = applyModelOverride(model, override)
				}
			}

			result = append(result, model)
		}
	}

	return result
}

// mergeCustomModels merges custom models into built-in list by provider+id (custom wins).
func (r *ModelRegistry) mergeCustomModels(builtIn []*ai.Model, custom []*ai.Model) []*ai.Model {
	merged := make([]*ai.Model, len(builtIn))
	copy(merged, builtIn)

	for _, customModel := range custom {
		existingIdx := -1
		for i, m := range merged {
			if m.Provider == customModel.Provider && m.ID == customModel.ID {
				existingIdx = i
				break
			}
		}
		if existingIdx >= 0 {
			merged[existingIdx] = customModel
		} else {
			merged = append(merged, customModel)
		}
	}

	return merged
}

func (r *ModelRegistry) loadCustomModels(modelsJsonPath string) *CustomModelsResult {
	data, err := os.ReadFile(modelsJsonPath)
	if err != nil {
		if os.IsNotExist(err) {
			return emptyCustomModelsResult("")
		}
		return emptyCustomModelsResult(
			fmt.Sprintf("Failed to load models.json: %v\n\nFile: %s", err, modelsJsonPath),
		)
	}

	var config ModelsConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return emptyCustomModelsResult(
			fmt.Sprintf("Failed to parse models.json: %v\n\nFile: %s", err, modelsJsonPath),
		)
	}

	if config.Providers == nil {
		return emptyCustomModelsResult(
			fmt.Sprintf("Invalid models.json: missing \"providers\" field\n\nFile: %s", modelsJsonPath),
		)
	}

	// Validate config
	if err := r.validateConfig(&config); err != nil {
		return emptyCustomModelsResult(
			fmt.Sprintf("%v\n\nFile: %s", err, modelsJsonPath),
		)
	}

	overrides := make(map[string]*ProviderOverride)
	modelOverrides := make(map[string]map[string]*ModelOverride)

	for providerName, providerConfig := range config.Providers {
		// Apply provider-level overrides
		if providerConfig.BaseURL != "" || len(providerConfig.Headers) > 0 || providerConfig.ApiKey != "" {
			overrides[providerName] = &ProviderOverride{
				BaseURL: providerConfig.BaseURL,
				Headers: providerConfig.Headers,
				ApiKey:  providerConfig.ApiKey,
			}
		}

		// Store API key for fallback resolver
		if providerConfig.ApiKey != "" {
			r.customProviderApiKeys[providerName] = providerConfig.ApiKey
		}

		if len(providerConfig.ModelOverrides) > 0 {
			perModel := make(map[string]*ModelOverride)
			for modelID, override := range providerConfig.ModelOverrides {
				o := override // copy
				perModel[modelID] = &o
			}
			modelOverrides[providerName] = perModel
		}
	}

	models := r.parseModels(&config)
	return &CustomModelsResult{
		Models:         models,
		Overrides:      overrides,
		ModelOverrides: modelOverrides,
	}
}

func (r *ModelRegistry) validateConfig(config *ModelsConfig) error {
	for providerName, providerConfig := range config.Providers {
		hasModels := len(providerConfig.Models) > 0
		hasModelOverrides := len(providerConfig.ModelOverrides) > 0

		if !hasModels {
			// Override-only config: needs baseUrl OR modelOverrides (or both)
			if providerConfig.BaseURL == "" && !hasModelOverrides {
				return fmt.Errorf("Provider %s: must specify \"baseUrl\", \"modelOverrides\", or \"models\"", providerName)
			}
		} else {
			// Custom models require endpoint + auth
			if providerConfig.BaseURL == "" {
				return fmt.Errorf("Provider %s: \"baseUrl\" is required when defining custom models", providerName)
			}
			if providerConfig.ApiKey == "" {
				return fmt.Errorf("Provider %s: \"apiKey\" is required when defining custom models", providerName)
			}
		}

		hasProviderApi := providerConfig.Api != ""
		for _, modelDef := range providerConfig.Models {
			hasModelApi := modelDef.Api != ""

			if !hasProviderApi && !hasModelApi {
				return fmt.Errorf(
					"Provider %s, model %s: no \"api\" specified. Set at provider or model level",
					providerName, modelDef.ID,
				)
			}

			if modelDef.ID == "" {
				return fmt.Errorf("Provider %s: model missing \"id\"", providerName)
			}
			if modelDef.ContextWindow != nil && *modelDef.ContextWindow <= 0 {
				return fmt.Errorf("Provider %s, model %s: invalid contextWindow", providerName, modelDef.ID)
			}
			if modelDef.MaxTokens != nil && *modelDef.MaxTokens <= 0 {
				return fmt.Errorf("Provider %s, model %s: invalid maxTokens", providerName, modelDef.ID)
			}
		}
	}
	return nil
}

func (r *ModelRegistry) parseModels(config *ModelsConfig) []*ai.Model {
	var models []*ai.Model

	for providerName, providerConfig := range config.Providers {
		if len(providerConfig.Models) == 0 {
			continue // Override-only, no custom models
		}

		// Store API key config for fallback resolver
		if providerConfig.ApiKey != "" {
			r.customProviderApiKeys[providerName] = providerConfig.ApiKey
		}

		for _, modelDef := range providerConfig.Models {
			api := modelDef.Api
			if api == "" {
				api = providerConfig.Api
			}
			if api == "" {
				continue
			}

			// Merge headers: provider headers are base, model headers override
			providerHeaders := ResolveHeaders(providerConfig.Headers)
			modelHeaders := ResolveHeaders(modelDef.Headers)
			var headers map[string]string
			if providerHeaders != nil || modelHeaders != nil {
				headers = make(map[string]string)
				for k, v := range providerHeaders {
					headers[k] = v
				}
				for k, v := range modelHeaders {
					headers[k] = v
				}
			}

			// If authHeader is true, add Authorization header with resolved API key
			if providerConfig.AuthHeader != nil && *providerConfig.AuthHeader && providerConfig.ApiKey != "" {
				resolvedKey := ResolveConfigValue(providerConfig.ApiKey)
				if resolvedKey != "" {
					if headers == nil {
						headers = make(map[string]string)
					}
					headers["Authorization"] = "Bearer " + resolvedKey
				}
			}

			// Apply defaults for optional fields
			name := modelDef.Name
			if name == "" {
				name = modelDef.ID
			}

			reasoning := false
			if modelDef.Reasoning != nil {
				reasoning = *modelDef.Reasoning
			}

			input := []string{"text"}
			if modelDef.Input != nil {
				input = modelDef.Input
			}

			cost := ai.ModelCost{}
			if modelDef.Cost != nil {
				if modelDef.Cost.Input != nil {
					cost.Input = *modelDef.Cost.Input
				}
				if modelDef.Cost.Output != nil {
					cost.Output = *modelDef.Cost.Output
				}
				if modelDef.Cost.CacheRead != nil {
					cost.CacheRead = *modelDef.Cost.CacheRead
				}
				if modelDef.Cost.CacheWrite != nil {
					cost.CacheWrite = *modelDef.Cost.CacheWrite
				}
			}

			contextWindow := 128000
			if modelDef.ContextWindow != nil {
				contextWindow = *modelDef.ContextWindow
			}

			maxTokens := 16384
			if modelDef.MaxTokens != nil {
				maxTokens = *modelDef.MaxTokens
			}

			var compat any
			if modelDef.Compat != nil {
				compat = modelDef.Compat.toOpenAICompletionsCompat()
			}

			models = append(models, &ai.Model{
				ID:            modelDef.ID,
				Name:          name,
				Api:           api,
				Provider:      providerName,
				BaseURL:       providerConfig.BaseURL,
				Reasoning:     reasoning,
				Input:         input,
				Cost:          cost,
				ContextWindow: contextWindow,
				MaxTokens:     maxTokens,
				Headers:       headers,
				Compat:        compat,
			})
		}
	}

	return models
}

// GetAll returns all models (built-in + custom).
func (r *ModelRegistry) GetAll() []*ai.Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*ai.Model, len(r.models))
	copy(result, r.models)
	return result
}

// GetAvailable returns only models that have auth configured.
func (r *ModelRegistry) GetAvailable() []*ai.Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*ai.Model
	for _, m := range r.models {
		if r.authStorage.HasAuth(m.Provider) {
			result = append(result, m)
		}
	}
	return result
}

// Find returns a model by provider and ID, or nil if not found.
func (r *ModelRegistry) Find(provider, modelID string) *ai.Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, m := range r.models {
		if m.Provider == provider && m.ID == modelID {
			return m
		}
	}
	return nil
}

// GetApiKey returns the API key for a model's provider.
func (r *ModelRegistry) GetApiKey(model *ai.Model) string {
	return r.authStorage.GetApiKey(model.Provider)
}

// GetApiKeyForProvider returns the API key for a provider.
func (r *ModelRegistry) GetApiKeyForProvider(provider string) string {
	return r.authStorage.GetApiKey(provider)
}

// IsUsingOAuth checks if a model is using OAuth credentials.
func (r *ModelRegistry) IsUsingOAuth(model *ai.Model) bool {
	cred := r.authStorage.Get(model.Provider)
	return cred != nil && cred.Type == CredentialTypeOAuth
}

// RegisterProvider registers a provider dynamically (from extensions).
func (r *ModelRegistry) RegisterProvider(providerName string, config *ProviderConfigInput) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.registeredProviders[providerName] = config
	r.applyProviderConfig(providerName, config)
}

func (r *ModelRegistry) applyProviderConfig(providerName string, config *ProviderConfigInput) {
	// Register streamSimple if provided
	if config.StreamSimple != nil {
		if config.Api == "" {
			panic(fmt.Sprintf("Provider %s: \"api\" is required when registering streamSimple", providerName))
		}
		ai.DefaultRegistry.RegisterApiProvider(&ai.ApiProvider{
			Api:          config.Api,
			StreamSimple: config.StreamSimple,
		}, "modelregistry:"+providerName)
	}

	// Store API key
	if config.ApiKey != "" {
		r.customProviderApiKeys[providerName] = config.ApiKey
	}

	if len(config.Models) > 0 {
		// Full replacement: remove existing models for this provider
		filtered := make([]*ai.Model, 0, len(r.models))
		for _, m := range r.models {
			if m.Provider != providerName {
				filtered = append(filtered, m)
			}
		}
		r.models = filtered

		if config.BaseURL == "" {
			panic(fmt.Sprintf("Provider %s: \"baseUrl\" is required when defining models", providerName))
		}
		if config.ApiKey == "" && config.OAuth == nil {
			panic(fmt.Sprintf("Provider %s: \"apiKey\" or \"oauth\" is required when defining models", providerName))
		}

		for _, modelDef := range config.Models {
			api := modelDef.Api
			if api == "" {
				api = config.Api
			}
			if api == "" {
				panic(fmt.Sprintf("Provider %s, model %s: no \"api\" specified", providerName, modelDef.ID))
			}

			// Merge headers
			providerHeaders := ResolveHeaders(config.Headers)
			modelHeaders := ResolveHeaders(modelDef.Headers)
			var headers map[string]string
			if providerHeaders != nil || modelHeaders != nil {
				headers = make(map[string]string)
				for k, v := range providerHeaders {
					headers[k] = v
				}
				for k, v := range modelHeaders {
					headers[k] = v
				}
			}

			// If authHeader is true, add Authorization header
			if config.AuthHeader && config.ApiKey != "" {
				resolvedKey := ResolveConfigValue(config.ApiKey)
				if resolvedKey != "" {
					if headers == nil {
						headers = make(map[string]string)
					}
					headers["Authorization"] = "Bearer " + resolvedKey
				}
			}

			r.models = append(r.models, &ai.Model{
				ID:            modelDef.ID,
				Name:          modelDef.Name,
				Api:           api,
				Provider:      providerName,
				BaseURL:       config.BaseURL,
				Reasoning:     modelDef.Reasoning,
				Input:         modelDef.Input,
				Cost:          modelDef.Cost,
				ContextWindow: modelDef.ContextWindow,
				MaxTokens:     modelDef.MaxTokens,
				Headers:       headers,
				Compat:        modelDef.Compat,
			})
		}

		// Apply OAuth modifyModels if credentials exist
		if config.OAuth != nil && config.OAuth.ModifyModels != nil {
			cred := r.authStorage.Get(providerName)
			if cred != nil && cred.Type == CredentialTypeOAuth {
				r.models = config.OAuth.ModifyModels(r.models, cred)
			}
		}
	} else if config.BaseURL != "" {
		// Override-only: update baseUrl/headers for existing models
		resolvedHeaders := ResolveHeaders(config.Headers)
		for i, m := range r.models {
			if m.Provider != providerName {
				continue
			}
			copy := *m
			copy.BaseURL = config.BaseURL
			if resolvedHeaders != nil {
				merged := make(map[string]string)
				for k, v := range m.Headers {
					merged[k] = v
				}
				for k, v := range resolvedHeaders {
					merged[k] = v
				}
				copy.Headers = merged
			}
			r.models[i] = &copy
		}
	}
}

// DefaultModelsJsonPath returns the default path for models.json.
func DefaultModelsJsonPath(agentDir string) string {
	return filepath.Join(agentDir, "models.json")
}
