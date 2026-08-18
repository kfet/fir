// Ported from: packages/ai/src/models.ts
// Upstream hash: 1caadb2e
package ai

import (
	"math"
	"testing"
)

// assertGetModelRoundTrip exercises GetModel for an arbitrary registered model
// of the given provider and asserts the structural invariants that must hold
// regardless of which exact (churnable, often dated) model IDs the catalog
// currently ships. It deliberately does NOT pin any single model ID — those get
// dropped on every `make generate-models` refresh, which is what made the old
// tests flaky. wantAPI is the API every model of this provider must use.
func assertGetModelRoundTrip(t *testing.T, provider Provider, wantAPI Api) {
	t.Helper()
	models := GetModels(provider)
	if len(models) == 0 {
		t.Fatalf("expected at least one registered model for provider %q", provider)
	}
	for _, want := range models {
		// Round-trip: the model returned by GetModels must be retrievable by ID.
		m := GetModel(provider, want.ID)
		if m == nil {
			t.Fatalf("GetModel(%q, %q) returned nil for a registered model", provider, want.ID)
		}
		if m.ID != want.ID {
			t.Errorf("expected ID %q, got %q", want.ID, m.ID)
		}
		if m.Provider != provider {
			t.Errorf("model %q: expected provider %q, got %q", m.ID, provider, m.Provider)
		}
		if m.API != wantAPI {
			t.Errorf("model %q: expected api %q, got %q", m.ID, wantAPI, m.API)
		}
		if m.ContextWindow <= 0 {
			t.Errorf("model %q: expected positive context window, got %d", m.ID, m.ContextWindow)
		}
		if m.MaxTokens <= 0 {
			t.Errorf("model %q: expected positive max tokens, got %d", m.ID, m.MaxTokens)
		}
	}
}

func TestGetModel_Anthropic(t *testing.T) {
	assertGetModelRoundTrip(t, ProviderAnthropic, ApiAnthropicMessages)
}

func TestGetModel_OpenAI(t *testing.T) {
	assertGetModelRoundTrip(t, ProviderOpenAI, ApiOpenAIResponses)
}

func TestGetModel_NotFound(t *testing.T) {
	m := GetModel(ProviderAnthropic, "nonexistent-model")
	if m != nil {
		t.Error("expected nil for nonexistent model")
	}
}

// TestGetModel_PoeAnthropicBaseURL guards against a regression where Poe's
// BaseURL kept its "/v1" suffix while the model routed through
// anthropic-messages. The Anthropic handler appends "/v1/messages" itself,
// so a BaseURL ending in "/v1" produces https://api.poe.com/v1/v1/messages
// and 404s at the server.
func TestGetModel_PoeAnthropicBaseURL(t *testing.T) {
	m := GetModel(ProviderPoe, "claude-haiku-4.5")
	if m == nil {
		t.Fatal("expected claude-haiku-4.5 on poe to be registered")
	}
	if m.API != ApiAnthropicMessages {
		t.Errorf("expected api %q, got %q", ApiAnthropicMessages, m.API)
	}
	if m.BaseURL != "https://api.poe.com" {
		t.Errorf("expected BaseURL without /v1 suffix, got %q", m.BaseURL)
	}
}

func TestGetModel_UnknownProvider(t *testing.T) {
	m := GetModel("unknown-provider", "some-model")
	if m != nil {
		t.Error("expected nil for unknown provider")
	}
}

func TestGetProviders(t *testing.T) {
	providers := GetProviders()
	if len(providers) == 0 {
		t.Error("expected non-empty providers list")
	}
	providerSet := make(map[string]bool)
	for _, p := range providers {
		providerSet[p] = true
	}
	if !providerSet[ProviderAnthropic] {
		t.Error("expected Anthropic in providers")
	}
	if !providerSet[ProviderOpenAI] {
		t.Error("expected OpenAI in providers")
	}
	if !providerSet[ProviderGoogle] {
		t.Error("expected Google in providers")
	}
}

func TestGetModels(t *testing.T) {
	models := GetModels(ProviderAnthropic)
	if len(models) == 0 {
		t.Error("expected non-empty models for Anthropic")
	}
	for _, m := range models {
		if m.Provider != ProviderAnthropic {
			t.Errorf("expected Anthropic provider, got %q", m.Provider)
		}
		if m.ID == "" {
			t.Error("expected non-empty ID")
		}
		if m.Name == "" {
			t.Error("expected non-empty Name")
		}
	}
}

func TestGetModels_UnknownProvider(t *testing.T) {
	models := GetModels("unknown-provider")
	if models != nil {
		t.Error("expected nil for unknown provider")
	}
}

