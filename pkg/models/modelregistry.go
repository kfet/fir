// Ported from: packages/coding-agent/src/core/model-registry.ts
// Upstream hash: a1edb8a4
package models

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
	configpkg "github.com/kfet/fir/pkg/config"
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
	SupportsUsageInStreaming         *bool                       `json:"supportsUsageInStreaming,omitempty"`
	MaxTokensField                   string                      `json:"maxTokensField,omitempty"`
	RequiresToolResultName           *bool                       `json:"requiresToolResultName,omitempty"`
	RequiresAssistantAfterToolResult *bool                       `json:"requiresAssistantAfterToolResult,omitempty"`
	RequiresThinkingAsText           *bool                       `json:"requiresThinkingAsText,omitempty"`
	ThinkingFormat                   string                      `json:"thinkingFormat,omitempty"`
	OpenRouterRouting                *OpenRouterRoutingConfig    `json:"openRouterRouting,omitempty"`
	VercelGatewayRouting             *VercelGatewayRoutingConfig `json:"vercelGatewayRouting,omitempty"`
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
		SupportsUsageInStreaming:         c.SupportsUsageInStreaming,
		MaxTokensField:                   ai.MaxTokensField(c.MaxTokensField),
		RequiresToolResultName:           c.RequiresToolResultName,
		RequiresAssistantAfterToolResult: c.RequiresAssistantAfterToolResult,
		RequiresThinkingAsText:           c.RequiresThinkingAsText,
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
	ID                    string            `json:"id"`
	Name                  string            `json:"name,omitempty"`
	Api                   string            `json:"api,omitempty"`
	BaseURL               string            `json:"baseUrl,omitempty"`
	Reasoning             *bool             `json:"reasoning,omitempty"`
	ReasoningEffortValues []string          `json:"reasoningEffortValues,omitempty"`
	Input                 []string          `json:"input,omitempty"`
	Cost                  *ModelCostConfig  `json:"cost,omitempty"`
	ContextWindow         *int              `json:"contextWindow,omitempty"`
	MaxTokens             *int              `json:"maxTokens,omitempty"`
	Headers               map[string]string `json:"headers,omitempty"`
	Compat                *CompatConfig     `json:"compat,omitempty"`
	ServerTools           []string          `json:"serverTools,omitempty"`
	Compaction            *bool             `json:"compaction,omitempty"`
	AdaptiveThinking      *bool             `json:"adaptiveThinking,omitempty"`
	SWEScore              *float64          `json:"sweScore,omitempty"`
}

// ModelOverride holds per-model overrides (all fields optional, merged with built-in model).
//
// Note: overrides only apply to BUILT-IN models. A model defined by a higher
// layer (catalog overlay, models.json, models.d) is redefined wholesale by the
// next layer up rather than field-merged — so to correct one field of an
// overlay-defined model you must redefine that model, not override it. This is
// the long-standing models.json/models.d semantic; "user wins" is most
// predictable when winning is total.
//
// Wire-format warning: see ProviderConfig.
type ModelOverride struct {
	Name      string `json:"name,omitempty"`
	Reasoning *bool  `json:"reasoning,omitempty"`
	// ReasoningEffortValues overrides the allowed reasoning.effort enum used to
	// clamp the outbound effort (see clampEffortToEnum). Needed for providers
	// whose models accept a restricted set; empty leaves the built-in value.
	ReasoningEffortValues []string          `json:"reasoningEffortValues,omitempty"`
	Input                 []string          `json:"input,omitempty"`
	Cost                  *ModelCostConfig  `json:"cost,omitempty"`
	ContextWindow         *int              `json:"contextWindow,omitempty"`
	MaxTokens             *int              `json:"maxTokens,omitempty"`
	Headers               map[string]string `json:"headers,omitempty"`
	Compat                *CompatConfig     `json:"compat,omitempty"`
	ServerTools           []string          `json:"serverTools,omitempty"`
	Compaction            *bool             `json:"compaction,omitempty"`
	AdaptiveThinking      *bool             `json:"adaptiveThinking,omitempty"`
	SWEScore              *float64          `json:"sweScore,omitempty"`
}

