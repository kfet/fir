// Ported from: packages/ai/src/utils/overflow.ts
// Upstream hash: 036bde0a
package overflow

import (
	"regexp"

	"github.com/kfet/fir/pkg/ai"
)

// overflowPatterns are regex patterns to detect context overflow errors.
var overflowPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)prompt is too long`),                                  // Anthropic token overflow
	regexp.MustCompile(`(?i)request_too_large`),                                   // Anthropic request byte-size overflow (HTTP 413)
	regexp.MustCompile(`(?i)input is too long for requested model`),               // Amazon Bedrock
	regexp.MustCompile(`(?i)exceeds the context window`),                          // OpenAI
	regexp.MustCompile(`(?i)input token count.*exceeds the maximum`),              // Google
	regexp.MustCompile(`(?i)maximum prompt length is \d+`),                        // xAI
	regexp.MustCompile(`(?i)reduce the length of the messages`),                   // Groq
	regexp.MustCompile(`(?i)maximum context length is \d+ tokens`),                // OpenRouter
	regexp.MustCompile(`(?i)exceeds the limit of \d+`),                            // GitHub Copilot
	regexp.MustCompile(`(?i)exceeds the available context size`),                  // llama.cpp
	regexp.MustCompile(`(?i)greater than the context length`),                     // LM Studio
	regexp.MustCompile(`(?i)context window exceeds limit`),                        // MiniMax
	regexp.MustCompile(`(?i)exceeded model token limit`),                          // Kimi
	regexp.MustCompile(`(?i)too large for model with \d+ maximum context length`), // Mistral
	regexp.MustCompile(`(?i)prompt too long; exceeded (?:max )?context length`),   // Ollama explicit overflow error
	regexp.MustCompile(`(?i)model_context_window_exceeded`),                       // z.ai non-standard finish_reason surfaced as error text
	regexp.MustCompile(`(?i)context[_ ]length[_ ]exceeded`),                       // Generic
	regexp.MustCompile(`(?i)too many tokens`),                                     // Generic
	regexp.MustCompile(`(?i)token limit exceeded`),                                // Generic
	regexp.MustCompile(`(?i)^4(?:00|13)\s*(?:status code)?\s*\(no body\)`),        // Cerebras: 400/413 with no body
}

// nonOverflowPatterns indicate non-overflow errors (rate limiting, server errors).
// Messages matching any of these are excluded from overflow detection even if
// they also match an overflowPatterns entry.
var nonOverflowPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^(Throttling error|Service unavailable):`), // AWS Bedrock non-overflow
	regexp.MustCompile(`(?i)rate limit`),                               // Generic rate limiting
	regexp.MustCompile(`(?i)too many requests`),                        // Generic HTTP 429
}

// IsContextOverflow checks if an assistant message represents a context overflow error.
func IsContextOverflow(message *ai.AssistantMessage, contextWindow int) bool {
	if message == nil {
		return false
	}

	// Case 1: Error-based overflow
	if message.StopReason == ai.StopReasonError && message.ErrorMessage != "" {
		// Skip messages matching known non-overflow patterns (e.g. throttling).
		isNonOverflow := false
		for _, p := range nonOverflowPatterns {
			if p.MatchString(message.ErrorMessage) {
				isNonOverflow = true
				break
			}
		}
		if !isNonOverflow {
			for _, p := range overflowPatterns {
				if p.MatchString(message.ErrorMessage) {
					return true
				}
			}
		}
	}

	// Case 2: Silent overflow
	if contextWindow > 0 && message.StopReason == ai.StopReasonStop {
		inputTokens := message.Usage.Input + message.Usage.CacheRead
		if inputTokens > contextWindow {
			return true
		}
	}

	// Case 3: Length-stop overflow (Xiaomi MiMo style) — server truncates oversized input
	// to fit the context window, leaving no room for output. Returns stopReason "length"
	// with output=0 and input+cacheRead filling the context window.
	if contextWindow > 0 && message.StopReason == ai.StopReasonLength && message.Usage.Output == 0 {
		inputTokens := message.Usage.Input + message.Usage.CacheRead
		if float64(inputTokens) >= float64(contextWindow)*0.99 {
			return true
		}
	}

	return false
}

// GetOverflowPatterns returns a copy of the overflow patterns (for testing).
func GetOverflowPatterns() []*regexp.Regexp {
	result := make([]*regexp.Regexp, len(overflowPatterns))
	copy(result, overflowPatterns)
	return result
}
