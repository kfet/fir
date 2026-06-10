package providers

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

func azureTestModel() *ai.Model {
	return &ai.Model{
		ID:            "gpt-4o",
		Name:          "GPT-4o",
		API:           ai.ApiAzureOpenAIResponses,
		Provider:      ai.ProviderAzureOpenAIResponses,
		BaseURL:       "",
		Reasoning:     false,
		Input:         []ai.InputModality{ai.InputText},
		Cost:          ai.ModelCost{Input: 3.0, Output: 15.0},
		ContextWindow: 200000,
		MaxTokens:     8192,
	}
}

func azureTestContext() ai.Context {
	return ai.Context{
		SystemPrompt: "You are a test assistant.",
		Messages:     []ai.Message{ai.NewUserMsg("hello", 0)},
	}
}

func TestParseDeploymentNameMap(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected map[string]string
	}{
		{"empty", "", map[string]string{}},
		{"single", "gpt-4o=my-deploy", map[string]string{"gpt-4o": "my-deploy"}},
		{"multiple", "gpt-4o=deploy1,o1=deploy2", map[string]string{"gpt-4o": "deploy1", "o1": "deploy2"}},
		{"whitespace", " gpt-4o = deploy1 , o1 = deploy2 ", map[string]string{"gpt-4o": "deploy1", "o1": "deploy2"}},
		{"skip invalid", "invalid,gpt-4o=deploy1,=bad,bad=", map[string]string{"gpt-4o": "deploy1"}},
		{"empty entries", ",,gpt-4o=deploy1,,", map[string]string{"gpt-4o": "deploy1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDeploymentNameMap(tt.input)
			if len(got) != len(tt.expected) {
				t.Fatalf("expected %d entries, got %d: %v", len(tt.expected), len(got), got)
			}
			for k, v := range tt.expected {
				if got[k] != v {
					t.Errorf("key %q: expected %q, got %q", k, v, got[k])
				}
			}
		})
	}
}

func TestResolveDeploymentName(t *testing.T) {
	model := azureTestModel()

	// Override takes precedence
	got := resolveDeploymentName(model, "my-override")
	if got != "my-override" {
		t.Errorf("expected 'my-override', got %q", got)
	}

	// Environment map
	os.Setenv("AZURE_OPENAI_DEPLOYMENT_NAME_MAP", "gpt-4o=env-deploy")
	defer os.Unsetenv("AZURE_OPENAI_DEPLOYMENT_NAME_MAP")
	got = resolveDeploymentName(model, "")
	if got != "env-deploy" {
		t.Errorf("expected 'env-deploy', got %q", got)
	}

	// Falls back to model ID
	os.Unsetenv("AZURE_OPENAI_DEPLOYMENT_NAME_MAP")
	got = resolveDeploymentName(model, "")
	if got != "gpt-4o" {
		t.Errorf("expected 'gpt-4o', got %q", got)
	}
}

