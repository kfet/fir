// Ported from: packages/coding-agent/src/core/model-resolver.ts
// Upstream hash: 036bde0a
package models

import (
	"fmt"
	"strings"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/config"
)

// DefaultModelPerProvider returns the default model ID for a provider, or ""
// if the provider isn't registered (or has no default).  Backed by the
// pkg/ai.RegisteredProvider registry; replaces a previous static map.
func DefaultModelPerProvider(p ai.Provider) string {
	r := ai.GetProviderRecord(p)
	if r == nil {
		return ""
	}
	return r.DefaultModelID
}

// OrderedProviders returns provider IDs in registry-defined preference order
// (lower Priority first, then ID).  Replaces the previous knownProviderOrder
// slice.
func OrderedProviders() []ai.Provider {
	records := ai.GetRegisteredProviders()
	out := make([]ai.Provider, 0, len(records))
	for _, r := range records {
		out = append(out, r.ID)
	}
	return out
}

// ParsedModelResult is the result of parsing a model pattern.
type ParsedModelResult struct {
	Model         *ai.Model
	ThinkingLevel string // empty if not explicitly set
	Warning       string
}

// InitialModelResult is the result of finding the initial model.
type InitialModelResult struct {
	Model           *ai.Model
	ThinkingLevel   string
	FallbackMessage string
}

// validThinkingLevels for pattern parsing.
var validThinkingLevels = map[string]bool{
	"off": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true,
}

func isValidThinkingLevel(s string) bool {
	return validThinkingLevels[strings.ToLower(s)]
}

// isAlias checks if a model ID looks like an alias (no date suffix).
func isAlias(id string) bool {
	if strings.HasSuffix(id, "-latest") {
		return true
	}
	// Check if ends with -YYYYMMDD
	if len(id) >= 9 {
		suffix := id[len(id)-9:]
		if suffix[0] == '-' {
			allDigits := true
			for _, c := range suffix[1:] {
				if c < '0' || c > '9' {
					allDigits = false
					break
				}
			}
			if allDigits {
				return false
			}
		}
	}
	return true
}

// tryMatchModel tries to match a pattern to a model from available models.
func tryMatchModel(pattern string, available []*ai.Model) *ai.Model {
	lp := strings.ToLower(pattern)

	// Check for provider/modelId format
	if idx := strings.Index(pattern, "/"); idx != -1 {
		prov := strings.ToLower(pattern[:idx])
		mid := strings.ToLower(pattern[idx+1:])
		for _, m := range available {
			if strings.ToLower(m.Provider) == prov && strings.ToLower(m.ID) == mid {
				return m
			}
		}
	}

	// Exact ID match (case-insensitive) — first match wins
	for _, m := range available {
		if strings.ToLower(m.ID) == lp {
			return m
		}
	}

	// Partial matching
	var matches []*ai.Model
	for _, m := range available {
		if strings.Contains(strings.ToLower(m.ID), lp) || strings.Contains(strings.ToLower(m.Name), lp) {
			matches = append(matches, m)
		}
	}

	if len(matches) == 0 {
		return nil
	}

	// Separate aliases and dated versions
	var aliases, dated []*ai.Model
	for _, m := range matches {
		if isAlias(m.ID) {
			aliases = append(aliases, m)
		} else {
			dated = append(dated, m)
		}
	}

	if len(aliases) > 0 {
		// Pick highest-sorting alias
		best := aliases[0]
		for _, m := range aliases[1:] {
			if m.ID > best.ID {
				best = m
			}
		}
		return best
	}

	// No alias, pick latest dated version
	best := dated[0]
	for _, m := range dated[1:] {
		if m.ID > best.ID {
			best = m
		}
	}
	return best
}

// ParseModelPattern parses a pattern to extract model and thinking level.
// Handles models with colons in their IDs (e.g., OpenRouter's :exacto suffix).
// When an invalid thinking-level suffix is encountered, this falls back to
// matching the prefix and emits a warning. For strict CLI parsing without
// fallback use parseModelPatternStrict.
func ParseModelPattern(pattern string, available []*ai.Model) ParsedModelResult {
	return parseModelPatternInner(pattern, available, true)
}

// parseModelPatternStrict is like ParseModelPattern but returns an empty result
// (model not found) if an invalid thinking-level suffix is encountered, rather
// than falling back to the prefix match. Used by ResolveCliModel.
func parseModelPatternStrict(pattern string, available []*ai.Model) ParsedModelResult {
	return parseModelPatternInner(pattern, available, false)
}

