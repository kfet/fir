// Ported from: packages/ai/src/providers/register-builtins.ts
// Upstream hash: 1caadb2e
package providers

import "github.com/kfet/pi-go/pkg/ai"

// RegisterBuiltInApiProviders registers all built-in API providers in the given registry.
func RegisterBuiltInApiProviders(r *ai.Registry) {
	RegisterAnthropic(r)
	RegisterOpenAICompletions(r)
	RegisterOpenAIResponses(r)
	RegisterGoogle(r)
	RegisterBedrock(r)
	// Azure OpenAI Responses, Codex, Vertex, Gemini CLI — not yet ported
}

// RegisterDefaultProviders is a convenience that registers built-in providers
// in the default registry.
func RegisterDefaultProviders() {
	RegisterBuiltInApiProviders(ai.DefaultRegistry)
}
