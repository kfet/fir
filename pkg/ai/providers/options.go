// Ported from: packages/ai/src/providers/simple-options.ts
// Upstream hash: 1caadb2e
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
		maxTokens := model.MaxTokens
		if maxTokens > 32000 {
			maxTokens = 32000
		}
		return &ai.StreamOptions{
			MaxTokens: &maxTokens,
			ApiKey:    apiKey,
		}
	}

	maxTokens := options.MaxTokens
	if maxTokens == nil {
		mt := model.MaxTokens
		if mt > 32000 {
			mt = 32000
		}
		maxTokens = &mt
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
		MaxRetryDelayMs: options.MaxRetryDelayMs,
		ServerTools:     options.ServerTools,
		Compaction:      options.Compaction,
	}
}

// ClampReasoning clamps "xhigh" to "high".
func ClampReasoning(level ai.ThinkingLevel) ai.ThinkingLevel {
	if level == ai.ThinkingXHigh {
		return ai.ThinkingHigh
	}
	return level
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
