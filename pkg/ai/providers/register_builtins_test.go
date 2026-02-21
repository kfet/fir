// Ported from: packages/ai/src/providers/register-builtins.ts
// Upstream hash: 1caadb2e
package providers

import (
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

func TestRegisterBuiltInApiProviders(t *testing.T) {
	r := ai.NewRegistry()
	RegisterBuiltInApiProviders(r)

	apis := []ai.Api{
		ai.ApiAnthropicMessages,
		ai.ApiOpenAICompletions,
		ai.ApiOpenAIResponses,
		ai.ApiGoogleGenerativeAI,
		ai.ApiBedrockConverseStream,
	}

	for _, api := range apis {
		p := r.GetApiProvider(api)
		if p == nil {
			t.Errorf("expected provider for %s", api)
			continue
		}
		if p.Stream == nil {
			t.Errorf("expected Stream for %s", api)
		}
		if p.StreamSimple == nil {
			t.Errorf("expected StreamSimple for %s", api)
		}
	}
}

func TestRegisterDefaultProviders(t *testing.T) {
	// Reset default registry
	old := ai.DefaultRegistry
	ai.DefaultRegistry = ai.NewRegistry()
	defer func() { ai.DefaultRegistry = old }()

	RegisterDefaultProviders()

	p := ai.DefaultRegistry.GetApiProvider(ai.ApiAnthropicMessages)
	if p == nil {
		t.Error("expected anthropic provider in default registry")
	}
}