func TestCalculateCostModel(t *testing.T) {
	model := &Model{
		Cost: ModelCost{Input: 3.0, Output: 15.0, CacheRead: 0.3, CacheWrite: 3.75},
	}
	usage := &Usage{Input: 1000, Output: 500, CacheRead: 2000, CacheWrite: 100}

	cost := CalculateCost(model, usage)
	assertClose(t, "input", 0.003, cost.Input)
	assertClose(t, "output", 0.0075, cost.Output)
	assertClose(t, "cacheRead", 0.0006, cost.CacheRead)
	assertClose(t, "cacheWrite", 0.000375, cost.CacheWrite)
	assertClose(t, "total", 0.011475, cost.Total)
}

func assertClose(t *testing.T, name string, expected, actual float64) {
	t.Helper()
	if math.Abs(expected-actual) > 1e-9 {
		t.Errorf("%s: expected %f, got %f", name, expected, actual)
	}
}

func TestSupportsXhigh_GPT52(t *testing.T) {
	m := &Model{ID: "gpt-5.2-turbo", API: ApiOpenAICompletions}
	if !SupportsXhigh(m) {
		t.Error("expected xhigh support for gpt-5.2")
	}
}

func TestSupportsXhigh_GPT53(t *testing.T) {
	m := &Model{ID: "gpt-5.3", API: ApiOpenAICompletions}
	if !SupportsXhigh(m) {
		t.Error("expected xhigh support for gpt-5.3")
	}
}

func TestSupportsXhigh_AnthropicOpus47Plus(t *testing.T) {
	// Matches across first-party, Bedrock, and Vertex IDs.
	for _, id := range []string{
		"claude-opus-4-8", "claude-opus-4.8",
		"anthropic.claude-opus-4-8", "us.anthropic.claude-opus-4-8",
		"claude-opus-4-8@20260528",
		"claude-opus-4-7", "claude-opus-4.7",
		"anthropic.claude-opus-4-7", "us.anthropic.claude-opus-4-7",
		"claude-opus-4-7@20250101",
	} {
		m := &Model{ID: id, API: ApiAnthropicMessages}
		if !SupportsXhigh(m) {
			t.Errorf("expected xhigh support for %s", id)
		}
	}
	// Bedrock path (different Api) must also work.
	mb := &Model{ID: "anthropic.claude-opus-4-7", API: ApiBedrockConverseStream}
	if !SupportsXhigh(mb) {
		t.Error("expected xhigh support for Bedrock opus-4-7")
	}
}

func TestSupportsXhigh_AnthropicOpus46_ClampsDown(t *testing.T) {
	// Opus 4.6 has a "max" tier but NOT a distinct xhigh tier — xhigh must
	// clamp to "high" for these models.
	m := &Model{ID: "claude-opus-4-6", API: ApiAnthropicMessages}
	if SupportsXhigh(m) {
		t.Error("opus-4-6 must not report xhigh support (clamps to high)")
	}
	m2 := &Model{ID: "claude-opus-4.6", API: ApiAnthropicMessages}
	if SupportsXhigh(m2) {
		t.Error("opus-4.6 must not report xhigh support (clamps to high)")
	}
}

func TestSupportsMax_AnthropicAdaptive(t *testing.T) {
	// All adaptive-thinking Anthropic models support the "max" tier,
	// across first-party, Bedrock, and Vertex IDs.
	for _, id := range []string{
		"claude-opus-4-6", "claude-opus-4.6",
		"claude-opus-4-7", "claude-opus-4.7",
		"claude-opus-4-8", "claude-opus-4.8",
		"claude-sonnet-4-6", "claude-sonnet-4.6",
		"anthropic.claude-opus-4-6",
		"us.anthropic.claude-opus-4-7",
		"us.anthropic.claude-opus-4-8",
		"eu.anthropic.claude-sonnet-4-6",
	} {
		m := &Model{ID: id, API: ApiAnthropicMessages}
		if !SupportsMax(m) {
			t.Errorf("expected max support for %s (ApiAnthropicMessages)", id)
		}
		// Also verify it works for the Bedrock Api.
		mb := &Model{ID: id, API: ApiBedrockConverseStream}
		if !SupportsMax(mb) {
			t.Errorf("expected max support for %s (Bedrock Api)", id)
		}
	}
}

func TestSupportsMax_RegularModel(t *testing.T) {
	m := &Model{ID: "claude-sonnet-4-20250514", API: ApiAnthropicMessages}
	if SupportsMax(m) {
		t.Error("expected no max support for pre-4.6 model")
	}
}

func TestSupportsMax_NilModel(t *testing.T) {
	if SupportsMax(nil) {
		t.Error("expected false for nil model")
	}
}

func TestSupportsXhigh_RegularModel(t *testing.T) {
	m := &Model{ID: "claude-sonnet-4-20250514", API: ApiAnthropicMessages}
	if SupportsXhigh(m) {
		t.Error("expected no xhigh support for regular model")
	}
}

func TestSupportsXhigh_NilModel(t *testing.T) {
	if SupportsXhigh(nil) {
		t.Error("expected false for nil model")
	}
}

