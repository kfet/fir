package providers

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/kfet/pi-go/pkg/ai"
)

func codexTestModel() *ai.Model {
	return &ai.Model{
		ID:            "codex-mini-latest",
		Name:          "Codex Mini",
		Api:           ai.ApiOpenAICodexResponses,
		Provider:      ai.ProviderOpenAICodex,
		BaseURL:       "",
		Reasoning:     true,
		Input:         []ai.InputModality{ai.InputText},
		Cost:          ai.ModelCost{Input: 1.0, Output: 4.0},
		ContextWindow: 200000,
		MaxTokens:     100000,
	}
}

func makeTestJWT(accountID string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims := map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": accountID,
		},
	}
	claimsJSON, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(claimsJSON)
	return header + "." + payload + ".signature"
}

func TestExtractAccountID_Valid(t *testing.T) {
	token := makeTestJWT("acc_12345")
	id, err := extractAccountID(token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "acc_12345" {
		t.Errorf("expected 'acc_12345', got %q", id)
	}
}

func TestExtractAccountID_InvalidToken(t *testing.T) {
	_, err := extractAccountID("not-a-jwt")
	if err == nil {
		t.Error("expected error for invalid token")
	}
}

func TestExtractAccountID_NoClaim(t *testing.T) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user"}`))
	token := header + "." + payload + ".sig"

	_, err := extractAccountID(token)
	if err == nil {
		t.Error("expected error for missing auth claim")
	}
}

