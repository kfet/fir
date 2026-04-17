package models

import (
	"path/filepath"
	"testing"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
)

// --- Test models ---

func testModels() []*ai.Model {
	return []*ai.Model{
		{
			ID: "claude-sonnet-4-5", Name: "Claude Sonnet 4.5",
			Api: ai.ApiAnthropicMessages, Provider: ai.ProviderAnthropic,
			BaseURL: "https://api.anthropic.com", Reasoning: true,
			Input: []string{"text", "image"}, Cost: ai.ModelCost{Input: 3.0, Output: 15.0},
			ContextWindow: 200000, MaxTokens: 8192,
		},
		{
			ID: "claude-sonnet-4-5-20250929", Name: "Claude Sonnet 4.5 (Sep 2025)",
			Api: ai.ApiAnthropicMessages, Provider: ai.ProviderAnthropic,
			BaseURL: "https://api.anthropic.com", Reasoning: true,
			Input: []string{"text", "image"}, Cost: ai.ModelCost{Input: 3.0, Output: 15.0},
			ContextWindow: 200000, MaxTokens: 8192,
		},
		{
			ID: "claude-opus-4-6", Name: "Claude Opus 4.6",
			Api: ai.ApiAnthropicMessages, Provider: ai.ProviderAnthropic,
			BaseURL: "https://api.anthropic.com", Reasoning: true,
			Input: []string{"text", "image"}, Cost: ai.ModelCost{Input: 15.0, Output: 75.0},
			ContextWindow: 200000, MaxTokens: 8192,
		},
		{
			ID: "gpt-5.1-codex", Name: "GPT 5.1 Codex",
			Api: ai.ApiOpenAICompletions, Provider: ai.ProviderOpenAI,
			BaseURL: "https://api.openai.com", Reasoning: false,
			Input: []string{"text"}, Cost: ai.ModelCost{Input: 2.0, Output: 8.0},
			ContextWindow: 128000, MaxTokens: 16384,
		},
		{
			ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro",
			Api: ai.ApiGoogleGenerativeAI, Provider: ai.ProviderGoogle,
			BaseURL: "https://generativelanguage.googleapis.com",
			Input:   []string{"text", "image"}, Cost: ai.ModelCost{},
			ContextWindow: 1000000, MaxTokens: 8192,
		},
		{
			ID: "openai/gpt-5.1-codex", Name: "GPT 5.1 Codex (via OpenRouter)",
			Api: ai.ApiOpenAICompletions, Provider: ai.ProviderOpenRouter,
			BaseURL: "https://openrouter.ai/api",
			Input:   []string{"text"}, Cost: ai.ModelCost{},
			ContextWindow: 128000, MaxTokens: 16384,
		},
		{
			ID: "model:exacto", Name: "Model with Colon",
			Api: ai.ApiOpenAICompletions, Provider: "test-provider",
			BaseURL: "https://api.test.com",
			Input:   []string{"text"}, Cost: ai.ModelCost{},
			ContextWindow: 128000, MaxTokens: 4096,
		},
	}
}

// --- isAlias ---

func TestIsAlias(t *testing.T) {
	tests := []struct {
		id    string
		alias bool
	}{
		{"claude-sonnet-4-5", true},
		{"claude-sonnet-4-5-20250929", false},
		{"gpt-5.1-codex", true},
		{"devstral-medium-latest", true},
		{"model-20241022", false},
	}
	for _, tc := range tests {
		if got := isAlias(tc.id); got != tc.alias {
			t.Errorf("isAlias(%q) = %v, want %v", tc.id, got, tc.alias)
		}
	}
}

// --- isValidThinkingLevel ---

func TestIsValidThinkingLevel(t *testing.T) {
	valid := []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}
	for _, v := range valid {
		if !isValidThinkingLevel(v) {
			t.Errorf("expected %q to be valid", v)
		}
	}
	invalid := []string{"", "none", "ultra"}
	for _, v := range invalid {
		if isValidThinkingLevel(v) {
			t.Errorf("expected %q to be invalid", v)
		}
	}
}

// --- tryMatchModel ---

func TestTryMatchModel_ExactID(t *testing.T) {
	models := testModels()
	m := tryMatchModel("claude-opus-4-6", models)
	if m == nil || m.ID != "claude-opus-4-6" {
		t.Errorf("expected claude-opus-4-6, got %v", m)
	}
}