func parseModelPatternInner(pattern string, available []*ai.Model, allowFallback bool) ParsedModelResult {
	// Try exact match first
	if m := tryMatchModel(pattern, available); m != nil {
		return ParsedModelResult{Model: m}
	}

	// Try splitting on last colon
	lastColon := strings.LastIndex(pattern, ":")
	if lastColon == -1 {
		return ParsedModelResult{}
	}

	prefix := pattern[:lastColon]
	suffix := pattern[lastColon+1:]

	if isValidThinkingLevel(suffix) {
		result := parseModelPatternInner(prefix, available, allowFallback)
		if result.Model != nil {
			if result.Warning == "" {
				result.ThinkingLevel = strings.ToLower(suffix)
			}
			return result
		}
		return result
	}

	if !allowFallback {
		// Strict mode: treat the suffix as part of the model ID and fail.
		// This avoids accidentally resolving to a different model.
		return ParsedModelResult{}
	}

	// Fallback mode: recurse on prefix and warn.
	result := parseModelPatternInner(prefix, available, allowFallback)
	if result.Model != nil {
		return ParsedModelResult{
			Model:   result.Model,
			Warning: fmt.Sprintf("Invalid thinking level %q in pattern %q. Using default instead.", suffix, pattern),
		}
	}
	return result
}

// ResolveCliModelResult is the result of ResolveCliModel.
type ResolveCliModelResult struct {
	Model         *ai.Model
	ThinkingLevel string
	Warning       string
	// Error is set (and Model is nil) when the model could not be found.
	Error string
}

// ResolveCliModelOptions configures ResolveCliModel.
type ResolveCliModelOptions struct {
	CLIProvider   string
	CLIModel      string
	ModelRegistry *ModelRegistry
}

