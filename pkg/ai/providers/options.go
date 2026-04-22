// Ported from: packages/ai/src/providers/simple-options.ts
// Upstream hash: 036bde0a
package providers

import (
	"fmt"

	"github.com/kfet/fir/pkg/ai"
)

// noAPIKeyError builds a descriptive error message when no API key is available.
// apiKeyError is an optional detail string (e.g. from StreamOptions.ApiKeyError).
func noAPIKeyError(provider string, apiKeyError string) string {
	if apiKeyError != "" {
		return fmt.Sprintf("no API key for provider %q: %s", provider, apiKeyError)
	}
	return fmt.Sprintf("no API key for provider %q. Set an API key or run 'fir login %s'", provider, provider)
}

// apiKeyErrorFromOpts extracts ApiKeyError from options, handling nil.
func apiKeyErrorFromOpts(options *ai.StreamOptions) string {
	if options != nil {
		return options.ApiKeyError
	}
	return ""
}

// apiKeyErrorFromSimpleOpts extracts ApiKeyError from simple options, handling nil.
func apiKeyErrorFromSimpleOpts(options *ai.SimpleStreamOptions) string {
	if options != nil {
		return options.ApiKeyError
	}
	return ""
}

// BuildBaseOptions constructs base StreamOptions from SimpleStreamOptions.
func BuildBaseOptions(model *ai.Model, options *ai.SimpleStreamOptions, apiKey string) *ai.StreamOptions {
	if options == nil {
		var maxTokens *int
		if model.MaxTokens > 0 {
			mt := model.MaxTokens
			if mt > 32000 {
				mt = 32000
			}
			maxTokens = &mt
		}
		return &ai.StreamOptions{
			MaxTokens: maxTokens,
			ApiKey:    apiKey,
		}
	}

	maxTokens := options.MaxTokens
	if maxTokens == nil {
		if model.MaxTokens > 0 {
			mt := model.MaxTokens
			if mt > 32000 {
				mt = 32000
			}
			maxTokens = &mt
		}
	}

	key := apiKey
	if key == "" {
		key = options.ApiKey
	}

	return &ai.StreamOptions{
		Temperature:     options.Temperature,
		MaxTokens:       maxTokens,
		ApiKey:          key,
		ApiKeyError:     options.ApiKeyError,
		CacheRetention:  options.CacheRetention,
		SessionID:       options.SessionID,
		Headers:         options.Headers,
		TimeoutMs:       options.TimeoutMs,
		MaxRetries:      options.MaxRetries,
		MaxRetryDelayMs: options.MaxRetryDelayMs,
		ServerTools:     options.ServerTools,
		Compaction:      options.Compaction,
		RefreshApiKey:   options.RefreshApiKey,
		OnPayload:       options.OnPayload,
		OnResponse:      options.OnResponse,
		OnRetry:         options.OnRetry,
		Transport:       options.Transport,
		ReasoningEffort: options.ReasoningEffort,
		ToolChoice:      options.ToolChoice,
		Metadata:        options.Metadata,
	}
}

// ClampReasoning clamps "xhigh" and "max" down to "high". This is the
// safe fallback for providers/models that don't support the higher tiers.
// Model-aware callers should prefer ClampReasoningForModel so that models
// which do support xhigh or max keep the requested level.
func ClampReasoning(level ai.ThinkingLevel) ai.ThinkingLevel {
	if level == ai.ThinkingXHigh || level == ai.ThinkingMax {
		return ai.ThinkingHigh
	}
	return level
}

// ClampReasoningForModel clamps a requested reasoning level to the highest
// tier the model actually supports:
//   - "max"   -> kept if the model supports max; otherwise clamped to
//     xhigh (if supported) or high.
//   - "xhigh" -> kept if the model supports xhigh; otherwise clamped to
//     high.
//   - other   -> returned as-is.
func ClampReasoningForModel(level ai.ThinkingLevel, model *ai.Model) ai.ThinkingLevel {
	switch level {
	case ai.ThinkingMax:
		if ai.SupportsMax(model) {
			return ai.ThinkingMax
		}
		if ai.SupportsXhigh(model) {
			return ai.ThinkingXHigh
		}
		return ai.ThinkingHigh
	case ai.ThinkingXHigh:
		if ai.SupportsXhigh(model) {
			return ai.ThinkingXHigh
		}
		return ai.ThinkingHigh
	default:
		return level
	}
}

// clampEffortToEnum returns effort unchanged if it is in allowed, otherwise
// picks the nearest allowed neighbour on fir's canonical ladder
// (none < minimal < low < medium < high < xhigh < max). Also tolerates a few
// common synonyms seen in upstream catalogs ("extra-high" ≡ "xhigh").
// If allowed is empty, effort is returned as-is.
func clampEffortToEnum(effort string, allowed []string) string {
	if len(allowed) == 0 || effort == "" {
		return effort
	}
	ladder := []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
	rank := func(s string) int {
		if s == "extra-high" {
			s = "xhigh"
		}
		for i, v := range ladder {
			if v == s {
				return i
			}
		}
		return -1
	}
	// allowed contains exact match?
	for _, v := range allowed {
		if v == effort {
			return effort
		}
	}
	want := rank(effort)
	if want < 0 {
		return allowed[0] // unknown — fall back to first advertised value
	}
	best := ""
	bestDist := 0
	bestRank := -1
	for _, v := range allowed {
		r := rank(v)
		if r < 0 {
			continue
		}
		d := r - want
		if d < 0 {
			d = -d
		}
		// On ties, prefer the higher rung — a user who asked for more
		// reasoning effort than the enum offers gets the highest
		// available; likewise ties between a rung below and above the
		// request round up.
		if best == "" || d < bestDist || (d == bestDist && r > bestRank) {
			best, bestDist, bestRank = v, d, r
		}
	}
	if best == "" {
		return allowed[0]
	}
	return best
}

// AdjustMaxTokensForThinking adjusts max tokens and calculates thinking budget.
func AdjustMaxTokensForThinking(
	baseMaxTokens int,
	modelMaxTokens int,
	reasoningLevel ai.ThinkingLevel,
	customBudgets *ai.ThinkingBudgets,
) (maxTokens int, thinkingBudget int) {
	defaultBudgets := map[ai.ThinkingLevel]int{
		ai.ThinkingMinimal: 1024,
		ai.ThinkingLow:     2048,
		ai.ThinkingMedium:  8192,
		ai.ThinkingHigh:    16384,
	}

	if customBudgets != nil {
		if customBudgets.Minimal != nil {
			defaultBudgets[ai.ThinkingMinimal] = *customBudgets.Minimal
		}
		if customBudgets.Low != nil {
			defaultBudgets[ai.ThinkingLow] = *customBudgets.Low
		}
		if customBudgets.Medium != nil {
			defaultBudgets[ai.ThinkingMedium] = *customBudgets.Medium
		}
		if customBudgets.High != nil {
			defaultBudgets[ai.ThinkingHigh] = *customBudgets.High
		}
	}

	minOutputTokens := 1024
	level := ClampReasoning(reasoningLevel)
	thinkingBudget = defaultBudgets[level]

	maxTokens = baseMaxTokens + thinkingBudget
	if maxTokens > modelMaxTokens {
		maxTokens = modelMaxTokens
	}

	if maxTokens <= thinkingBudget {
		thinkingBudget = maxTokens - minOutputTokens
		if thinkingBudget < 0 {
			thinkingBudget = 0
		}
	}

	return maxTokens, thinkingBudget
}