func TestTryMatchModel_ProviderSlashModel(t *testing.T) {
	models := testModels()
	m := tryMatchModel("anthropic/claude-opus-4-6", models)
	if m == nil || m.ID != "claude-opus-4-6" {
		t.Errorf("expected claude-opus-4-6, got %v", m)
	}
}

func TestTryMatchModel_CaseInsensitive(t *testing.T) {
	models := testModels()
	m := tryMatchModel("Claude-Opus-4-6", models)
	if m == nil || m.ID != "claude-opus-4-6" {
		t.Errorf("expected claude-opus-4-6, got %v", m)
	}
}

func TestTryMatchModel_PartialMatch_PrefersAlias(t *testing.T) {
	models := testModels()
	// "sonnet" should match both claude-sonnet-4-5 and claude-sonnet-4-5-20250929
	// Should prefer the alias (no date suffix)
	m := tryMatchModel("sonnet", models)
	if m == nil || m.ID != "claude-sonnet-4-5" {
		t.Errorf("expected alias claude-sonnet-4-5, got %v", m)
	}
}

func TestTryMatchModel_NoMatch(t *testing.T) {
	models := testModels()
	m := tryMatchModel("nonexistent-model", models)
	if m != nil {
		t.Errorf("expected nil, got %v", m)
	}
}

// --- ParseModelPattern ---

func TestParseModelPattern_ExactMatch(t *testing.T) {
	models := testModels()
	result := ParseModelPattern("claude-opus-4-6", models)
	if result.Model == nil || result.Model.ID != "claude-opus-4-6" {
		t.Errorf("expected claude-opus-4-6, got %v", result.Model)
	}
	if result.ThinkingLevel != "" {
		t.Errorf("expected empty thinking level, got %q", result.ThinkingLevel)
	}
	if result.Warning != "" {
		t.Errorf("expected no warning, got %q", result.Warning)
	}
}

func TestParseModelPattern_WithThinkingLevel(t *testing.T) {
	models := testModels()
	result := ParseModelPattern("claude-opus-4-6:high", models)
	if result.Model == nil || result.Model.ID != "claude-opus-4-6" {
		t.Errorf("expected claude-opus-4-6, got %v", result.Model)
	}
	if result.ThinkingLevel != "high" {
		t.Errorf("expected thinking level 'high', got %q", result.ThinkingLevel)
	}
}

func TestParseModelPattern_ColonInModelID(t *testing.T) {
	models := testModels()
	// "model:exacto" is a valid model ID
	result := ParseModelPattern("model:exacto", models)
	if result.Model == nil || result.Model.ID != "model:exacto" {
		t.Errorf("expected model:exacto, got %v", result.Model)
	}
}

func TestParseModelPattern_ColonInModelID_WithThinkingLevel(t *testing.T) {
	models := testModels()
	// "model:exacto:high" — should match "model:exacto" with thinking level "high"
	result := ParseModelPattern("model:exacto:high", models)
	if result.Model == nil || result.Model.ID != "model:exacto" {
		t.Errorf("expected model:exacto, got %v", result.Model)
	}
	if result.ThinkingLevel != "high" {
		t.Errorf("expected thinking level 'high', got %q", result.ThinkingLevel)
	}
}

func TestParseModelPattern_InvalidThinkingLevel(t *testing.T) {
	models := testModels()
	result := ParseModelPattern("claude-opus-4-6:invalid", models)
	if result.Model == nil || result.Model.ID != "claude-opus-4-6" {
		t.Errorf("expected claude-opus-4-6, got %v", result.Model)
	}
	if result.ThinkingLevel != "" {
		t.Errorf("expected empty thinking level, got %q", result.ThinkingLevel)
	}
	if result.Warning == "" {
		t.Error("expected warning for invalid thinking level")
	}
}

func TestParseModelPatternStrict_InvalidThinkingLevel_NoFallback(t *testing.T) {
	// In strict mode, invalid thinking level suffix should NOT fall back to prefix match.
	models := testModels()
	result := parseModelPatternStrict("claude-opus-4-6:invalid", models)
	if result.Model != nil {
		t.Errorf("strict mode: expected no match for invalid suffix, got %v", result.Model)
	}
	if result.Warning != "" {
		t.Errorf("strict mode: expected no warning, got %q", result.Warning)
	}
}