func TestResolveAzureConfig_BaseURL(t *testing.T) {
	model := azureTestModel()

	baseURL, apiVer, err := resolveAzureConfig(model, "https://my-resource.openai.azure.com/openai/v1", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if baseURL != "https://my-resource.openai.azure.com/openai/v1" {
		t.Errorf("unexpected base URL: %s", baseURL)
	}
	if apiVer != defaultAzureAPIVersion {
		t.Errorf("expected default API version, got %q", apiVer)
	}
}

func TestResolveAzureConfig_ResourceName(t *testing.T) {
	model := azureTestModel()

	baseURL, _, err := resolveAzureConfig(model, "", "my-resource", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if baseURL != "https://my-resource.openai.azure.com/openai/v1" {
		t.Errorf("unexpected base URL: %s", baseURL)
	}
}

func TestResolveAzureConfig_EnvVars(t *testing.T) {
	model := azureTestModel()
	os.Setenv("AZURE_OPENAI_BASE_URL", "https://env-resource.openai.azure.com/openai/v1")
	os.Setenv("AZURE_OPENAI_API_VERSION", "2024-02-01")
	defer func() {
		os.Unsetenv("AZURE_OPENAI_BASE_URL")
		os.Unsetenv("AZURE_OPENAI_API_VERSION")
	}()

	baseURL, apiVer, err := resolveAzureConfig(model, "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if baseURL != "https://env-resource.openai.azure.com/openai/v1" {
		t.Errorf("unexpected base URL: %s", baseURL)
	}
	if apiVer != "2024-02-01" {
		t.Errorf("expected '2024-02-01', got %q", apiVer)
	}
}

func TestResolveAzureConfig_ModelBaseURL(t *testing.T) {
	model := azureTestModel()
	model.BaseURL = "https://model-base.openai.azure.com/openai/v1"

	baseURL, _, err := resolveAzureConfig(model, "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if baseURL != "https://model-base.openai.azure.com/openai/v1" {
		t.Errorf("unexpected base URL: %s", baseURL)
	}
}

func TestResolveAzureConfig_NoBaseURL(t *testing.T) {
	model := azureTestModel()
	model.BaseURL = ""

	// Clear env vars
	os.Unsetenv("AZURE_OPENAI_BASE_URL")
	os.Unsetenv("AZURE_OPENAI_RESOURCE_NAME")

	_, _, err := resolveAzureConfig(model, "", "", "")
	if err == nil {
		t.Error("expected error when no base URL is available")
	}
}

func TestResolveAzureConfig_TrailingSlash(t *testing.T) {
	model := azureTestModel()

	baseURL, _, err := resolveAzureConfig(model, "https://resource.openai.azure.com/openai/v1///", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if baseURL != "https://resource.openai.azure.com/openai/v1" {
		t.Errorf("unexpected base URL: %s", baseURL)
	}
}

func TestStreamSimpleAzureOpenAIResponses_NoKey(t *testing.T) {
	model := azureTestModel()

	// Clear all Azure env vars
	os.Unsetenv("AZURE_OPENAI_API_KEY")

	result := StreamSimpleAzureOpenAIResponses(nil, model, azureTestContext(), nil)
	if result == nil {
		t.Fatal("expected non-nil stream")
	}

	var lastEvt *ai.AssistantMessageEvent
	for evt := range result.Events {
		e := evt
		lastEvt = &e
	}
	if lastEvt == nil {
		t.Fatal("expected at least one event")
	}
	if lastEvt.Type != ai.EventError {
		t.Errorf("expected error event, got %q", lastEvt.Type)
	}
}

func TestBuildAzureResponsesBody_Basic(t *testing.T) {
	model := azureTestModel()
	ctx := azureTestContext()
	body, err := buildAzureResponsesBody(model, ctx, nil, "my-deploy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if parsed["model"] != "my-deploy" {
		t.Errorf("expected model 'my-deploy', got %v", parsed["model"])
	}
	if parsed["stream"] != true {
		t.Error("expected stream=true")
	}
}

func TestBuildAzureResponsesBody_WithOptions(t *testing.T) {
	model := azureTestModel()
	ctx := azureTestContext()
	maxTokens := 1000
	temp := 0.5
	opts := &ai.StreamOptions{
		MaxTokens:   &maxTokens,
		Temperature: &temp,
		SessionID:   "test-session",
		Headers:     map[string]string{},
	}

	body, err := buildAzureResponsesBody(model, ctx, opts, "deploy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if parsed["max_output_tokens"] != float64(1000) {
		t.Errorf("expected max_output_tokens=1000, got %v", parsed["max_output_tokens"])
	}
	if parsed["temperature"] != 0.5 {
		t.Errorf("expected temperature=0.5, got %v", parsed["temperature"])
	}
	if parsed["prompt_cache_key"] != "test-session" {
		t.Errorf("expected prompt_cache_key='test-session', got %v", parsed["prompt_cache_key"])
	}
}

func TestBuildAzureResponsesBody_Reasoning(t *testing.T) {
	model := azureTestModel()
	model.Reasoning = true
	ctx := azureTestContext()
	opts := &ai.StreamOptions{
		ReasoningEffort: "high",
		Headers:         map[string]string{},
	}

	body, err := buildAzureResponsesBody(model, ctx, opts, "deploy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	reasoning, ok := parsed["reasoning"].(map[string]any)
	if !ok {
		t.Fatal("expected reasoning config")
	}
	if reasoning["effort"] != "high" {
		t.Errorf("expected effort 'high', got %v", reasoning["effort"])
	}
	if reasoning["summary"] != "auto" {
		t.Errorf("expected summary 'auto', got %v", reasoning["summary"])
	}
}

func TestRegisterAzureOpenAIResponses(t *testing.T) {
	reg := ai.NewRegistry()
	RegisterAzureOpenAIResponses(reg)

	provider := reg.GetApiProvider(ai.ApiAzureOpenAIResponses)
	if provider == nil {
		t.Fatal("expected azure-openai-responses provider to be registered")
	}
}