// ResolveCliModel resolves a single model from CLI flags.
//
// Supports:
//   - --provider <provider> --model <pattern>
//   - --model <provider>/<pattern>
//   - Fuzzy matching (exact id, then partial id/name)
//
// Returns an empty result (no error) when CLIModel is not set.
func ResolveCliModel(opts ResolveCliModelOptions) ResolveCliModelResult {
	if opts.CLIModel == "" {
		return ResolveCliModelResult{}
	}

	// Use all models — not just those with pre-configured auth — so that
	// --api-key can be used for first-time setup.
	allModels := opts.ModelRegistry.GetAll()
	if len(allModels) == 0 {
		return ResolveCliModelResult{
			Error: "No models available. Check your installation or add models to models.json.",
		}
	}

	// Build case-insensitive provider lookup.
	providerMap := make(map[string]string)
	for _, m := range allModels {
		providerMap[strings.ToLower(m.Provider)] = m.Provider
	}

	var provider string
	if opts.CLIProvider != "" {
		p, ok := providerMap[strings.ToLower(opts.CLIProvider)]
		if !ok {
			return ResolveCliModelResult{
				Error: fmt.Sprintf("Unknown provider %q. Use --list-models to see available providers/models.", opts.CLIProvider),
			}
		}
		provider = p
	}

	pattern := opts.CLIModel
	inferredProvider := false

	// If no explicit --provider, try to interpret "provider/model" format first.
	// When the prefix before the first slash matches a known provider, prefer that
	// interpretation over matching models whose IDs literally contain slashes
	// (e.g. "zai/glm-5" → provider=zai, model=glm-5, not a gateway model with id "zai/glm-5").
	if provider == "" {
		if idx := strings.Index(opts.CLIModel, "/"); idx != -1 {
			maybeProvider := opts.CLIModel[:idx]
			if canonical, ok := providerMap[strings.ToLower(maybeProvider)]; ok {
				provider = canonical
				pattern = opts.CLIModel[idx+1:]
				inferredProvider = true
			}
		}
	}

	// If no provider was inferred from the slash, try exact matches without provider inference.
	// This handles models whose IDs naturally contain slashes (e.g. OpenRouter-style IDs).
	if provider == "" {
		lower := strings.ToLower(opts.CLIModel)
		for _, m := range allModels {
			if strings.ToLower(m.ID) == lower || strings.ToLower(m.Provider+"/"+m.ID) == lower {
				return ResolveCliModelResult{Model: m}
			}
		}
	}

	if opts.CLIProvider != "" && provider != "" {
		// If both --provider and --model were given, tolerate --model <provider>/<pattern>
		// by stripping the provider prefix.
		prefix := provider + "/"
		if strings.EqualFold(opts.CLIModel[:min(len(opts.CLIModel), len(prefix))], prefix) {
			pattern = opts.CLIModel[len(prefix):]
		}
	}

	var candidates []*ai.Model
	if provider != "" {
		for _, m := range allModels {
			if m.Provider == provider {
				candidates = append(candidates, m)
			}
		}
	} else {
		candidates = allModels
	}

	res := parseModelPatternStrict(pattern, candidates)
	if res.Model != nil {
		return ResolveCliModelResult{
			Model:         res.Model,
			ThinkingLevel: res.ThinkingLevel,
			Warning:       res.Warning,
		}
	}

	// If we inferred a provider from the slash but found no match within that provider,
	// fall back to matching the full input as a raw model id across all models.
	// This handles OpenRouter-style IDs like "openai/gpt-4o:extended" where "openai"
	// looks like a provider but the full string is actually a model id on openrouter.
	if inferredProvider {
		lower := strings.ToLower(opts.CLIModel)
		for _, m := range allModels {
			if strings.ToLower(m.ID) == lower || strings.ToLower(m.Provider+"/"+m.ID) == lower {
				return ResolveCliModelResult{Model: m}
			}
		}
		// Also try parseModelPattern on the full input against all models.
		fallback := parseModelPatternStrict(opts.CLIModel, allModels)
		if fallback.Model != nil {
			return ResolveCliModelResult{
				Model:         fallback.Model,
				ThinkingLevel: fallback.ThinkingLevel,
				Warning:       fallback.Warning,
			}
		}
	}

	if provider != "" {
		fallbackModel := buildFallbackModel(provider, pattern, allModels)
		if fallbackModel != nil {
			rec := ai.GetProviderRecord(ai.Provider(provider))
			// Provider claims this ID shape (e.g. bedrock ARN inference
			// profiles) — pass it through silently rather than warning.
			if rec != nil && providerClaimsModelID(rec, pattern) {
				return ResolveCliModelResult{Model: fallbackModel}
			}
			// Provider refuses fuzzy matching (e.g. Poe's bot catalogue is
			// exhaustive — an unknown id almost always means a typo).
			if rec != nil && rec.RefuseFuzzyMatch {
				return ResolveCliModelResult{
					Warning: res.Warning,
					Error:   fmt.Sprintf("Model %q not found for provider %q. Use --list-models to see available models.", pattern, provider),
				}
			}
			fallbackWarning := fmt.Sprintf("Model %q not found for provider %q. Using custom model id.", pattern, provider)
			if res.Warning != "" {
				fallbackWarning = res.Warning + " " + fallbackWarning
			}
			return ResolveCliModelResult{
				Model:   fallbackModel,
				Warning: fallbackWarning,
			}
		}
	}

	display := opts.CLIModel
	if provider != "" {
		display = provider + "/" + pattern
	}
	return ResolveCliModelResult{
		Warning: res.Warning,
		Error:   fmt.Sprintf("Model %q not found. Use --list-models to see available models.", display),
	}
}

// providerClaimsModelID reports whether any of the registered ClaimsModelIDGlobs
// match the given model id.  Replaces the previous inline isBedrockPassthroughID
// special case.
func providerClaimsModelID(rec *ai.RegisteredProvider, modelID string) bool {
	if rec == nil {
		return false
	}
	for _, g := range rec.ClaimsModelIDGlobs {
		if globMatch(modelID, g) {
			return true
		}
	}
	return false
}

// buildFallbackModel creates a model with a custom ID when the exact model
// isn't found but we know the provider. Copies settings from the provider's
// default model (or first model) and overrides the ID.
func buildFallbackModel(provider, modelID string, availableModels []*ai.Model) *ai.Model {
	var providerModels []*ai.Model
	for _, m := range availableModels {
		if m.Provider == provider {
			providerModels = append(providerModels, m)
		}
	}
	if len(providerModels) == 0 {
		return nil
	}

	defaultID := DefaultModelPerProvider(ai.Provider(provider))
	var baseModel *ai.Model
	if defaultID != "" {
		for _, m := range providerModels {
			if m.ID == defaultID {
				baseModel = m
				break
			}
		}
	}
	if baseModel == nil {
		baseModel = providerModels[0]
	}

	clone := *baseModel
	clone.ID = modelID
	clone.Name = modelID
	return &clone
}