func TestParseModelPattern_NoMatch(t *testing.T) {
	models := testModels()
	result := ParseModelPattern("nonexistent", models)
	if result.Model != nil {
		t.Errorf("expected nil model, got %v", result.Model)
	}
}

// --- globMatch ---

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		text, pattern string
		want          bool
	}{
		{"claude-opus-4-6", "claude-*", true},
		{"claude-opus-4-6", "*opus*", true},
		{"claude-opus-4-6", "gemini-*", false},
		{"claude-opus-4-6", "claude-opus-4-?", true},
		{"anthropic/claude-opus-4-6", "anthropic/*", true},
		{"anthropic/claude-opus-4-6", "*/claude-*", true},
		{"anything", "*", true},
		{"CLAUDE-OPUS", "claude-opus", true}, // case insensitive
	}
	for _, tc := range tests {
		if got := globMatch(tc.text, tc.pattern); got != tc.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", tc.text, tc.pattern, got, tc.want)
		}
	}
}

// --- Test helpers ---

func newTestRegistry(t *testing.T, models []*ai.Model) *ModelRegistry {
	t.Helper()
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	authStorage := auth.NewAuthStorage(authPath)

	// Set up auth for all providers in our test models
	providers := make(map[string]bool)
	for _, m := range models {
		providers[m.Provider] = true
	}
	for p := range providers {
		authStorage.Set(p, auth.AuthCredential{Type: auth.CredentialTypeAPIKey, Key: "test-key"})
	}

	// Register models
	for _, m := range models {
		ai.RegisterModel(m)
	}

	registry := NewModelRegistry(authStorage, "")
	return registry
}

// --- FindInitialModel ---

func TestFindInitialModel_CLIArgs(t *testing.T) {
	models := testModels()
	registry := newTestRegistry(t, models)

	result := FindInitialModel(FindInitialModelOptions{
		CLIProvider:   ai.ProviderAnthropic,
		CLIModel:      "claude-opus-4-6",
		ModelRegistry: registry,
	})
	if result.Model == nil || result.Model.ID != "claude-opus-4-6" {
		t.Errorf("expected claude-opus-4-6 from CLI args, got %v", result.Model)
	}
}

func TestFindInitialModel_CLIArgs_NotFound(t *testing.T) {
	models := testModels()
	registry := newTestRegistry(t, models)

	result := FindInitialModel(FindInitialModelOptions{
		CLIProvider:   "nonexistent",
		CLIModel:      "nonexistent",
		ModelRegistry: registry,
	})
	if result.Model != nil {
		t.Errorf("expected nil for nonexistent CLI model, got %v", result.Model)
	}
}

func TestFindInitialModel_Default(t *testing.T) {
	models := testModels()
	registry := newTestRegistry(t, models)

	result := FindInitialModel(FindInitialModelOptions{
		DefaultProvider:      ai.ProviderAnthropic,
		DefaultModelID:       "claude-opus-4-6",
		DefaultThinkingLevel: "low",
		ModelRegistry:        registry,
	})
	if result.Model == nil || result.Model.ID != "claude-opus-4-6" {
		t.Errorf("expected claude-opus-4-6 from default, got %v", result.Model)
	}
	if result.ThinkingLevel != "low" {
		t.Errorf("expected thinking level 'low', got %q", result.ThinkingLevel)
	}
}

// --- RestoreModelFromSession ---

func TestRestoreModelFromSession_Found(t *testing.T) {
	models := testModels()
	registry := newTestRegistry(t, models)

	model, fallback := RestoreModelFromSession(
		ai.ProviderAnthropic, "claude-opus-4-6",
		nil, registry,
	)
	if model == nil || model.ID != "claude-opus-4-6" {
		t.Errorf("expected claude-opus-4-6, got %v", model)
	}
	if fallback != "" {
		t.Errorf("expected no fallback message, got %q", fallback)
	}
}

func TestRestoreModelFromSession_NotFound_WithCurrent(t *testing.T) {
	models := testModels()
	registry := newTestRegistry(t, models)

	currentModel := models[0]
	model, fallback := RestoreModelFromSession(
		"nonexistent", "nonexistent",
		currentModel, registry,
	)
	if model != currentModel {
		t.Errorf("expected current model as fallback, got %v", model)
	}
	if fallback == "" {
		t.Error("expected fallback message")
	}
}