// ProviderConfig is the per-provider section in models.json.
//
// Wire-format warning: this type (together with ModelDefinition and
// ModelOverride) is also the schema of the published fir-dist catalog overlay,
// which floats independently of binaries. Changes must be purely additive —
// see the compatibility rules at the top of catalog.go before touching it.
type ProviderConfig struct {
	BaseURL        string                   `json:"baseUrl,omitempty"`
	ApiKey         string                   `json:"apiKey,omitempty"`
	Api            string                   `json:"api,omitempty"`
	Headers        map[string]string        `json:"headers,omitempty"`
	AuthHeader     *bool                    `json:"authHeader,omitempty"`
	Models         []ModelDefinition        `json:"models,omitempty"`
	ModelOverrides map[string]ModelOverride `json:"modelOverrides,omitempty"`
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

// Model definition origins, reported by ModelRegistry.ModelOrigin.
const (
	// OriginBuiltIn is the compiled-in catalog (models_generated.go).
	OriginBuiltIn = "builtin"
	// OriginOverlay is the fir-dist catalog overlay.
	OriginOverlay = "overlay"
	// OriginUserModelsJSON is the user's models.json.
	OriginUserModelsJSON = "user:models.json"
	// OriginUserFragment is a user models.d/*.json fragment; the fragment
	// filename is appended (e.g. "user:models.d/10-local.json").
	OriginUserFragment = "user:models.d/"
)

// CustomModelsResult is the result of loading custom models from models.json.
type CustomModelsResult struct {
	Models         []*ai.Model
	Overrides      map[string]*ProviderOverride         // provider -> override
	ModelOverrides map[string]map[string]*ModelOverride // provider -> modelId -> override
	ApiKeys        map[string]string                    // provider -> apiKey config
	Origins        map[string]string                    // originKey -> origin constant
	Error          string
}

func emptyCustomModelsResult(errMsg string) *CustomModelsResult {
	return &CustomModelsResult{
		Overrides:      make(map[string]*ProviderOverride),
		ModelOverrides: make(map[string]map[string]*ModelOverride),
		ApiKeys:        make(map[string]string),
		Origins:        make(map[string]string),
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
	if override.ReasoningEffortValues != nil {
		result.ReasoningEffortValues = append([]string(nil), override.ReasoningEffortValues...)
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
	if override.ServerTools != nil {
		result.ServerTools = append([]string(nil), override.ServerTools...)
	}
	if override.Compaction != nil {
		result.Compaction = *override.Compaction
	}
	if override.AdaptiveThinking != nil {
		result.AdaptiveThinking = *override.AdaptiveThinking
	}
	if override.SWEScore != nil {
		result.SWEScore = *override.SWEScore
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
		resolvedHeaders := configpkg.ResolveHeaders(override.Headers)
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

// ModelRegistry loads and manages models, resolves API keys via AuthStorage.
type ModelRegistry struct {
	mu                    sync.RWMutex
	models                []*ai.Model
	customProviderApiKeys map[string]string
	loadError             string
	authStorage           *auth.AuthStorage
	modelsJsonPath        string

	// refreshMu serialises whole-registry rebuilds. Distinct from mu: the
	// rebuild deliberately runs without mu held (see Refresh).
	refreshMu sync.Mutex

	// catalogRaw is the exact bytes of the catalog overlay the current models
	// were built from (fir-dist document merged above built-ins and below
	// user config), used to detect a changed document after a fetch.
	catalogRaw []byte
	// modelOrigins maps originKey(provider, id) to the layer that last
	// defined or overrode that model. Models absent from the map are
	// built-ins.
	modelOrigins map[string]string
	// providerDefaults holds the overlay's DefaultModelID overrides.
	providerDefaults map[string]string

	// liveModels tracks which models are actually available per provider,
	// populated by background API calls. Protected by liveModelsMu.
	liveModelsMu sync.RWMutex
	liveModels   map[string]*liveModelState
	liveCacheDir string
	liveExtReady <-chan struct{}

	// Synthesis caches for live-only model IDs not present in the built-in
	// registry. Protected by synthMu (independent of r.mu so read paths like
	// Find/GetAvailable don't have to upgrade their lock).
	synthMu             sync.Mutex
	synthesised         map[string]*ai.Model   // cache: provider\x00id -> synthesised model (or nil)
	synthesisedSiblings map[string][]*ai.Model // provider -> built-in siblings for synthesis
}

// NewModelRegistry creates a new ModelRegistry. If modelsJsonPath is empty no
// user config is read (and no catalog-overlay cache is kept), but the built-in
// catalog and the embedded catalog overlay still apply.
func NewModelRegistry(authStorage *auth.AuthStorage, modelsJsonPath string) *ModelRegistry {
	r := &ModelRegistry{
		customProviderApiKeys: make(map[string]string),
		authStorage:           authStorage,
		modelsJsonPath:        modelsJsonPath,
		liveModels:            make(map[string]*liveModelState),
	}

	// Set up fallback resolver for custom provider API keys
	authStorage.SetFallbackResolver(func(provider string) string {
		r.mu.RLock()
		defer r.mu.RUnlock()
		keyConfig, ok := r.customProviderApiKeys[provider]
		if !ok {
			return ""
		}
		return configpkg.ResolveConfigValue(keyConfig)
	})

	// Load models
	r.applySnapshot(r.buildModels())

	return r
}

// Refresh reloads models from disk (built-in + catalog overlay + custom from
// models.json).
func (r *ModelRegistry) Refresh() {
	// Serialise rebuilds. Readers never block on the (slow) build, but two
	// concurrent rebuilds must not interleave: they share the global API
	// provider registry, and the last swap would otherwise win regardless of
	// which build started with the fresher state.
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()

	// Reset the API provider registry so dynamic registrations are rebuilt.
	ai.DefaultRegistry.ResetApiProviders()

	// Build outside r.mu, then swap under it. buildModels runs OAuth
	// ModifyModels callbacks, and the extension-backed implementations do a
	// blocking JSON-RPC round trip that may re-enter the registry — doing
	// that under the write lock would deadlock the process.
	snap := r.buildModels()

	r.mu.Lock()
	r.applySnapshot(snap)
	r.mu.Unlock()

	// Synthesised models cloned siblings from the previous snapshot; drop
	// them so the next synth uses fresh siblings.
	r.invalidateSynthesisCache()
}

// modelSnapshot is a fully-computed registry state, built without holding r.mu.
type modelSnapshot struct {
	models           []*ai.Model
	apiKeys          map[string]string
	origins          map[string]string
	providerDefaults map[string]string
	catalogRaw       []byte
	loadError        string
}

// applySnapshot installs a snapshot. Caller holds r.mu (or holds the only
// reference, as in the constructor).
func (r *ModelRegistry) applySnapshot(s modelSnapshot) {
	r.models = s.models
	r.customProviderApiKeys = s.apiKeys
	r.modelOrigins = s.origins
	r.providerDefaults = s.providerDefaults
	r.catalogRaw = s.catalogRaw
	r.loadError = s.loadError
}

// GetError returns any error from loading models.json (empty string if no error).
func (r *ModelRegistry) GetError() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.loadError
}

// ModelOrigin reports which layer defined or last overrode a model: one of the
// Origin* constants. Provenance is a first-class requirement — when a model
// resolves wrongly, the operator must be able to see which layer won without
// reading source.
func (r *ModelRegistry) ModelOrigin(provider, modelID string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if origin, ok := r.modelOrigins[originKey(provider, modelID)]; ok {
		return origin
	}
	return OriginBuiltIn
}

// DefaultModelForProvider returns the default model ID for a provider, or ""
// if the provider isn't registered (or has no default). The catalog overlay's
// providerDefaults win over the compiled-in ai.RegisteredProvider value, which
// stays as the offline fallback — moving a provider default is plainly data
// and must not require a binary release.
func (r *ModelRegistry) DefaultModelForProvider(p ai.Provider) string {
	r.mu.RLock()
	id, ok := r.providerDefaults[string(p)]
	r.mu.RUnlock()
	if ok && id != "" {
		return id
	}
	rec := ai.GetProviderRecord(p)
	if rec == nil {
		return ""
	}
	return rec.DefaultModelID
}

// buildModels computes the whole registry state. It must NOT be called with
// r.mu held: it runs OAuth ModifyModels callbacks which may block on I/O.
func (r *ModelRegistry) buildModels() modelSnapshot {
	overlay, overlayRaw := r.loadCatalogOverlay()

	result := r.loadCustomModels(r.modelsJsonPath, overlay)

	builtInModels := r.loadBuiltInModels(result.Overrides, result.ModelOverrides)
	combined := r.mergeCustomModels(builtInModels, result.Models)

	// Let OAuth providers modify their models (e.g., update baseUrl)
	for _, oauthProvider := range r.authStorage.GetOAuthProviders() {
		cred := r.authStorage.Get(oauthProvider.ID())
		if cred != nil && cred.Type == auth.CredentialTypeOAuth {
			oauthCreds := auth.AuthCredToOAuthCreds(cred)
			if modified := oauthProvider.ModifyModels(combined, oauthCreds); modified != nil {
				combined = modified
			}
		}
	}

	defaults := map[string]string{}
	if overlay != nil {
		for provider, id := range overlay.ProviderDefaults {
			defaults[provider] = id
		}
	}

	return modelSnapshot{
		models:           combined,
		apiKeys:          result.ApiKeys,
		origins:          result.Origins,
		providerDefaults: defaults,
		catalogRaw:       overlayRaw,
		loadError:        result.Error, // built-ins are kept even if custom models failed
	}
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
				resolvedHeaders := configpkg.ResolveHeaders(providerOverride.Headers)
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

	// Fan out per-account clones for any provider that has additional named
	// accounts. Each clone is presented under the account's composite slot id
	// so the OAuth ModifyModels pass injects that account's credentials/headers
	// and the selector groups it separately. Bedrock accounts additionally
	// apply per-account region (regional endpoint) and model-id/ARN overrides.
	for _, base := range result {
		for _, acct := range r.authStorage.AccountsForProvider(base.Provider) {
			if acct.AccountID == "" {
				continue // default account uses the base models as-is
			}
			clone := *base
			clone.Provider = acct.SlotKey
			if label := acct.DisplayName(); label != "" && label != "default" {
				clone.Name = base.Name + " (" + label + ")"
			}
			applyBedrockAccountOverrides(&clone, base, acct)
			result = append(result, &clone)
		}
	}

	return result
}

// applyBedrockAccountOverrides rewrites a cloned Amazon Bedrock model with an
// account's per-account region (regional endpoint) and model-id/ARN override.
// No-op for non-Bedrock providers or accounts without overrides.
func applyBedrockAccountOverrides(clone, base *ai.Model, acct auth.Account) {
	baseProvider, _ := auth.SplitSlot(acct.SlotKey)
	if baseProvider != "amazon-bedrock" {
		return
	}
	if region := auth.AccountRegion(acct.Extra); region != "" {
		clone.BaseURL = "https://bedrock-runtime." + region + ".amazonaws.com"
	}
	if override := auth.AccountModelOverride(acct.Extra, base.ID); override != "" {
		clone.ID = override
	}
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

// AddRuntimeModel registers an extra model in the registry at runtime.
// Used for env-driven additions (e.g. ANTHROPIC_MODEL pointing at a Bedrock
// ARN). If a model with the same provider+id already exists it is replaced.
// The model becomes visible in the /model selector and --list-models output.
func (r *ModelRegistry) AddRuntimeModel(m *ai.Model) {
	if m == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, existing := range r.models {
		if existing.Provider == m.Provider && existing.ID == m.ID {
			r.models[i] = m
			return
		}
	}
	r.models = append(r.models, m)
	r.invalidateSynthesisCache()
}

func (r *ModelRegistry) loadCustomModels(modelsJsonPath string, overlay *CatalogOverlay) *CustomModelsResult {
	// Layered load, lowest precedence first:
	//   1. the fir-dist catalog overlay (data-only model additions/fixes)
	//   2. the user's models.json
	//   3. every <agentDir>/models.d/*.json fragment, in lexical filename
	//      order (use NN- prefixes to control ordering)
	// Each layer is deep-merged on top of the previous one, so USER CONFIG
	// ALWAYS WINS over the fetched overlay — non-negotiable. Every layer has
	// the same shape and goes through the same mergeModelsConfig; see it for
	// the precise merge semantics.
	config := &ModelsConfig{}
	origins := make(map[string]string)

	if overlay != nil {
		mergeModelsConfig(config, overlay.modelsConfig())
		recordOrigins(origins, overlay.modelsConfig(), OriginOverlay)
	}

	if modelsJsonPath != "" {
		if data, err := os.ReadFile(modelsJsonPath); err == nil {
			var base ModelsConfig
			if err := json.Unmarshal(data, &base); err != nil {
				return emptyCustomModelsResult(
					fmt.Sprintf("Failed to parse models.json: %v\n\nFile: %s", err, modelsJsonPath),
				)
			}
			mergeModelsConfig(config, &base)
			recordOrigins(origins, &base, OriginUserModelsJSON)
		} else if !os.IsNotExist(err) {
			return emptyCustomModelsResult(
				fmt.Sprintf("Failed to load models.json: %v\n\nFile: %s", err, modelsJsonPath),
			)
		}

		fragDir := filepath.Join(filepath.Dir(modelsJsonPath), "models.d")
		fragments, _ := filepath.Glob(filepath.Join(fragDir, "*.json"))
		sort.Strings(fragments)
		for _, frag := range fragments {
			data, err := os.ReadFile(frag)
			if err != nil {
				return emptyCustomModelsResult(
					fmt.Sprintf("Failed to load models.d fragment: %v\n\nFile: %s", err, frag),
				)
			}
			var fragConfig ModelsConfig
			if err := json.Unmarshal(data, &fragConfig); err != nil {
				return emptyCustomModelsResult(
					fmt.Sprintf("Failed to parse models.d fragment: %v\n\nFile: %s", err, frag),
				)
			}
			if fragConfig.Providers == nil {
				return emptyCustomModelsResult(
					fmt.Sprintf("Invalid models.d fragment: missing \"providers\" field\n\nFile: %s", frag),
				)
			}
			mergeModelsConfig(config, &fragConfig)
			recordOrigins(origins, &fragConfig, OriginUserFragment+filepath.Base(frag))
		}
	}

	if config.Providers == nil {
		return emptyCustomModelsResult("")
	}

	// Validate the merged config. The overlay was already validated on its
	// own before it got here, so an error at this point is the user's.
	if err := validateModelsConfig(config); err != nil {
		return emptyCustomModelsResult(
			fmt.Sprintf("%v\n\nFile: %s", err, modelsJsonPath),
		)
	}

	overrides := make(map[string]*ProviderOverride)
	modelOverrides := make(map[string]map[string]*ModelOverride)
	apiKeys := make(map[string]string)

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
			apiKeys[providerName] = providerConfig.ApiKey
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

	models := r.parseModels(config, apiKeys)
	return &CustomModelsResult{
		Models:         models,
		Overrides:      overrides,
		ModelOverrides: modelOverrides,
		ApiKeys:        apiKeys,
		Origins:        origins,
	}
}

// recordOrigins stamps every model a layer defines or overrides with that
// layer's label. Later layers overwrite earlier ones, so the map always names
// the winning layer.
func recordOrigins(origins map[string]string, config *ModelsConfig, origin string) {
	for providerName, pc := range config.Providers {
		for _, m := range pc.Models {
			origins[originKey(providerName, m.ID)] = origin
		}
		for modelID := range pc.ModelOverrides {
			origins[originKey(providerName, modelID)] = origin
		}
	}
}

// originKey builds the modelOrigins map key. NUL-separated because model IDs
// routinely contain slashes (e.g. "openai/gpt-4o" on openrouter).
func originKey(provider, modelID string) string {
	return provider + "\x00" + modelID
}

// mergeModelsConfig deep-merges overlay into base (overlay wins). Providers are
// merged by name. Within a provider: scalar fields (baseUrl, apiKey, api) are
// overwritten when the overlay sets them non-empty; authHeader is overwritten
// when non-nil; header maps are key-merged; the models slice is concatenated
// then de-duplicated by id with the last writer winning (order preserved); and
// modelOverrides maps are key-merged.
func mergeModelsConfig(base, overlay *ModelsConfig) {
	if overlay == nil || overlay.Providers == nil {
		return
	}
	if base.Providers == nil {
		base.Providers = make(map[string]ProviderConfig)
	}
	for name, op := range overlay.Providers {
		bp, ok := base.Providers[name]
		if !ok {
			base.Providers[name] = op
			continue
		}
		if op.BaseURL != "" {
			bp.BaseURL = op.BaseURL
		}
		if op.ApiKey != "" {
			bp.ApiKey = op.ApiKey
		}
		if op.Api != "" {
			bp.Api = op.Api
		}
		if op.AuthHeader != nil {
			bp.AuthHeader = op.AuthHeader
		}
		if len(op.Headers) > 0 {
			if bp.Headers == nil {
				bp.Headers = make(map[string]string)
			}
			for k, v := range op.Headers {
				bp.Headers[k] = v
			}
		}
		if len(op.Models) > 0 {
			bp.Models = mergeModelDefs(bp.Models, op.Models)
		}
		if len(op.ModelOverrides) > 0 {
			if bp.ModelOverrides == nil {
				bp.ModelOverrides = make(map[string]ModelOverride)
			}
			for id, ov := range op.ModelOverrides {
				bp.ModelOverrides[id] = ov
			}
		}
		base.Providers[name] = bp
	}
}

// mergeModelDefs concatenates base and overlay model definitions, dropping any
// base entry whose id is redefined in overlay (last writer wins) while
// preserving order.
func mergeModelDefs(base, overlay []ModelDefinition) []ModelDefinition {
	overlayIDs := make(map[string]bool, len(overlay))
	for _, m := range overlay {
		overlayIDs[m.ID] = true
	}
	out := make([]ModelDefinition, 0, len(base)+len(overlay))
	for _, m := range base {
		if !overlayIDs[m.ID] {
			out = append(out, m)
		}
	}
	return append(out, overlay...)
}

func validateModelsConfig(config *ModelsConfig) error {
	// Built-in providers can define custom models without specifying baseUrl / apiKey / api;
	// those fields are inherited from the first built-in model for the provider.
	builtInProviders := make(map[string]bool, len(ai.GetProviders()))
	for _, p := range ai.GetProviders() {
		builtInProviders[string(p)] = true
	}

	for providerName, providerConfig := range config.Providers {
		isBuiltIn := builtInProviders[providerName]
		hasModels := len(providerConfig.Models) > 0
		hasModelOverrides := len(providerConfig.ModelOverrides) > 0

		if !hasModels {
			// Override-only config: needs baseUrl OR modelOverrides (or both)
			if providerConfig.BaseURL == "" && !hasModelOverrides {
				return fmt.Errorf("Provider %s: must specify \"baseUrl\", \"modelOverrides\", or \"models\"", providerName)
			}
		} else if !isBuiltIn {
			// Non-built-in providers with custom models require endpoint + auth.
			if providerConfig.BaseURL == "" {
				return fmt.Errorf("Provider %s: \"baseUrl\" is required when defining custom models", providerName)
			}
			if providerConfig.ApiKey == "" {
				return fmt.Errorf("Provider %s: \"apiKey\" is required when defining custom models", providerName)
			}
		}
		// Built-in providers with custom models: baseUrl/apiKey/api are optional,
		// inherited from the first built-in model for that provider.

		hasProviderApi := providerConfig.Api != ""
		for _, modelDef := range providerConfig.Models {
			hasModelApi := modelDef.Api != ""

			if !hasProviderApi && !hasModelApi && !isBuiltIn {
				return fmt.Errorf(
					"Provider %s, model %s: no \"api\" specified. Set at provider or model level",
					providerName, modelDef.ID,
				)
			}
			// For built-in providers, api is optional — inherited from built-in models.

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

func (r *ModelRegistry) parseModels(config *ModelsConfig, apiKeys map[string]string) []*ai.Model {
	var models []*ai.Model

	builtInProviders := make(map[string]bool, len(ai.GetProviders()))
	for _, p := range ai.GetProviders() {
		builtInProviders[string(p)] = true
	}

	// Cache built-in defaults (api, baseUrl) per provider, extracted from first model.
	builtInDefaults := make(map[string]struct {
		api     ai.Api
		baseURL string
	})
	getBuiltInDefaults := func(providerName string) (ai.Api, string, bool) {
		if !builtInProviders[providerName] {
			return "", "", false
		}
		if d, ok := builtInDefaults[providerName]; ok {
			return d.api, d.baseURL, true
		}
		builtIn := ai.GetModels(ai.Provider(providerName))
		if len(builtIn) == 0 {
			return "", "", false
		}
		d := struct {
			api     ai.Api
			baseURL string
		}{api: builtIn[0].API, baseURL: builtIn[0].BaseURL}
		builtInDefaults[providerName] = d
		return d.api, d.baseURL, true
	}

	for providerName, providerConfig := range config.Providers {
		if len(providerConfig.Models) == 0 {
			continue // Override-only, no custom models
		}

		// Store API key config for fallback resolver
		if providerConfig.ApiKey != "" {
			apiKeys[providerName] = providerConfig.ApiKey
		}

		defApi, defBaseURL, hasDefaults := getBuiltInDefaults(providerName)

		for _, modelDef := range providerConfig.Models {
			api := modelDef.Api
			if api == "" {
				api = providerConfig.Api
			}
			if api == "" && hasDefaults {
				api = defApi
			}
			if api == "" {
				continue
			}

			baseURL := firstNonEmpty(modelDef.BaseURL, providerConfig.BaseURL)
			if baseURL == "" && hasDefaults {
				baseURL = defBaseURL
			}
			if baseURL == "" {
				continue
			}

			// Merge headers: provider headers are base, model headers override
			providerHeaders := configpkg.ResolveHeaders(providerConfig.Headers)
			modelHeaders := configpkg.ResolveHeaders(modelDef.Headers)
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
				resolvedKey := configpkg.ResolveConfigValue(providerConfig.ApiKey)
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

			compaction := false
			if modelDef.Compaction != nil {
				compaction = *modelDef.Compaction
			}

			adaptiveThinking := false
			if modelDef.AdaptiveThinking != nil {
				adaptiveThinking = *modelDef.AdaptiveThinking
			}

			var sweScore float64
			if modelDef.SWEScore != nil {
				sweScore = *modelDef.SWEScore
			}

			models = append(models, &ai.Model{
				ID:                    modelDef.ID,
				Name:                  name,
				API:                   api,
				Provider:              providerName,
				BaseURL:               baseURL,
				Reasoning:             reasoning,
				ReasoningEffortValues: append([]string(nil), modelDef.ReasoningEffortValues...),
				Input:                 input,
				Cost:                  cost,
				ContextWindow:         contextWindow,
				MaxTokens:             maxTokens,
				Headers:               headers,
				Compat:                compat,
				ServerTools:           append([]string(nil), modelDef.ServerTools...),
				Compaction:            compaction,
				AdaptiveThinking:      adaptiveThinking,
				SWEScore:              sweScore,
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

// GetAvailable returns only models that have auth configured and are confirmed
// available by the provider's live model list (when available). It also
// surfaces live-list IDs that aren't in the built-in registry, with metadata
// synthesised from sibling models.
func (r *ModelRegistry) GetAvailable() []*ai.Model {
	// Built-in models confirmed live.
	r.mu.RLock()
	var result []*ai.Model
	for _, m := range r.models {
		if r.authStorage.HasAuth(m.Provider) && r.isModelLive(m.Provider, m.ID) {
			result = append(result, m)
		}
	}
	r.mu.RUnlock()

	// Live-only models not in the built-in registry (already populated by the
	// fetch path with full metadata).
	r.liveModelsMu.RLock()
	states := make([]*liveModelState, 0, len(r.liveModels))
	for prov, st := range r.liveModels {
		if r.authStorage.HasAuth(prov) {
			states = append(states, st)
		}
	}
	r.liveModelsMu.RUnlock()

	for _, st := range states {
		st.mu.RLock()
		result = append(result, st.models...)
		st.mu.RUnlock()
	}
	return result
}

// Find returns a model by provider and ID. Resolution order:
//  1. built-in registered models
//  2. synthesised model from the live-list state (cached or just-fetched)
//  3. on-the-fly synthesis using sibling-clone heuristic — only used when no
//     live-list state exists (e.g. cold start before fetch completes), so a
//     mistyped ID against an authed provider returns nil rather than a
//     phantom model.
func (r *ModelRegistry) Find(provider, modelID string) *ai.Model {
	r.mu.RLock()
	for _, m := range r.models {
		if m.Provider == provider && m.ID == modelID {
			r.mu.RUnlock()
			return m
		}
	}
	r.mu.RUnlock()

	// Consult live-list state. If a live-list exists for this provider, it's
	// authoritative: only IDs the provider confirmed exist will resolve.
	r.liveModelsMu.RLock()
	state, hasLive := r.liveModels[provider]
	r.liveModelsMu.RUnlock()
	if hasLive {
		if m := state.get(modelID); m != nil {
			return m
		}
		// Live-list exists but doesn't contain this ID. If the fetch has
		// produced data, the provider has spoken — return nil. If it hasn't
		// completed yet, fall through to synthesis so settings referencing a
		// previously-live model still resolve.
		if state.hasData() {
			return nil
		}
	}

	// Cold-start synthesis: settings may reference a previously-live model.
	return r.synthesise(provider, modelID)
}

// GetApiKey returns the API key for a model's provider.
func (r *ModelRegistry) GetApiKey(model *ai.Model) string {
	return r.authStorage.GetApiKey(model.Provider)
}

// HasConfiguredAuth returns true if the model has any configured authentication
// (either via auth storage or a custom provider API key config).
func (r *ModelRegistry) HasConfiguredAuth(model *ai.Model) bool {
	if r.authStorage.HasAuth(model.Provider) {
		return true
	}
	r.mu.RLock()
	_, hasCustomKey := r.customProviderApiKeys[model.Provider]
	r.mu.RUnlock()
	return hasCustomKey
}

// GetApiKeyForProvider returns the API key for a provider.
func (r *ModelRegistry) GetApiKeyForProvider(provider string) string {
	return r.authStorage.GetApiKey(provider)
}

// RefreshApiKeyForProvider re-reads auth.json from disk and then resolves the
// provider's API key. Use it on the request path (and after an auth rejection)
// so a credential rotated by another process — notably `fir auth refresh` from
// cron — is picked up instead of the revoked token this process still holds.
func (r *ModelRegistry) RefreshApiKeyForProvider(provider string) string {
	return r.authStorage.RefreshApiKey(provider)
}

// GetApiKeyError returns the last error encountered when resolving an API key
// for the given provider (e.g. an OAuth token refresh failure).
func (r *ModelRegistry) GetApiKeyError(provider string) error {
	return r.authStorage.GetApiKeyError(provider)
}

// IsUsingOAuth checks if a model is using OAuth credentials.
func (r *ModelRegistry) IsUsingOAuth(model *ai.Model) bool {
	cred := r.authStorage.Get(model.Provider)
	return cred != nil && cred.Type == auth.CredentialTypeOAuth
}

// AuthStorage returns the auth storage used by this registry.
func (r *ModelRegistry) AuthStorage() *auth.AuthStorage {
	return r.authStorage
}

// DefaultModelsJsonPath returns the default path for models.json.
func DefaultModelsJsonPath(agentDir string) string {
	return filepath.Join(agentDir, "models.json")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
