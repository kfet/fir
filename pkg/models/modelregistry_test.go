package models

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/config"
)

// setupTestModelRegistry creates a ModelRegistry with a temp auth file.
func setupTestModelRegistry(t *testing.T, modelsJsonPath string) (*ModelRegistry, *auth.AuthStorage) {
	t.Helper()
	tmpDir := t.TempDir()
	authPath := filepath.Join(tmpDir, "auth.json")
	authStorage := auth.NewAuthStorage(authPath)
	registry := NewModelRegistry(authStorage, modelsJsonPath)
	return registry, authStorage
}

func TestModelRegistry_GetAll_BuiltIn(t *testing.T) {
	ai.RegisterModel(&ai.Model{
		ID:            "test-model-1",
		Name:          "Test Model 1",
		API:           ai.ApiAnthropicMessages,
		Provider:      "test-provider-builtin",
		BaseURL:       "https://api.test.com",
		Reasoning:     false,
		Input:         []ai.InputModality{ai.InputText},
		Cost:          ai.ModelCost{Input: 3.0, Output: 15.0},
		ContextWindow: 200000,
		MaxTokens:     4096,
	})

	registry, _ := setupTestModelRegistry(t, "")
	models := registry.GetAll()

	found := false
	for _, m := range models {
		if m.ID == "test-model-1" && m.Provider == "test-provider-builtin" {
			found = true
			if m.Name != "Test Model 1" {
				t.Errorf("expected name 'Test Model 1', got %q", m.Name)
			}
			if m.ContextWindow != 200000 {
				t.Errorf("expected contextWindow 200000, got %d", m.ContextWindow)
			}
		}
	}
	if !found {
		t.Error("test-model-1 not found in GetAll results")
	}
}

func TestModelRegistry_Find(t *testing.T) {
	ai.RegisterModel(&ai.Model{
		ID:            "find-me",
		Name:          "Find Me",
		API:           ai.ApiAnthropicMessages,
		Provider:      "test-provider-find",
		BaseURL:       "https://api.test.com",
		Input:         []ai.InputModality{ai.InputText},
		Cost:          ai.ModelCost{},
		ContextWindow: 128000,
		MaxTokens:     4096,
	})

	registry, _ := setupTestModelRegistry(t, "")

	m := registry.Find("test-provider-find", "find-me")
	if m == nil {
		t.Fatal("expected to find model")
	}
	if m.Name != "Find Me" {
		t.Errorf("expected name 'Find Me', got %q", m.Name)
	}

	m = registry.Find("test-provider-find", "nonexistent")
	// With cold-start synthesis, an unknown ID returns a sibling-cloned model
	// rather than nil when siblings exist (and no live-list state contradicts
	// it). Verify it inherits the sibling's wire-protocol fields.
	if m == nil {
		t.Fatal("expected synthesised model for unknown ID when siblings exist, got nil")
	}
	if m.ID != "nonexistent" || m.Provider != "test-provider-find" {
		t.Errorf("expected synthesised model with correct ID/Provider, got %+v", m)
	}
	if m.API != ai.ApiAnthropicMessages {
		t.Errorf("expected synth to inherit sibling's Api, got %q", m.API)
	}
	if m.BaseURL != "https://api.test.com" {
		t.Errorf("expected synth to inherit sibling's BaseURL, got %q", m.BaseURL)
	}
	if m.ContextWindow != 128000 {
		t.Errorf("expected synth to inherit sibling's ContextWindow, got %d", m.ContextWindow)
	}
	if !m.SWEInferred {
		t.Error("expected synth to flag SWEInferred=true")
	}

	// When no siblings exist at all, Find returns nil
	m = registry.Find("unknown-provider", "unknown-model")
	if m != nil {
		t.Error("expected nil for unknown provider with no siblings")
	}
}