// FindInitialModel finds the initial model based on priority.
func FindInitialModel(opts FindInitialModelOptions) InitialModelResult {
	defaultTL := string(config.DefaultThinkingLevel)
	if opts.DefaultThinkingLevel != "" {
		defaultTL = opts.DefaultThinkingLevel
	}

	// 1. CLI args
	if opts.CLIProvider != "" && opts.CLIModel != "" {
		resolved := ResolveCliModel(ResolveCliModelOptions{
			CLIProvider:   opts.CLIProvider,
			CLIModel:      opts.CLIModel,
			ModelRegistry: opts.ModelRegistry,
		})
		if resolved.Error != "" {
			return InitialModelResult{ThinkingLevel: defaultTL}
		}
		if resolved.Model != nil {
			return InitialModelResult{Model: resolved.Model, ThinkingLevel: defaultTL}
		}
	}

	// 2. Saved default from settings
	if opts.DefaultProvider != "" && opts.DefaultModelID != "" {
		found := opts.ModelRegistry.Find(opts.DefaultProvider, opts.DefaultModelID)
		if found != nil {
			return InitialModelResult{Model: found, ThinkingLevel: defaultTL}
		}
	}

	// 3. First available model with valid API key
	available := opts.ModelRegistry.GetAvailable()
	if len(available) > 0 {
		// Try default model per provider
		for _, prov := range OrderedProviders() {
			defaultID := DefaultModelPerProvider(prov)
			if defaultID == "" {
				continue
			}
			for _, m := range available {
				if m.Provider == prov && m.ID == defaultID {
					return InitialModelResult{Model: m, ThinkingLevel: defaultTL}
				}
			}
		}
		return InitialModelResult{Model: available[0], ThinkingLevel: defaultTL}
	}

	return InitialModelResult{ThinkingLevel: defaultTL}
}

// FindInitialModelOptions configures FindInitialModel.
type FindInitialModelOptions struct {
	CLIProvider          string
	CLIModel             string
	IsContinuing         bool
	DefaultProvider      string
	DefaultModelID       string
	DefaultThinkingLevel string
	ModelRegistry        *ModelRegistry
}

// RestoreModelFromSession restores a model from session, with fallback.
func RestoreModelFromSession(savedProvider, savedModelID string, currentModel *ai.Model, registry *ModelRegistry) (*ai.Model, string) {
	restored := registry.Find(savedProvider, savedModelID)

	if restored != nil {
		if registry.HasConfiguredAuth(restored) {
			return restored, ""
		}
	}

	reason := "model no longer exists"
	if restored != nil {
		reason = "no auth configured"
	}

	if currentModel != nil {
		msg := fmt.Sprintf("Could not restore model %s/%s (%s). Using %s/%s.",
			savedProvider, savedModelID, reason, currentModel.Provider, currentModel.ID)
		return currentModel, msg
	}

	// Try any available model
	available := registry.GetAvailable()
	if len(available) > 0 {
		for _, prov := range OrderedProviders() {
			defaultID := DefaultModelPerProvider(prov)
			for _, m := range available {
				if m.Provider == prov && m.ID == defaultID {
					msg := fmt.Sprintf("Could not restore model %s/%s (%s). Using %s/%s.",
						savedProvider, savedModelID, reason, m.Provider, m.ID)
					return m, msg
				}
			}
		}
		fallback := available[0]
		msg := fmt.Sprintf("Could not restore model %s/%s (%s). Using %s/%s.",
			savedProvider, savedModelID, reason, fallback.Provider, fallback.ID)
		return fallback, msg
	}

	return nil, ""
}

// --- helpers ---

// knownProviderOrder is now derived from the ai.RegisteredProvider registry
// at call time; see OrderedProviders() above.

// globMatch is a simple glob matcher supporting * and ?.
func globMatch(text, pattern string) bool {
	return globMatchImpl(strings.ToLower(text), strings.ToLower(pattern))
}

func globMatchImpl(text, pattern string) bool {
	if pattern == "*" {
		return true
	}

	ti, pi := 0, 0
	starTi, starPi := -1, -1

	for ti < len(text) {
		if pi < len(pattern) && (pattern[pi] == '?' || pattern[pi] == text[ti]) {
			ti++
			pi++
		} else if pi < len(pattern) && pattern[pi] == '*' {
			starTi = ti
			starPi = pi
			pi++
		} else if starPi >= 0 {
			starTi++
			ti = starTi
			pi = starPi + 1
		} else {
			return false
		}
	}

	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}

	return pi == len(pattern)
}
