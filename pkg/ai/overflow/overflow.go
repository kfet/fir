// Ported from: packages/ai/src/utils/overflow.ts
// Upstream hash: c99b9940
package overflow

import (
	"regexp"

	"github.com/kfet/fir/pkg/ai"
)

// overflowPatterns are regex patterns to detect context overflow errors.
var overflowPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)prompt is too long`),                     // Anthropic
	regexp.MustCompile(`(?i)input is too long for requested model`),  // Amazon Bedrock
	regexp.MustCompile(`(?i)exceeds the context window`),             // OpenAI
	regexp.MustCompile(`(?i)input token count.*exceeds the maximum`), // Google
	regexp.MustCompile(`(?i)maximum prompt length is \d+`),           // xAI
	regexp.MustCompile(`(?i)reduce the length of the messages`),      // Groq
	regexp.MustCompile(`(?i)maximum context length is \d+ tokens`),   // OpenRouter
	regexp.MustCompile(`(?i)exceeds the limit of \d+`),               // GitHub Copilot
	regexp.MustCompile(`(?i)exceeds the available context size`),     // llama.cpp
	regexp.MustCompile(`(?i)greater than the context length`),        // LM Studio
	regexp.MustCompile(`(?i)context window exceeds limit`),           // MiniMax
	regexp.MustCompile(`(?i)exceeded model token limit`),                                       // Kimi
	regexp.MustCompile(`(?i)too large for model with \d+ maximum context length`),               // Mistral
	regexp.MustCompile(`(?i)context[_ ]length[_ ]exceeded`),                                     // Generic
	regexp.MustCompile(`(?i)too many tokens`),                        // Generic
	regexp.MustCompile(`(?i)token limit exceeded`),                   // Generic
}

var statusCodePattern = regexp.MustCompile(`(?i)^4(00|13)\s*(status code)?\s*\(no body\)`)

// IsContextOverflow checks if an assistant message represents a context overflow error.
func IsContextOverflow(message *ai.AssistantMessage, contextWindow int) bool {
	if message == nil {
		return false
	}

	// Case 1: Error-based overflow
	if message.StopReason == ai.StopReasonError && message.ErrorMessage != "" {
		for _, p := range overflowPatterns {
			if p.MatchString(message.ErrorMessage) {
				return true
			}
		}
		if statusCodePattern.MatchString(message.ErrorMessage) {
			return true
		}
	}

	// Case 2: Silent overflow
	if contextWindow > 0 && message.StopReason == ai.StopReasonStop {
		inputTokens := message.Usage.Input + message.Usage.CacheRead
		if inputTokens > contextWindow {
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