func TestModelRegistry_GetAvailable(t *testing.T) {
	ai.RegisterModel(&ai.Model{
		ID:       "avail-model",
		Name:     "Available Model",
		API:      ai.ApiAnthropicMessages,
		Provider: "test-provider-avail",
		BaseURL:  "https://api.test.com",
		Input:    []ai.InputModality{ai.InputText},
		Cost:     ai.ModelCost{},
	})

	registry, authStorage := setupTestModelRegistry(t, "")

	// Before setting auth, should not be available.
	avail := registry.GetAvailable()
	for _, m := range avail {
		if m.Provider == "test-provider-avail" {
			t.Error("model should not be available without auth")
		}
	}

	// Set auth.
	authStorage.SetRuntimeApiKey("test-provider-avail", "test-key")

	avail = registry.GetAvailable()
	found := false
	for _, m := range avail {
		if m.Provider == "test-provider-avail" && m.ID == "avail-model" {
			found = true
		}
	}
	if !found {
		t.Error("model should be available after setting auth")
	}
}

func TestModelRegistry_GetApiKey(t *testing.T) {
	ai.RegisterModel(&ai.Model{
		ID:       "key-model",
		Name:     "Key Model",
		API:      ai.ApiAnthropicMessages,
		Provider: "test-provider-key",
		BaseURL:  "https://api.test.com",
		Input:    []ai.InputModality{ai.InputText},
		Cost:     ai.ModelCost{},
	})

	registry, authStorage := setupTestModelRegistry(t, "")
	authStorage.SetRuntimeApiKey("test-provider-key", "my-secret-key")

	model := registry.Find("test-provider-key", "key-model")
	if model == nil {
		t.Fatal("model not found")
	}

	key := registry.GetApiKey(model)
	if key != "my-secret-key" {
		t.Errorf("expected 'my-secret-key', got %q", key)
	}
}

