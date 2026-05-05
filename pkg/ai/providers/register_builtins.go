// Ported from: packages/ai/src/providers/register-builtins.ts
// Upstream hash: 41039e8d
package providers

import "github.com/kfet/fir/pkg/ai"

// RegisterBuiltInApiProviders registers all built-in API providers in the given registry.
func RegisterBuiltInApiProviders(r *ai.Registry) {
	RegisterAnthropic(r)
	RegisterOpenAICompletions(r)
	RegisterOpenAIResponses(r)
	RegisterGoogle(r)
	RegisterBedrock(r)
	RegisterAzureOpenAIResponses(r)
	RegisterOpenAICodexResponses(r)
	RegisterGoogleVertex(r)
	// Other Apis (Cloud-Code-Assist family etc.) ship from builtin
	// extensions via fir_ext.register_api(...); their kind handlers
	// live alongside this file (e.g. extkind_declgoogle.go).
}

// RegisterDefaultProviders is a convenience that registers built-in providers
// in the default registry.
func RegisterDefaultProviders() {
	RegisterBuiltInApiProviders(ai.DefaultRegistry)
	ai.DefaultRegistry.SetBuiltInRegistrar(func(r *ai.Registry) {
		RegisterBuiltInApiProviders(r)
	})
}