func TestRestoreModelFromSession_NotFound_NoCurrent(t *testing.T) {
	models := testModels()
	registry := newTestRegistry(t, models)

	model, fallback := RestoreModelFromSession(
		"nonexistent", "nonexistent",
		nil, registry,
	)
	// Should find some available model
	if model == nil {
		t.Error("expected some available model as fallback")
	}
	if fallback == "" {
		t.Error("expected fallback message")
	}
}

// --- ResolveCliModel ---

func TestResolveCliModel_Empty(t *testing.T) {
	registry := newTestRegistry(t, testModels())
	result := ResolveCliModel(ResolveCliModelOptions{ModelRegistry: registry})
	if result.Model != nil || result.Error != "" {
		t.Errorf("expected empty result, got %+v", result)
	}
}

func TestResolveCliModel_ExactID(t *testing.T) {
	registry := newTestRegistry(t, testModels())
	result := ResolveCliModel(ResolveCliModelOptions{
		CLIModel:      "claude-opus-4-6",
		ModelRegistry: registry,
	})
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.Model == nil || result.Model.ID != "claude-opus-4-6" {
		t.Errorf("expected claude-opus-4-6, got %v", result.Model)
	}
}

func TestResolveCliModel_ProviderSlashModel(t *testing.T) {
	registry := newTestRegistry(t, testModels())
	result := ResolveCliModel(ResolveCliModelOptions{
		CLIModel:      "anthropic/claude-opus-4-6",
		ModelRegistry: registry,
	})
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.Model == nil || result.Model.ID != "claude-opus-4-6" {
		t.Errorf("expected claude-opus-4-6, got %v", result.Model)
	}
}

func TestResolveCliModel_ExplicitProvider(t *testing.T) {
	registry := newTestRegistry(t, testModels())
	result := ResolveCliModel(ResolveCliModelOptions{
		CLIProvider:   "anthropic",
		CLIModel:      "opus",
		ModelRegistry: registry,
	})
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.Model == nil || result.Model.Provider != "anthropic" {
		t.Errorf("expected anthropic model, got %v", result.Model)
	}
}

func TestResolveCliModel_UnknownProvider(t *testing.T) {
	registry := newTestRegistry(t, testModels())
	result := ResolveCliModel(ResolveCliModelOptions{
		CLIProvider:   "nonexistent-provider",
		CLIModel:      "some-model",
		ModelRegistry: registry,
	})
	if result.Error == "" {
		t.Error("expected error for unknown provider")
	}
}

func TestResolveCliModel_ModelNotFound(t *testing.T) {
	registry := newTestRegistry(t, testModels())
	result := ResolveCliModel(ResolveCliModelOptions{
		CLIModel:      "nonexistent-model-xyz",
		ModelRegistry: registry,
	})
	if result.Error == "" {
		t.Error("expected error for unknown model")
	}
	if result.Model != nil {
		t.Errorf("expected nil model, got %v", result.Model)
	}
}

func TestResolveCliModel_OpenRouterSlashID(t *testing.T) {
	// "openai/gpt-5.1-codex" is a model ID on openrouter (not provider=openai, model=gpt-5.1-codex)
	registry := newTestRegistry(t, testModels())
	result := ResolveCliModel(ResolveCliModelOptions{
		CLIModel:      "openai/gpt-5.1-codex",
		ModelRegistry: registry,
	})
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	// Should prefer openai/gpt-5.1-codex on OpenRouter, OR the OpenAI model —
	// either is acceptable since both exist. The key is no error.
	if result.Model == nil {
		t.Error("expected a model match")
	}
}

func TestResolveCliModel_ThinkingLevel(t *testing.T) {
	registry := newTestRegistry(t, testModels())
	result := ResolveCliModel(ResolveCliModelOptions{
		CLIModel:      "claude-opus-4-6:high",
		ModelRegistry: registry,
	})
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.Model == nil || result.Model.ID != "claude-opus-4-6" {
		t.Errorf("expected claude-opus-4-6, got %v", result.Model)
	}
	if result.ThinkingLevel != "high" {
		t.Errorf("expected thinking level 'high', got %q", result.ThinkingLevel)
	}
}