func TestModelRegistry_CustomModels(t *testing.T) {
	tmpDir := t.TempDir()
	modelsPath := filepath.Join(tmpDir, "models.json")

	config := ModelsConfig{
		Providers: map[string]ProviderConfig{
			"my-custom": {
				BaseURL: "http://localhost:11434",
				ApiKey:  "sk-custom-key",
				Api:     "openai-completions",
				Models: []ModelDefinition{
					{
						ID:   "my-local-model",
						Name: "My Local Model",
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(modelsPath, data, 0644)

	registry, _ := setupTestModelRegistry(t, modelsPath)

	m := registry.Find("my-custom", "my-local-model")
	if m == nil {
		t.Fatal("custom model not found")
	}
	if m.Name != "My Local Model" {
		t.Errorf("expected name 'My Local Model', got %q", m.Name)
	}
	if m.BaseURL != "http://localhost:11434" {
		t.Errorf("expected baseURL 'http://localhost:11434', got %q", m.BaseURL)
	}
	if m.API != "openai-completions" {
		t.Errorf("expected api 'openai-completions', got %q", m.API)
	}
	// Should have default contextWindow.
	if m.ContextWindow != 128000 {
		t.Errorf("expected default contextWindow 128000, got %d", m.ContextWindow)
	}
	if m.MaxTokens != 16384 {
		t.Errorf("expected default maxTokens 16384, got %d", m.MaxTokens)
	}
}

func TestModelRegistry_CustomModels_WithOverrides(t *testing.T) {
	ai.RegisterModel(&ai.Model{
		ID:            "override-target",
		Name:          "Original Name",
		API:           ai.ApiOpenAICompletions,
		Provider:      "test-provider-override",
		BaseURL:       "https://api.original.com",
		Reasoning:     false,
		Input:         []ai.InputModality{ai.InputText},
		Cost:          ai.ModelCost{Input: 1.0, Output: 2.0},
		ContextWindow: 100000,
		MaxTokens:     4096,
	})

	tmpDir := t.TempDir()
	modelsPath := filepath.Join(tmpDir, "models.json")

	boolTrue := true
	newCW := 200000
	config := ModelsConfig{
		Providers: map[string]ProviderConfig{
			"test-provider-override": {
				BaseURL: "https://api.override.com",
				ModelOverrides: map[string]ModelOverride{
					"override-target": {
						Name:          "Overridden Name",
						Reasoning:     &boolTrue,
						ContextWindow: &newCW,
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(modelsPath, data, 0644)

	registry, _ := setupTestModelRegistry(t, modelsPath)

	m := registry.Find("test-provider-override", "override-target")
	if m == nil {
		t.Fatal("model not found")
	}
	if m.Name != "Overridden Name" {
		t.Errorf("expected 'Overridden Name', got %q", m.Name)
	}
	if m.ContextWindow != 200000 {
		t.Errorf("expected contextWindow 200000, got %d", m.ContextWindow)
	}
	if m.BaseURL != "https://api.override.com" {
		t.Errorf("expected baseURL 'https://api.override.com', got %q", m.BaseURL)
	}
	if !m.Reasoning {
		t.Error("expected reasoning=true after override")
	}
	// Cost should remain original.
	if m.Cost.Input != 1.0 {
		t.Errorf("expected cost.Input 1.0, got %f", m.Cost.Input)
	}
}

func TestModelRegistry_CustomModels_PartialCostOverride(t *testing.T) {
	ai.RegisterModel(&ai.Model{
		ID:            "cost-override-target",
		Name:          "Cost Model",
		API:           ai.ApiOpenAICompletions,
		Provider:      "test-provider-cost",
		BaseURL:       "https://api.test.com",
		Input:         []ai.InputModality{ai.InputText},
		Cost:          ai.ModelCost{Input: 1.0, Output: 2.0, CacheRead: 0.5, CacheWrite: 0.3},
		ContextWindow: 100000,
		MaxTokens:     4096,
	})

	tmpDir := t.TempDir()
	modelsPath := filepath.Join(tmpDir, "models.json")

	newOutput := 5.0
	config := ModelsConfig{
		Providers: map[string]ProviderConfig{
			"test-provider-cost": {
				ModelOverrides: map[string]ModelOverride{
					"cost-override-target": {
						Cost: &ModelCostConfig{
							Output: &newOutput,
						},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(modelsPath, data, 0644)

	registry, _ := setupTestModelRegistry(t, modelsPath)

	m := registry.Find("test-provider-cost", "cost-override-target")
	if m == nil {
		t.Fatal("model not found")
	}
	if m.Cost.Input != 1.0 {
		t.Errorf("expected cost.Input 1.0, got %f", m.Cost.Input)
	}
	if m.Cost.Output != 5.0 {
		t.Errorf("expected cost.Output 5.0, got %f", m.Cost.Output)
	}
	if m.Cost.CacheRead != 0.5 {
		t.Errorf("expected cost.CacheRead 0.5, got %f", m.Cost.CacheRead)
	}
}

func TestModelRegistry_InvalidModelsJson(t *testing.T) {
	tmpDir := t.TempDir()
	modelsPath := filepath.Join(tmpDir, "models.json")

	os.WriteFile(modelsPath, []byte("{invalid json"), 0644)

	registry, _ := setupTestModelRegistry(t, modelsPath)

	errMsg := registry.GetError()
	if errMsg == "" {
		t.Error("expected error for invalid JSON")
	}

	// Should still have built-in models.
	models := registry.GetAll()
	if len(models) == 0 {
		t.Error("expected built-in models even with invalid models.json")
	}
}

func TestModelRegistry_MissingModelsJson(t *testing.T) {
	registry, _ := setupTestModelRegistry(t, "/nonexistent/path/models.json")

	errMsg := registry.GetError()
	if errMsg != "" {
		t.Errorf("expected no error for missing file, got %q", errMsg)
	}

	models := registry.GetAll()
	if len(models) == 0 {
		t.Error("expected built-in models")
	}
}

func TestModelRegistry_ValidationErrors(t *testing.T) {
	tests := []struct {
		name   string
		config ModelsConfig
	}{
		{
			name: "models without baseUrl",
			config: ModelsConfig{
				Providers: map[string]ProviderConfig{
					"bad-provider": {
						ApiKey: "key",
						Api:    "openai-completions",
						Models: []ModelDefinition{{ID: "m1"}},
					},
				},
			},
		},
		{
			name: "models without apiKey",
			config: ModelsConfig{
				Providers: map[string]ProviderConfig{
					"bad-provider": {
						BaseURL: "http://localhost",
						Api:     "openai-completions",
						Models:  []ModelDefinition{{ID: "m1"}},
					},
				},
			},
		},
		{
			name: "models without api",
			config: ModelsConfig{
				Providers: map[string]ProviderConfig{
					"bad-provider": {
						BaseURL: "http://localhost",
						ApiKey:  "key",
						Models:  []ModelDefinition{{ID: "m1"}},
					},
				},
			},
		},
		{
			name: "override-only without baseUrl or modelOverrides",
			config: ModelsConfig{
				Providers: map[string]ProviderConfig{
					"bad-provider": {
						ApiKey: "key",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			modelsPath := filepath.Join(tmpDir, "models.json")
			data, _ := json.MarshalIndent(tt.config, "", "  ")
			os.WriteFile(modelsPath, data, 0644)

			registry, _ := setupTestModelRegistry(t, modelsPath)
			if registry.GetError() == "" {
				t.Error("expected validation error")
			}
		})
	}
}

func TestModelRegistry_Refresh(t *testing.T) {
	tmpDir := t.TempDir()
	modelsPath := filepath.Join(tmpDir, "models.json")

	// Write initial config.
	config := ModelsConfig{
		Providers: map[string]ProviderConfig{
			"refresh-provider": {
				BaseURL: "http://localhost:11434",
				ApiKey:  "key1",
				Api:     "openai-completions",
				Models: []ModelDefinition{
					{ID: "model-v1", Name: "Model V1"},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(modelsPath, data, 0644)

	registry, _ := setupTestModelRegistry(t, modelsPath)

	m := registry.Find("refresh-provider", "model-v1")
	if m == nil {
		t.Fatal("initial model not found")
	}

	// Update config.
	config.Providers["refresh-provider"] = ProviderConfig{
		BaseURL: "http://localhost:11434",
		ApiKey:  "key1",
		Api:     "openai-completions",
		Models: []ModelDefinition{
			{ID: "model-v1", Name: "Model V1 Updated"},
		},
	}
	data, _ = json.MarshalIndent(config, "", "  ")
	os.WriteFile(modelsPath, data, 0644)

	registry.Refresh()

	m = registry.Find("refresh-provider", "model-v1")
	if m == nil {
		t.Fatal("model not found after refresh")
	}
	if m.Name != "Model V1 Updated" {
		t.Errorf("expected 'Model V1 Updated', got %q", m.Name)
	}
}

func TestModelRegistry_CustomModels_MergeOverridesBuiltIn(t *testing.T) {
	ai.RegisterModel(&ai.Model{
		ID:            "merge-target",
		Name:          "Built-in Model",
		API:           ai.ApiOpenAICompletions,
		Provider:      "test-provider-merge",
		BaseURL:       "https://api.builtin.com",
		Input:         []ai.InputModality{ai.InputText},
		Cost:          ai.ModelCost{Input: 1.0, Output: 2.0},
		ContextWindow: 100000,
		MaxTokens:     4096,
	})

	tmpDir := t.TempDir()
	modelsPath := filepath.Join(tmpDir, "models.json")

	// Custom model with same provider+id should override.
	config := ModelsConfig{
		Providers: map[string]ProviderConfig{
			"test-provider-merge": {
				BaseURL: "https://api.custom.com",
				ApiKey:  "custom-key",
				Api:     "openai-completions",
				Models: []ModelDefinition{
					{
						ID:   "merge-target",
						Name: "Custom Override",
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(modelsPath, data, 0644)

	registry, _ := setupTestModelRegistry(t, modelsPath)

	m := registry.Find("test-provider-merge", "merge-target")
	if m == nil {
		t.Fatal("model not found")
	}
	if m.Name != "Custom Override" {
		t.Errorf("expected 'Custom Override' (custom wins), got %q", m.Name)
	}
	if m.BaseURL != "https://api.custom.com" {
		t.Errorf("expected custom baseURL, got %q", m.BaseURL)
	}
}

func TestModelRegistry_FallbackApiKey(t *testing.T) {
	config.ClearConfigValueCache()
	tmpDir := t.TempDir()
	modelsPath := filepath.Join(tmpDir, "models.json")

	config := ModelsConfig{
		Providers: map[string]ProviderConfig{
			"fallback-provider": {
				BaseURL: "http://localhost:11434",
				ApiKey:  "fallback-literal-key",
				Api:     "openai-completions",
				Models: []ModelDefinition{
					{ID: "fallback-model"},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(modelsPath, data, 0644)

	registry, authStorage := setupTestModelRegistry(t, modelsPath)

	// The fallback resolver should resolve the API key.
	key := authStorage.GetApiKey("fallback-provider")
	if key != "fallback-literal-key" {
		t.Errorf("expected 'fallback-literal-key', got %q", key)
	}

	// Via registry.
	m := registry.Find("fallback-provider", "fallback-model")
	if m == nil {
		t.Fatal("model not found")
	}
	key = registry.GetApiKey(m)
	if key != "fallback-literal-key" {
		t.Errorf("expected 'fallback-literal-key' via registry, got %q", key)
	}
}

func TestModelRegistry_IsUsingOAuth(t *testing.T) {
	ai.RegisterModel(&ai.Model{
		ID:       "oauth-model",
		Name:     "OAuth Model",
		API:      ai.ApiAnthropicMessages,
		Provider: "test-provider-oauth",
		BaseURL:  "https://api.test.com",
		Input:    []ai.InputModality{ai.InputText},
		Cost:     ai.ModelCost{},
	})

	tmpDir := t.TempDir()
	authPath := filepath.Join(tmpDir, "auth.json")
	authStorage := auth.NewAuthStorage(authPath)

	// Set OAuth credential.
	authStorage.Set("test-provider-oauth", auth.AuthCredential{
		Type:   auth.CredentialTypeOAuth,
		Access: "oauth-token",
	})

	registry := NewModelRegistry(authStorage, "")

	m := registry.Find("test-provider-oauth", "oauth-model")
	if m == nil {
		t.Fatal("model not found")
	}

	if !registry.IsUsingOAuth(m) {
		t.Error("expected IsUsingOAuth to be true")
	}

	// Regression: OAuth-only providers have no API key, but MUST still count as
	// having configured auth. Session restore (pkg/session/sdk.go) keys off
	// HasConfiguredAuth, not GetApiKey; using GetApiKey here caused -c to
	// spuriously fail to restore OAuth models ("Could not restore X. Using X.").
	if registry.GetApiKey(m) != "" {
		t.Errorf("expected empty API key for OAuth provider, got %q", registry.GetApiKey(m))
	}
	if !registry.HasConfiguredAuth(m) {
		t.Error("expected HasConfiguredAuth to be true for OAuth provider")
	}
}

func TestModelRegistry_AuthHeader(t *testing.T) {
	config.ClearConfigValueCache()
	tmpDir := t.TempDir()
	modelsPath := filepath.Join(tmpDir, "models.json")

	authHeader := true
	config := ModelsConfig{
		Providers: map[string]ProviderConfig{
			"auth-header-provider": {
				BaseURL:    "http://localhost:11434",
				ApiKey:     "my-auth-key",
				Api:        "openai-completions",
				AuthHeader: &authHeader,
				Models: []ModelDefinition{
					{ID: "auth-header-model"},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(modelsPath, data, 0644)

	registry, _ := setupTestModelRegistry(t, modelsPath)

	m := registry.Find("auth-header-provider", "auth-header-model")
	if m == nil {
		t.Fatal("model not found")
	}
	if m.Headers == nil {
		t.Fatal("expected headers to be set")
	}
	if m.Headers["Authorization"] != "Bearer my-auth-key" {
		t.Errorf("expected 'Bearer my-auth-key', got %q", m.Headers["Authorization"])
	}
}

func TestModelRegistry_HeaderMerging(t *testing.T) {
	config.ClearConfigValueCache()
	tmpDir := t.TempDir()
	modelsPath := filepath.Join(tmpDir, "models.json")

	config := ModelsConfig{
		Providers: map[string]ProviderConfig{
			"header-merge-test": {
				BaseURL: "http://localhost",
				ApiKey:  "key",
				Api:     "openai-completions",
				Headers: map[string]string{
					"X-Provider": "provider-value",
					"X-Shared":   "from-provider",
				},
				Models: []ModelDefinition{
					{
						ID: "header-model",
						Headers: map[string]string{
							"X-Model":  "model-value",
							"X-Shared": "from-model",
						},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(modelsPath, data, 0644)

	registry, _ := setupTestModelRegistry(t, modelsPath)

	m := registry.Find("header-merge-test", "header-model")
	if m == nil {
		t.Fatal("model not found")
	}
	if m.Headers["X-Provider"] != "provider-value" {
		t.Errorf("expected provider header, got %q", m.Headers["X-Provider"])
	}
	if m.Headers["X-Model"] != "model-value" {
		t.Errorf("expected model header, got %q", m.Headers["X-Model"])
	}
	// Model header should win on conflict.
	if m.Headers["X-Shared"] != "from-model" {
		t.Errorf("expected model header to override, got %q", m.Headers["X-Shared"])
	}
}

func TestModelRegistry_CompatOverride(t *testing.T) {
	boolTrue := true
	ai.RegisterModel(&ai.Model{
		ID:            "compat-target",
		Name:          "Compat Model",
		API:           ai.ApiOpenAICompletions,
		Provider:      "test-provider-compat",
		BaseURL:       "https://api.test.com",
		Input:         []ai.InputModality{ai.InputText},
		Cost:          ai.ModelCost{},
		ContextWindow: 128000,
		MaxTokens:     4096,
		Compat: &ai.OpenAICompletionsCompat{
			SupportsStore: &boolTrue,
		},
	})

	tmpDir := t.TempDir()
	modelsPath := filepath.Join(tmpDir, "models.json")

	boolFalse := false
	config := ModelsConfig{
		Providers: map[string]ProviderConfig{
			"test-provider-compat": {
				ModelOverrides: map[string]ModelOverride{
					"compat-target": {
						Compat: &CompatConfig{
							SupportsStore:  &boolFalse,
							MaxTokensField: "max_tokens",
						},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(modelsPath, data, 0644)

	registry, _ := setupTestModelRegistry(t, modelsPath)

	m := registry.Find("test-provider-compat", "compat-target")
	if m == nil {
		t.Fatal("model not found")
	}

	compat := m.GetOpenAICompletionsCompat()
	if compat == nil {
		t.Fatal("expected OpenAI completions compat")
	}
	if compat.SupportsStore == nil || *compat.SupportsStore != false {
		t.Error("expected supportsStore=false after override")
	}
	if compat.MaxTokensField != ai.MaxTokensFieldMaxTokens {
		t.Errorf("expected maxTokensField 'max_tokens', got %q", compat.MaxTokensField)
	}
}

func TestModelRegistry_DefaultModelsJsonPath(t *testing.T) {
	path := DefaultModelsJsonPath("/home/user/.config/fir")
	expected := filepath.Join("/home/user/.config/fir", "models.json")
	if path != expected {
		t.Errorf("expected %q, got %q", expected, path)
	}
}

func TestModelRegistry_CustomModelCapabilities(t *testing.T) {
	tmp := t.TempDir()
	modelsPath := filepath.Join(tmp, "models.json")
	content := `{
  "providers": {
    "proxy": {
      "baseUrl": "https://proxy.example.com",
      "apiKey": "test-key",
      "api": "anthropic-messages",
      "models": [
        {
          "id": "my-claude-proxy",
          "serverTools": ["web_search_20260209", "web_fetch_20260209"],
          "compaction": true,
          "adaptiveThinking": true,
          "sweScore": 73.5
        }
      ]
    }
  }
}`
	if err := os.WriteFile(modelsPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	registry, _ := setupTestModelRegistry(t, modelsPath)
	m := registry.Find("proxy", "my-claude-proxy")
	if m == nil {
		t.Fatal("expected custom model")
	}
	if len(m.ServerTools) != 2 || m.ServerTools[0] != "web_search_20260209" {
		t.Fatalf("unexpected serverTools: %#v", m.ServerTools)
	}
	if !m.Compaction {
		t.Fatal("expected compaction=true")
	}
	if !m.AdaptiveThinking {
		t.Fatal("expected adaptiveThinking=true")
	}
	if m.SWEScore != 73.5 {
		t.Fatalf("expected sweScore=73.5, got %v", m.SWEScore)
	}
	if m.SWEInferred {
		t.Fatal("custom sweScore should be exact, not inferred")
	}
}

func TestModelRegistry_ModelOverrideCapabilities(t *testing.T) {
	provider := "test-provider-override-capabilities"
	id := "override-me"
	ai.RegisterModel(&ai.Model{ID: id, Name: id, API: ai.ApiAnthropicMessages, Provider: provider, BaseURL: "https://api.example.com", Input: []ai.InputModality{ai.InputText}, Cost: ai.ModelCost{}, ContextWindow: 128000, MaxTokens: 4096})

	tmp := t.TempDir()
	modelsPath := filepath.Join(tmp, "models.json")
	content := `{
  "providers": {
    "` + provider + `": {
      "modelOverrides": {
        "` + id + `": {
          "serverTools": ["web_search_20250305"],
          "compaction": true,
          "adaptiveThinking": true,
          "sweScore": 81.2
        }
      }
    }
  }
}`
	if err := os.WriteFile(modelsPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	registry, _ := setupTestModelRegistry(t, modelsPath)
	m := registry.Find(provider, id)
	if m == nil {
		t.Fatal("expected overridden model")
	}
	if !m.Compaction {
		t.Fatal("expected compaction override=true")
	}
	if !m.AdaptiveThinking {
		t.Fatal("expected adaptiveThinking override=true")
	}
	if len(m.ServerTools) != 1 || m.ServerTools[0] != "web_search_20250305" {
		t.Fatalf("unexpected serverTools override: %#v", m.ServerTools)
	}
	if m.SWEScore != 81.2 {
		t.Fatalf("expected sweScore override=81.2, got %v", m.SWEScore)
	}
}

func TestModelRegistry_AddRuntimeModel(t *testing.T) {
	dir := t.TempDir()
	authStorage := auth.NewAuthStorage(filepath.Join(dir, "auth.json"))
	r := NewModelRegistry(authStorage, "")

	m := &ai.Model{
		Provider: "amazon-bedrock", ID: "arn:aws:bedrock:us-east-1:1:foo/bar",
		Name: "Custom", API: ai.ApiBedrockConverseStream,
		ContextWindow: 200000, MaxTokens: 8192,
	}
	r.AddRuntimeModel(m)

	got := r.Find("amazon-bedrock", "arn:aws:bedrock:us-east-1:1:foo/bar")
	if got == nil || got.Name != "Custom" {
		t.Fatalf("expected runtime model registered, got %#v", got)
	}

	// Replace path: same provider+id keeps a single entry.
	before := len(r.GetAll())
	m2 := *m
	m2.Name = "Renamed"
	r.AddRuntimeModel(&m2)
	if got := len(r.GetAll()); got != before {
		t.Errorf("expected %d models after replace, got %d", before, got)
	}
	if updated := r.Find("amazon-bedrock", m.ID); updated == nil || updated.Name != "Renamed" {
		t.Errorf("expected replaced model, got %#v", updated)
	}

	// Nil is a no-op.
	r.AddRuntimeModel(nil)
}