func TestModelsAreEqual(t *testing.T) {
	a := &Model{ID: "claude-3", Provider: ProviderAnthropic}
	b := &Model{ID: "claude-3", Provider: ProviderAnthropic}
	if !ModelsAreEqual(a, b) {
		t.Error("expected models to be equal")
	}
}

func TestModelsAreEqual_DifferentID(t *testing.T) {
	a := &Model{ID: "claude-3", Provider: ProviderAnthropic}
	b := &Model{ID: "claude-4", Provider: ProviderAnthropic}
	if ModelsAreEqual(a, b) {
		t.Error("expected models to not be equal")
	}
}

func TestModelsAreEqual_Nil(t *testing.T) {
	a := &Model{ID: "claude-3", Provider: ProviderAnthropic}
	if ModelsAreEqual(a, nil) || ModelsAreEqual(nil, a) || ModelsAreEqual(nil, nil) {
		t.Error("expected false for nil models")
	}
}

func TestGetModel_NewGemini31Models(t *testing.T) {
	tests := []struct {
		provider      Provider
		id            string
		wantAPI       Api
		wantCtxWindow int
		wantReasoning bool
	}{
		{
			provider:      ProviderGoogle,
			id:            "gemini-3.1-pro-preview-customtools",
			wantAPI:       ApiGoogleGenerativeAI,
			wantCtxWindow: 1048576,
			wantReasoning: true,
		},
		{
			provider:      ProviderGoogleVertex,
			id:            "gemini-3.1-pro-preview",
			wantAPI:       ApiGoogleVertex,
			wantCtxWindow: 1048576,
			wantReasoning: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.provider+"/"+tc.id, func(t *testing.T) {
			m := GetModel(tc.provider, tc.id)
			if m == nil {
				t.Fatalf("expected model %q/%q to be registered, got nil", tc.provider, tc.id)
			}
			if m.ID != tc.id {
				t.Errorf("ID: expected %q, got %q", tc.id, m.ID)
			}
			if m.Provider != tc.provider {
				t.Errorf("Provider: expected %q, got %q", tc.provider, m.Provider)
			}
			if m.API != tc.wantAPI {
				t.Errorf("API: expected %q, got %q", tc.wantAPI, m.API)
			}
			if m.ContextWindow != tc.wantCtxWindow {
				t.Errorf("ContextWindow: expected %d, got %d", tc.wantCtxWindow, m.ContextWindow)
			}
			if m.Reasoning != tc.wantReasoning {
				t.Errorf("Reasoning: expected %v, got %v", tc.wantReasoning, m.Reasoning)
			}
		})
	}
}

func TestGeneratedModelsRegistered(t *testing.T) {
	// Verify the init() from models_generated.go registered models
	providers := GetProviders()
	if len(providers) < 5 {
		t.Errorf("expected at least 5 providers, got %d", len(providers))
	}
	total := 0
	for _, p := range providers {
		total += len(GetModels(p))
	}
	if total < 100 {
		t.Errorf("expected >100 total models, got %d", total)
	}
}

// TestUnregisterModel covers the rollback path used when an extension
// withdraws a provider: individual models are removed, and the provider bucket
// itself disappears once it is empty so GetProviders() stops reporting it.
func TestUnregisterModel(t *testing.T) {
	const provider Provider = "test-unregister-model"
	t.Cleanup(func() { UnregisterProviderModels(provider) })

	RegisterModel(&Model{ID: "a", Provider: provider})
	RegisterModel(&Model{ID: "b", Provider: provider})

	// Removing an absent model is a no-op.
	UnregisterModel(provider, "nonexistent")
	UnregisterModel("nonexistent-provider", "a")
	if got := len(GetModels(provider)); got != 2 {
		t.Fatalf("expected 2 models after no-op removals, got %d", got)
	}

	UnregisterModel(provider, "a")
	if GetModel(provider, "a") != nil {
		t.Error("model 'a' should be gone")
	}
	if GetModel(provider, "b") == nil {
		t.Error("model 'b' should survive")
	}

	// Removing the last model drops the provider bucket entirely.
	UnregisterModel(provider, "b")
	if GetModels(provider) != nil {
		t.Error("expected nil models after removing the last one")
	}
	for _, p := range GetProviders() {
		if p == provider {
			t.Errorf("provider %q should no longer be listed", provider)
		}
	}
}

// TestUnregisterProviderModels covers the bulk rollback used on extension
// shutdown.
func TestUnregisterProviderModels(t *testing.T) {
	const provider Provider = "test-unregister-provider"
	t.Cleanup(func() { UnregisterProviderModels(provider) })

	RegisterModel(&Model{ID: "a", Provider: provider})
	RegisterModel(&Model{ID: "b", Provider: provider})
	if got := len(GetModels(provider)); got != 2 {
		t.Fatalf("expected 2 registered models, got %d", got)
	}

	UnregisterProviderModels(provider)
	if GetModels(provider) != nil {
		t.Error("expected all models gone")
	}
	// Idempotent.
	UnregisterProviderModels(provider)
}
