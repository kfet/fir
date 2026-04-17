// Ported from: packages/ai/src/models.ts
// Upstream hash: 1caadb2e
package ai

import (
	"math"
	"testing"
)

func TestGetModel_Anthropic(t *testing.T) {
	m := GetModel(ProviderAnthropic, "claude-sonnet-4-20250514")
	if m == nil {
		t.Fatal("expected non-nil model")
	}
	if m.ID != "claude-sonnet-4-20250514" {
		t.Errorf("expected 'claude-sonnet-4-20250514', got %q", m.ID)
	}
	if m.Provider != ProviderAnthropic {
		t.Errorf("expected provider %q, got %q", ProviderAnthropic, m.Provider)
	}
	if m.Api != ApiAnthropicMessages {
		t.Errorf("expected api %q, got %q", ApiAnthropicMessages, m.Api)
	}
	if m.ContextWindow <= 0 {
		t.Error("expected positive context window")
	}
	if m.MaxTokens <= 0 {
		t.Error("expected positive max tokens")
	}
}

func TestGetModel_OpenAI(t *testing.T) {
	m := GetModel(ProviderOpenAI, "gpt-4o")
	if m == nil {
		t.Fatal("expected non-nil model")
	}
	if m.ID != "gpt-4o" {
		t.Errorf("expected 'gpt-4o', got %q", m.ID)
	}
}

func TestGetModel_NotFound(t *testing.T) {
	m := GetModel(ProviderAnthropic, "nonexistent-model")
	if m != nil {
		t.Error("expected nil for nonexistent model")
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
	m := &Model{ID: "gpt-5.2-turbo", Api: ApiOpenAICompletions}
	if !SupportsXhigh(m) {
		t.Error("expected xhigh support for gpt-5.2")
	}
}

func TestSupportsXhigh_GPT53(t *testing.T) {
	m := &Model{ID: "gpt-5.3", Api: ApiOpenAICompletions}
	if !SupportsXhigh(m) {
		t.Error("expected xhigh support for gpt-5.3")
	}
}

func TestSupportsXhigh_AnthropicOpus47(t *testing.T) {
	// Matches across first-party, Bedrock, and Vertex IDs.
	for _, id := range []string{
		"claude-opus-4-7", "claude-opus-4.7",
		"anthropic.claude-opus-4-7", "us.anthropic.claude-opus-4-7",
		"claude-opus-4-7@20250101",
	} {
		m := &Model{ID: id, Api: ApiAnthropicMessages}
		if !SupportsXhigh(m) {
			t.Errorf("expected xhigh support for %s", id)
		}
	}
	// Bedrock path (different Api) must also work.
	mb := &Model{ID: "anthropic.claude-opus-4-7", Api: ApiBedrockConverseStream}
	if !SupportsXhigh(mb) {
		t.Error("expected xhigh support for Bedrock opus-4-7")
	}
}

func TestSupportsXhigh_AnthropicOpus46_ClampsDown(t *testing.T) {
	// Opus 4.6 has a "max" tier but NOT a distinct xhigh tier — xhigh must
	// clamp to "high" for these models.
	m := &Model{ID: "claude-opus-4-6", Api: ApiAnthropicMessages}
	if SupportsXhigh(m) {
		t.Error("opus-4-6 must not report xhigh support (clamps to high)")
	}
	m2 := &Model{ID: "claude-opus-4.6", Api: ApiAnthropicMessages}
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
		"claude-sonnet-4-6", "claude-sonnet-4.6",
		"anthropic.claude-opus-4-6",
		"us.anthropic.claude-opus-4-7",
		"eu.anthropic.claude-sonnet-4-6",
	} {
		m := &Model{ID: id, Api: ApiAnthropicMessages}
		if !SupportsMax(m) {
			t.Errorf("expected max support for %s (ApiAnthropicMessages)", id)
		}
		// Also verify it works for the Bedrock Api.
		mb := &Model{ID: id, Api: ApiBedrockConverseStream}
		if !SupportsMax(mb) {
			t.Errorf("expected max support for %s (Bedrock Api)", id)
		}
	}
}

func TestSupportsMax_RegularModel(t *testing.T) {
	m := &Model{ID: "claude-sonnet-4-20250514", Api: ApiAnthropicMessages}
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
	m := &Model{ID: "claude-sonnet-4-20250514", Api: ApiAnthropicMessages}
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
			provider:      ProviderGoogleAntigravity,
			id:            "gemini-3.1-pro-high",
			wantAPI:       ApiGoogleGeminiCLI,
			wantCtxWindow: 1048576,
			wantReasoning: true,
		},
		{
			provider:      ProviderGoogleAntigravity,
			id:            "gemini-3.1-pro-low",
			wantAPI:       ApiGoogleGeminiCLI,
			wantCtxWindow: 1048576,
			wantReasoning: true,
		},
		{
			provider:      ProviderGoogleGeminiCLI,
			id:            "gemini-3.1-pro-preview",
			wantAPI:       ApiGoogleGeminiCLI,
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
			if m.Api != tc.wantAPI {
				t.Errorf("Api: expected %q, got %q", tc.wantAPI, m.Api)
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