func TestResolveCodexURL(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		expected string
	}{
		{"empty defaults", "", defaultCodexBaseURL + "/codex/responses"},
		{"with trailing slash", "https://example.com/", "https://example.com/codex/responses"},
		{"already has /codex", "https://example.com/codex", "https://example.com/codex/responses"},
		{"already has full path", "https://example.com/codex/responses", "https://example.com/codex/responses"},
		{"custom base", "https://custom.api.com/v1", "https://custom.api.com/v1/codex/responses"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveCodexURL(tt.baseURL)
			if got != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestClampCodexReasoningEffort(t *testing.T) {
	tests := []struct {
		modelID  string
		effort   string
		expected string
	}{
		{"gpt-5.2", "minimal", "low"},
		{"gpt-5.3-preview", "minimal", "low"},
		{"gpt-5.2", "high", "high"},
		{"gpt-5.1", "xhigh", "high"},
		{"gpt-5.1", "medium", "medium"},
		{"gpt-5.1-codex-mini", "high", "high"},
		{"gpt-5.1-codex-mini", "xhigh", "high"},
		{"gpt-5.1-codex-mini", "low", "medium"},
		{"codex-mini-latest", "high", "high"},
		{"provider/gpt-5.2", "minimal", "low"},
	}
	for _, tt := range tests {
		t.Run(tt.modelID+"_"+tt.effort, func(t *testing.T) {
			got := clampCodexReasoningEffort(tt.modelID, tt.effort)
			if got != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestIsRetryableCodexError(t *testing.T) {
	if !isRetryableCodexError(429, "") {
		t.Error("expected 429 to be retryable")
	}
	if !isRetryableCodexError(503, "") {
		t.Error("expected 503 to be retryable")
	}
	if isRetryableCodexError(400, "") {
		t.Error("expected 400 to not be retryable")
	}
	if !isRetryableCodexError(200, "rate limit exceeded") {
		t.Error("expected 'rate limit exceeded' to be retryable")
	}
	if !isRetryableCodexError(200, "service unavailable") {
		t.Error("expected 'service unavailable' to be retryable")
	}
}

func TestMapCodexEvent_Error(t *testing.T) {
	data := `{"type":"error","code":"rate_limit","message":"Too many requests"}`
	result := mapCodexEvent(data)
	if result != data {
		t.Errorf("expected error to pass through, got %q", result)
	}
}

func TestMapCodexEvent_ResponseFailed(t *testing.T) {
	data := `{"type":"response.failed","response":{"error":{"message":"Usage limit reached"}}}`
	result := mapCodexEvent(data)
	var parsed map[string]any
	json.Unmarshal([]byte(result), &parsed)
	if parsed["type"] != "error" {
		t.Errorf("expected type 'error', got %v", parsed["type"])
	}
}

func TestMapCodexEvent_ResponseDone(t *testing.T) {
	data := `{"type":"response.done","response":{"status":"completed","usage":{"input_tokens":100}}}`
	result := mapCodexEvent(data)
	var parsed map[string]any
	json.Unmarshal([]byte(result), &parsed)
	if parsed["type"] != "response.completed" {
		t.Errorf("expected type 'response.completed', got %v", parsed["type"])
	}
}

func TestMapCodexEvent_PassThrough(t *testing.T) {
	data := `{"type":"response.output_text.delta","delta":"hello"}`
	result := mapCodexEvent(data)
	if result != data {
		t.Errorf("expected pass-through, got %q", result)
	}
}

func TestNormalizeCodexStatus(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"completed", "completed"},
		{"failed", "failed"},
		{"in_progress", "in_progress"},
		{"unknown", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := normalizeCodexStatus(tt.input)
		if got != tt.expected {
			t.Errorf("normalizeCodexStatus(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestBuildCodexRequestBody_Basic(t *testing.T) {
	model := codexTestModel()
	ctx := ai.Context{
		SystemPrompt: "You are a coding assistant.",
		Messages:     []ai.Message{ai.NewUserMsg("hello", 0)},
	}

	body, err := buildCodexRequestBody(model, ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if parsed["model"] != "codex-mini-latest" {
		t.Errorf("expected model 'codex-mini-latest', got %v", parsed["model"])
	}
	if parsed["instructions"] != "You are a coding assistant." {
		t.Errorf("expected instructions, got %v", parsed["instructions"])
	}
	if parsed["stream"] != true {
		t.Error("expected stream=true")
	}
	if parsed["store"] != false {
		t.Error("expected store=false")
	}
	if parsed["tool_choice"] != "auto" {
		t.Error("expected tool_choice=auto")
	}
}

func TestBuildCodexRequestBody_WithReasoning(t *testing.T) {
	model := codexTestModel()
	ctx := ai.Context{
		SystemPrompt: "test",
		Messages:     []ai.Message{ai.NewUserMsg("hello", 0)},
	}
	opts := &ai.StreamOptions{
		ReasoningEffort: "high",
		Headers:         map[string]string{},
	}

	body, err := buildCodexRequestBody(model, ctx, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]any
	json.Unmarshal(body, &parsed)

	reasoning, ok := parsed["reasoning"].(map[string]any)
	if !ok {
		t.Fatal("expected reasoning config")
	}
	if reasoning["effort"] != "high" {
		t.Errorf("expected effort 'high', got %v", reasoning["effort"])
	}
}

func TestBuildCodexHeaders(t *testing.T) {
	headers := buildCodexHeaders(
		map[string]string{"X-Custom": "value"},
		&ai.StreamOptions{SessionID: "sess-123", Headers: map[string]string{}},
		"acc_test",
		"test-token",
	)

	if headers["Authorization"] != "Bearer test-token" {
		t.Errorf("expected Bearer auth, got %q", headers["Authorization"])
	}
	if headers["chatgpt-account-id"] != "acc_test" {
		t.Errorf("expected account ID, got %q", headers["chatgpt-account-id"])
	}
	if headers["OpenAI-Beta"] != "responses=experimental" {
		t.Errorf("expected beta header, got %q", headers["OpenAI-Beta"])
	}
	if headers["originator"] != "pi" {
		t.Errorf("expected originator 'pi', got %q", headers["originator"])
	}
	if headers["session_id"] != "sess-123" {
		t.Errorf("expected session_id, got %q", headers["session_id"])
	}
	if headers["X-Custom"] != "value" {
		t.Errorf("expected custom header, got %q", headers["X-Custom"])
	}
}

func TestRegisterOpenAICodexResponses(t *testing.T) {
	reg := ai.NewRegistry()
	RegisterOpenAICodexResponses(reg)

	provider := reg.GetApiProvider(ai.ApiOpenAICodexResponses)
	if provider == nil {
		t.Fatal("expected openai-codex-responses provider to be registered")
	}
}

func TestStreamSimpleOpenAICodexResponses_NoKey(t *testing.T) {
	model := codexTestModel()

	result := StreamSimpleOpenAICodexResponses(nil, model, ai.Context{}, nil)
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
