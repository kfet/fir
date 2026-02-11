// Ported from: packages/ai/src/providers/anthropic.ts
// Upstream hash: 1caadb2e
package providers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kfet/pi-go/pkg/ai"
)

func anthropicModel(serverURL string) *ai.Model {
	m := testModel(serverURL, ai.ApiAnthropicMessages, ai.ProviderAnthropic)
	m.ID = "claude-sonnet-4-20250514"
	return m
}

func TestAnthropic_SimpleResponse(t *testing.T) {
	srv := mockSSEServer(t, "anthropic_simple_response.sse")
	defer srv.Close()

	model := anthropicModel(srv.URL)
	ctx := ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("Hello!", 1000)},
	}
	opts := &ai.StreamOptions{ApiKey: "test-key"}

	stream := StreamAnthropic(context.Background(), model, ctx, opts)
	events := collectEvents(t, stream)

	if len(events) < 4 {
		t.Fatalf("expected at least 4 events, got %d", len(events))
	}
	if events[0].Type != ai.EventStart {
		t.Errorf("expected start, got %s", events[0].Type)
	}
	last := events[len(events)-1]
	if last.Type != ai.EventDone {
		t.Errorf("expected done, got %s", last.Type)
	}

	result := stream.Result()
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.StopReason != ai.StopReasonStop {
		t.Errorf("expected stop, got %s", result.StopReason)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Content))
	}
	if !result.Content[0].IsText() {
		t.Error("expected text content")
	}
	if result.Content[0].Text.Text != "Hello! How can I help you?" {
		t.Errorf("unexpected text: %q", result.Content[0].Text.Text)
	}
	if result.Usage.Input != 25 {
		t.Errorf("expected input=25, got %d", result.Usage.Input)
	}
}

func TestAnthropic_ToolCall(t *testing.T) {
	srv := mockSSEServer(t, "anthropic_tool_call.sse")
	defer srv.Close()

	model := anthropicModel(srv.URL)
	ctx := ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("Read test.txt", 1000)},
	}
	opts := &ai.StreamOptions{ApiKey: "test-key"}

	stream := StreamAnthropic(context.Background(), model, ctx, opts)
	events := collectEvents(t, stream)

	result := stream.Result()
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.StopReason != ai.StopReasonToolUse {
		t.Errorf("expected toolUse, got %s", result.StopReason)
	}
	if len(result.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(result.Content))
	}
	if !result.Content[0].IsText() {
		t.Error("expected text content")
	}
	if result.Content[0].Text.Text != "I'll read that file for you." {
		t.Errorf("unexpected text: %q", result.Content[0].Text.Text)
	}
	if !result.Content[1].IsToolCall() {
		t.Error("expected tool call content")
	}
	tc := result.Content[1].ToolCall
	if tc.ID != "toolu_01" {
		t.Errorf("expected ID toolu_01, got %s", tc.ID)
	}
	if tc.Name != "read" {
		t.Errorf("expected name read, got %s", tc.Name)
	}
	if tc.Arguments["path"] != "test.txt" {
		t.Errorf("expected path=test.txt, got %v", tc.Arguments["path"])
	}

	var hasToolStart, hasToolEnd bool
	for _, evt := range events {
		if evt.Type == ai.EventToolcallStart {
			hasToolStart = true
		}
		if evt.Type == ai.EventToolcallEnd {
			hasToolEnd = true
		}
	}
	if !hasToolStart {
		t.Error("missing toolcall_start event")
	}
	if !hasToolEnd {
		t.Error("missing toolcall_end event")
	}
}

func TestAnthropic_Thinking(t *testing.T) {
	srv := mockSSEServer(t, "anthropic_thinking.sse")
	defer srv.Close()

	model := anthropicModel(srv.URL)
	ctx := ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("What is the meaning of life?", 1000)},
	}
	opts := &ai.StreamOptions{ApiKey: "test-key"}

	stream := StreamAnthropic(context.Background(), model, ctx, opts)
	events := collectEvents(t, stream)

	result := stream.Result()
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(result.Content))
	}
	if !result.Content[0].IsThinking() {
		t.Error("expected thinking content")
	}
	if result.Content[0].Thinking.Thinking != "Let me think about this..." {
		t.Errorf("unexpected thinking: %q", result.Content[0].Thinking.Thinking)
	}
	if result.Content[0].Thinking.ThinkingSignature != "sig123" {
		t.Errorf("expected signature sig123, got %q", result.Content[0].Thinking.ThinkingSignature)
	}
	if !result.Content[1].IsText() {
		t.Error("expected text content")
	}
	if result.Content[1].Text.Text != "The answer is 42." {
		t.Errorf("unexpected text: %q", result.Content[1].Text.Text)
	}

	var hasThinkingStart, hasThinkingEnd bool
	for _, evt := range events {
		if evt.Type == ai.EventThinkingStart {
			hasThinkingStart = true
		}
		if evt.Type == ai.EventThinkingEnd {
			hasThinkingEnd = true
		}
	}
	if !hasThinkingStart {
		t.Error("missing thinking_start event")
	}
	if !hasThinkingEnd {
		t.Error("missing thinking_end event")
	}
}

func TestAnthropic_StreamingError(t *testing.T) {
	srv := mockSSEServer(t, "anthropic_error.sse")
	defer srv.Close()

	model := anthropicModel(srv.URL)
	ctx := ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("Hello!", 1000)},
	}
	opts := &ai.StreamOptions{ApiKey: "test-key"}

	stream := StreamAnthropic(context.Background(), model, ctx, opts)
	events := collectEvents(t, stream)

	result := stream.Result()
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.StopReason != ai.StopReasonError {
		t.Errorf("expected error, got %s", result.StopReason)
	}
	if result.ErrorMessage != "Overloaded" {
		t.Errorf("expected 'Overloaded', got %q", result.ErrorMessage)
	}

	found := false
	for _, evt := range events {
		if evt.Type == ai.EventError {
			found = true
		}
	}
	if !found {
		t.Error("missing error event")
	}
}

func TestAnthropic_NoAPIKey(t *testing.T) {
	model := &ai.Model{
		ID:       "test-model",
		Api:      ai.ApiAnthropicMessages,
		Provider: "unknown-provider-no-key",
		BaseURL:  "http://localhost:1",
	}
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("hi", 1000)}}

	stream := StreamAnthropic(context.Background(), model, ctx, nil)
	events := collectEvents(t, stream)

	result := stream.Result()
	if result == nil {
		t.Fatal("expected result")
	}
	if result.StopReason != ai.StopReasonError {
		t.Errorf("expected error, got %s", result.StopReason)
	}

	found := false
	for _, e := range events {
		if e.Type == ai.EventError {
			found = true
		}
	}
	if !found {
		t.Error("expected error event")
	}
}

func TestAnthropic_RequestPayload(t *testing.T) {
	srv, captured := captureRequest(t, "anthropic_simple_response.sse")
	defer srv.Close()

	model := anthropicModel(srv.URL)
	ctx := ai.Context{
		SystemPrompt: "You are a helpful assistant.",
		Messages:     []ai.Message{ai.NewUserMsg("Hello!", 1000)},
		Tools: []ai.Tool{{
			Name:        "read",
			Description: "Read a file",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
				"required": []string{"path"},
			},
		}},
	}
	opts := &ai.StreamOptions{ApiKey: "test-key"}

	stream := StreamAnthropic(context.Background(), model, ctx, opts)
	collectEvents(t, stream)

	if len(*captured) == 0 {
		t.Fatal("expected captured request body")
	}

	var payload map[string]any
	if err := json.Unmarshal(*captured, &payload); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if payload["model"] != "claude-sonnet-4-20250514" {
		t.Errorf("expected model claude-sonnet-4-20250514, got %v", payload["model"])
	}
	if payload["stream"] != true {
		t.Error("expected stream=true")
	}
	system, ok := payload["system"].([]any)
	if !ok || len(system) == 0 {
		t.Error("expected system prompt")
	}
	tools, ok := payload["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Error("expected tools")
	}
}

func TestAnthropic_MapStopReasons(t *testing.T) {
	tests := []struct {
		input string
		want  ai.StopReason
	}{
		{"end_turn", ai.StopReasonStop},
		{"max_tokens", ai.StopReasonLength},
		{"tool_use", ai.StopReasonToolUse},
		{"refusal", ai.StopReasonError},
		{"pause_turn", ai.StopReasonStop},
		{"stop_sequence", ai.StopReasonStop},
		{"sensitive", ai.StopReasonError},
		{"unknown", ai.StopReasonError},
	}
	for _, tt := range tests {
		got := mapAnthropicStopReason(tt.input)
		if got != tt.want {
			t.Errorf("mapStopReason(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestAnthropic_NormalizeToolCallID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple_id", "simple_id"},
		{"id-with-dashes", "id-with-dashes"},
		{"id|with|pipes", "id_with_pipes"},
		{"a very long id that exceeds sixty four characters and should be truncated to fit within the limit", "a_very_long_id_that_exceeds_sixty_four_characters_and_should_be_"},
	}
	for _, tt := range tests {
		got := normalizeAnthropicToolCallID(tt.input, nil, nil)
		if got != tt.want {
			t.Errorf("normalizeToolCallID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestAnthropic_ClaudeCodeNames(t *testing.T) {
	if toClaudeCodeName("read") != "Read" {
		t.Error("expected Read for read")
	}
	if toClaudeCodeName("bash") != "Bash" {
		t.Error("expected Bash for bash")
	}
	if toClaudeCodeName("custom_tool") != "custom_tool" {
		t.Error("expected custom_tool to pass through")
	}

	tools := []ai.Tool{{Name: "read"}, {Name: "bash"}}
	if fromClaudeCodeName("Read", tools) != "read" {
		t.Error("expected read from Read")
	}
	if fromClaudeCodeName("Bash", tools) != "bash" {
		t.Error("expected bash from Bash")
	}
}

func TestAnthropic_FromClaudeCodeNameFallback(t *testing.T) {
	tools := []ai.Tool{{Name: "read"}, {Name: "bash"}}
	got := fromClaudeCodeName("UnknownTool", tools)
	if got != "UnknownTool" {
		t.Errorf("expected UnknownTool, got %s", got)
	}
}

func TestAnthropic_IsOAuthToken(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"sk-ant-api03-xxx", false},
		{"sk-ant-oat-xxx", true},
		{"sk-ant-oat01-prefix-xxx", true},
		{"regular-key", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isOAuthTokenStr(tt.key); got != tt.want {
			t.Errorf("isOAuthToken(%q) = %v, want %v", tt.key, got, tt.want)
		}
	}
}

func TestAnthropic_SupportsAdaptiveThinking(t *testing.T) {
	tests := []struct {
		modelID string
		want    bool
	}{
		{"claude-opus-4.6-20260101", true},
		{"claude-opus-4-6-20260101", true},
		{"claude-sonnet-4-20250514", false},
		{"claude-opus-4-20250514", false},
		{"gpt-4o", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := supportsAdaptiveThinking(tt.modelID); got != tt.want {
			t.Errorf("supportsAdaptiveThinking(%q) = %v, want %v", tt.modelID, got, tt.want)
		}
	}
}

func TestAnthropic_ThinkingLevelMapping(t *testing.T) {
	tests := []struct {
		level ai.ThinkingLevel
		want  string
	}{
		{ai.ThinkingMinimal, "low"},
		{ai.ThinkingLow, "low"},
		{ai.ThinkingMedium, "medium"},
		{ai.ThinkingHigh, "high"},
		{ai.ThinkingXHigh, "max"},
		{"", "high"}, // default
	}
	for _, tt := range tests {
		got := mapThinkingLevelToEffort(tt.level)
		if got != tt.want {
			t.Errorf("mapThinkingLevelToEffort(%q) = %q, want %q", tt.level, got, tt.want)
		}
	}
}

// --- Header tests ---

func TestAnthropic_OAuthHeaders(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet", BaseURL: "https://api.anthropic.com"}
	headers := buildAnthropicHeaders(model, "sk-ant-oat-test", true, nil)

	if headers["authorization"] != "Bearer sk-ant-oat-test" {
		t.Errorf("expected Bearer auth, got %q", headers["authorization"])
	}
	if headers["x-api-key"] != "" {
		t.Error("OAuth should not set x-api-key")
	}
	if !strings.Contains(headers["anthropic-beta"], "claude-code-20250219") {
		t.Error("OAuth should include claude-code beta flag")
	}
	if !strings.Contains(headers["user-agent"], "claude-cli/") {
		t.Error("OAuth should set claude-cli user-agent")
	}
}

func TestAnthropic_NonOAuthHeaders(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet", BaseURL: "https://api.anthropic.com"}
	headers := buildAnthropicHeaders(model, "sk-ant-api03-test", false, nil)

	if headers["x-api-key"] != "sk-ant-api03-test" {
		t.Errorf("expected x-api-key, got %q", headers["x-api-key"])
	}
	if headers["authorization"] != "" {
		t.Error("non-OAuth should not set authorization")
	}
	if strings.Contains(headers["anthropic-beta"], "claude-code-20250219") {
		t.Error("non-OAuth should not include claude-code beta flag")
	}
}

func TestAnthropic_BuildHeaders_ModelHeaders(t *testing.T) {
	model := &ai.Model{
		ID:      "claude-sonnet",
		BaseURL: "https://api.anthropic.com",
		Headers: map[string]string{"x-custom-model": "yes"},
	}
	headers := buildAnthropicHeaders(model, "test-key", false, nil)

	if headers["x-custom-model"] != "yes" {
		t.Errorf("expected model header x-custom-model=yes, got %q", headers["x-custom-model"])
	}
	if headers["x-api-key"] != "test-key" {
		t.Errorf("expected x-api-key, got %q", headers["x-api-key"])
	}
}

func TestAnthropic_BuildHeaders_InternalHeadersStripped(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet", BaseURL: "https://api.anthropic.com"}
	opts := &ai.StreamOptions{
		ApiKey: "test-key",
		Headers: map[string]string{
			"x-anthropic-thinking-enabled": "true",
			"x-anthropic-thinking-budget":  "2048",
			"x-custom-header":              "keep-me",
		},
	}
	headers := buildAnthropicHeaders(model, "test-key", false, opts)

	if _, ok := headers["x-anthropic-thinking-enabled"]; ok {
		t.Error("internal header x-anthropic-thinking-enabled should be stripped")
	}
	if _, ok := headers["x-anthropic-thinking-budget"]; ok {
		t.Error("internal header x-anthropic-thinking-budget should be stripped")
	}
	if headers["x-custom-header"] != "keep-me" {
		t.Error("custom headers should be preserved")
	}
}

// --- Build params tests ---

func TestAnthropic_OAuthToolNameMapping(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet", BaseURL: "https://api.anthropic.com", MaxTokens: 8192}
	ctx := ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("test", 1000)},
		Tools: []ai.Tool{
			{Name: "read", Description: "Read a file", Parameters: map[string]any{
				"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"},
			}},
		},
	}

	// OAuth mode: tools should be mapped to Claude Code names
	params := buildAnthropicParams(model, ctx, true, nil)
	tools, _ := params["tools"].([]map[string]any)
	if len(tools) == 0 {
		t.Fatal("expected tools")
	}
	if tools[0]["name"] != "Read" {
		t.Errorf("OAuth: expected 'Read', got %v", tools[0]["name"])
	}

	// Non-OAuth: tools should keep original names
	params = buildAnthropicParams(model, ctx, false, nil)
	tools, _ = params["tools"].([]map[string]any)
	if len(tools) == 0 {
		t.Fatal("expected tools")
	}
	if tools[0]["name"] != "read" {
		t.Errorf("non-OAuth: expected 'read', got %v", tools[0]["name"])
	}
}

func TestAnthropic_BuildParams_ThinkingEnabled(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet-4-20250514", BaseURL: "https://api.anthropic.com", MaxTokens: 8192}
	ctx := ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("test", 1000)},
	}

	maxTokens := 16000
	opts := &ai.StreamOptions{
		ApiKey:    "test-key",
		MaxTokens: &maxTokens,
		Headers: map[string]string{
			"x-anthropic-thinking-enabled": "true",
			"x-anthropic-thinking-budget":  "4096",
		},
	}

	params := buildAnthropicParams(model, ctx, false, opts)

	thinking, ok := params["thinking"].(map[string]any)
	if !ok {
		t.Fatal("expected thinking param")
	}
	if thinking["type"] != "enabled" {
		t.Errorf("expected type=enabled, got %v", thinking["type"])
	}
	if thinking["budget_tokens"] != 4096 {
		t.Errorf("expected budget_tokens=4096, got %v", thinking["budget_tokens"])
	}
}

func TestAnthropic_BuildParams_AdaptiveThinking(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet", BaseURL: "https://api.anthropic.com", MaxTokens: 8192}
	ctx := ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("test", 1000)},
	}

	opts := &ai.StreamOptions{
		ApiKey: "test-key",
		Headers: map[string]string{
			"x-anthropic-thinking-effort": "medium",
		},
	}

	params := buildAnthropicParams(model, ctx, false, opts)

	thinking, ok := params["thinking"].(map[string]any)
	if !ok {
		t.Fatal("expected thinking param")
	}
	if thinking["type"] != "adaptive" {
		t.Errorf("expected type=adaptive, got %v", thinking["type"])
	}
	outputConfig, ok := params["output_config"].(map[string]any)
	if !ok {
		t.Fatal("expected output_config")
	}
	if outputConfig["effort"] != "medium" {
		t.Errorf("expected effort=medium, got %v", outputConfig["effort"])
	}
}

func TestAnthropic_BuildParams_Temperature(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet", BaseURL: "https://api.anthropic.com", MaxTokens: 8192}
	ctx := ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("test", 1000)},
	}

	temp := 0.5
	opts := &ai.StreamOptions{
		ApiKey:      "test-key",
		Temperature: &temp,
	}

	params := buildAnthropicParams(model, ctx, false, opts)

	if params["temperature"] != 0.5 {
		t.Errorf("expected temperature=0.5, got %v", params["temperature"])
	}
}

func TestAnthropic_BuildParams_OAuthSystemPrompt(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet", BaseURL: "https://api.anthropic.com", MaxTokens: 8192}
	ctx := ai.Context{
		SystemPrompt: "Be helpful.",
		Messages:     []ai.Message{ai.NewUserMsg("test", 1000)},
	}

	// OAuth mode: should prepend Claude Code system prompt
	params := buildAnthropicParams(model, ctx, true, nil)
	system, ok := params["system"].([]map[string]any)
	if !ok {
		t.Fatal("expected system to be []map[string]any")
	}
	if len(system) != 2 {
		t.Fatalf("expected 2 system blocks (CC + user), got %d", len(system))
	}
	if !strings.Contains(system[0]["text"].(string), "Claude Code") {
		t.Error("first system block should be Claude Code prompt")
	}
	if system[1]["text"] != "Be helpful." {
		t.Errorf("second system block should be user prompt, got %v", system[1]["text"])
	}

	// Non-OAuth: only user system prompt
	params = buildAnthropicParams(model, ctx, false, nil)
	system = params["system"].([]map[string]any)
	if len(system) != 1 {
		t.Fatalf("expected 1 system block, got %d", len(system))
	}
	if system[0]["text"] != "Be helpful." {
		t.Errorf("expected 'Be helpful.', got %v", system[0]["text"])
	}
}

func TestAnthropic_BuildParams_NoSystemPrompt(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet", BaseURL: "https://api.anthropic.com", MaxTokens: 8192}
	ctx := ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("test", 1000)},
	}

	params := buildAnthropicParams(model, ctx, false, nil)
	if params["system"] != nil {
		t.Error("expected no system param when no system prompt")
	}
}

// --- Cache control tests ---

func TestAnthropic_CacheControlBlock(t *testing.T) {
	// Short retention on anthropic.com
	cc := cacheControlBlock("https://api.anthropic.com", ai.CacheShort)
	if cc["type"] != "ephemeral" {
		t.Errorf("expected ephemeral, got %v", cc["type"])
	}
	if cc["ttl"] != nil {
		t.Error("short retention should not have ttl")
	}

	// Long retention on anthropic.com
	cc = cacheControlBlock("https://api.anthropic.com", ai.CacheLong)
	if cc["ttl"] != "1h" {
		t.Errorf("expected 1h ttl, got %v", cc["ttl"])
	}

	// Long retention on non-anthropic
	cc = cacheControlBlock("https://custom.proxy.com", ai.CacheLong)
	if cc["ttl"] != nil {
		t.Error("non-anthropic should not have ttl even with long retention")
	}
}

func TestAnthropic_ResolveCacheRetention(t *testing.T) {
	// Explicit value takes precedence
	if got := resolveCacheRetention(ai.CacheLong); got != ai.CacheLong {
		t.Errorf("expected CacheLong, got %v", got)
	}
	if got := resolveCacheRetention(ai.CacheNone); got != ai.CacheNone {
		t.Errorf("expected CacheNone, got %v", got)
	}

	// Default with no env var
	t.Setenv("PI_CACHE_RETENTION", "")
	if got := resolveCacheRetention(""); got != ai.CacheShort {
		t.Errorf("expected CacheShort default, got %v", got)
	}

	// Env var override
	t.Setenv("PI_CACHE_RETENTION", "long")
	if got := resolveCacheRetention(""); got != ai.CacheLong {
		t.Errorf("expected CacheLong from env, got %v", got)
	}
}

// --- Helper function tests ---

func TestAnthropic_JsonInt(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		key  string
		want int
	}{
		{"present", map[string]any{"idx": float64(5)}, "idx", 5},
		{"missing", map[string]any{"other": float64(5)}, "idx", 0},
		{"wrong type", map[string]any{"idx": "5"}, "idx", 0},
		{"zero", map[string]any{"idx": float64(0)}, "idx", 0},
		{"negative", map[string]any{"idx": float64(-3)}, "idx", -3},
		{"nil value", map[string]any{"idx": nil}, "idx", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := jsonInt(tt.m, tt.key)
			if got != tt.want {
				t.Errorf("jsonInt() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestAnthropic_JsonString(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		key  string
		want string
	}{
		{"present", map[string]any{"k": "val"}, "k", "val"},
		{"missing", map[string]any{"other": "val"}, "k", ""},
		{"wrong type", map[string]any{"k": 42}, "k", ""},
		{"empty", map[string]any{"k": ""}, "k", ""},
		{"nil value", map[string]any{"k": nil}, "k", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := jsonString(tt.m, tt.key)
			if got != tt.want {
				t.Errorf("jsonString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAnthropic_ToolResultContentToString(t *testing.T) {
	tests := []struct {
		name    string
		content []ai.ToolResultContent
		want    string
	}{
		{"empty", nil, ""},
		{"single text", []ai.ToolResultContent{
			{Type: "text", Text: "hello"},
		}, "hello"},
		{"multiple text", []ai.ToolResultContent{
			{Type: "text", Text: "line1"},
			{Type: "text", Text: "line2"},
		}, "line1\nline2"},
		{"text and image mixed", []ai.ToolResultContent{
			{Type: "text", Text: "caption"},
			{Type: "image", Data: "base64data", MimeType: "image/png"},
			{Type: "text", Text: "more text"},
		}, "caption\nmore text"},
		{"only images", []ai.ToolResultContent{
			{Type: "image", Data: "data1"},
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toolResultContentToString(tt.content)
			if got != tt.want {
				t.Errorf("toolResultContentToString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAnthropic_UpdateUsage(t *testing.T) {
	model := &ai.Model{
		ID:   "claude-sonnet-4-20250514",
		Cost: ai.ModelCost{Input: 3.0, Output: 15.0, CacheRead: 0.3, CacheWrite: 3.75},
	}
	output := &ai.AssistantMessage{Usage: ai.ZeroUsage()}

	usage := map[string]any{
		"input_tokens":                float64(100),
		"output_tokens":               float64(50),
		"cache_read_input_tokens":     float64(200),
		"cache_creation_input_tokens": float64(30),
	}
	updateAnthropicUsage(output, usage, model)

	if output.Usage.Input != 100 {
		t.Errorf("expected input=100, got %d", output.Usage.Input)
	}
	if output.Usage.Output != 50 {
		t.Errorf("expected output=50, got %d", output.Usage.Output)
	}
	if output.Usage.CacheRead != 200 {
		t.Errorf("expected cacheRead=200, got %d", output.Usage.CacheRead)
	}
	if output.Usage.CacheWrite != 30 {
		t.Errorf("expected cacheWrite=30, got %d", output.Usage.CacheWrite)
	}
	if output.Usage.TotalTokens != 380 {
		t.Errorf("expected total=380, got %d", output.Usage.TotalTokens)
	}
	if output.Usage.Cost.Total <= 0 {
		t.Error("expected non-zero cost")
	}
}

func TestAnthropic_UpdateUsagePartial(t *testing.T) {
	model := &ai.Model{ID: "test", Cost: ai.ModelCost{}}
	output := &ai.AssistantMessage{Usage: ai.ZeroUsage()}

	usage := map[string]any{"input_tokens": float64(42)}
	updateAnthropicUsage(output, usage, model)

	if output.Usage.Input != 42 {
		t.Errorf("expected input=42, got %d", output.Usage.Input)
	}
	if output.Usage.Output != 0 {
		t.Errorf("expected output=0, got %d", output.Usage.Output)
	}
	if output.Usage.TotalTokens != 42 {
		t.Errorf("expected total=42, got %d", output.Usage.TotalTokens)
	}
}

// --- Convert messages tests ---

func TestAnthropic_ConvertMessages_UserText(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet", BaseURL: "https://api.anthropic.com", MaxTokens: 8192}
	msgs := []ai.Message{
		ai.NewUserMsg("Hello world", 1000),
	}

	result := convertAnthropicMessages(msgs, model, false, ai.CacheNone)

	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	if result[0]["role"] != "user" {
		t.Errorf("expected role=user, got %v", result[0]["role"])
	}
	if result[0]["content"] != "Hello world" {
		t.Errorf("expected content='Hello world', got %v", result[0]["content"])
	}
}

func TestAnthropic_ConvertMessages_EmptyStringSkipped(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet", BaseURL: "https://api.anthropic.com", MaxTokens: 8192}
	msgs := []ai.Message{
		ai.NewUserMsg("", 1000),
		ai.NewUserMsg("   ", 1000),
		ai.NewUserMsg("valid", 1000),
	}

	result := convertAnthropicMessages(msgs, model, false, ai.CacheNone)

	if len(result) != 1 {
		t.Fatalf("expected 1 message (empty strings skipped), got %d", len(result))
	}
	if result[0]["content"] != "valid" {
		t.Errorf("expected content='valid', got %v", result[0]["content"])
	}
}

func TestAnthropic_ConvertMessages_AssistantContent(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet", BaseURL: "https://api.anthropic.com", MaxTokens: 8192}

	assistantMsg := ai.AssistantMessage{
		Role: "assistant",
		Content: []ai.AssistantContent{
			ai.NewTextContent("I'll help you."),
			ai.NewToolCallContent("toolu_01", "read", map[string]any{"path": "test.txt"}),
		},
	}

	msgs := []ai.Message{
		ai.NewUserMsg("Read test.txt", 1000),
		ai.NewAssistantMsg(assistantMsg),
	}

	result := convertAnthropicMessages(msgs, model, false, ai.CacheNone)

	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}

	aBlocks, ok := result[1]["content"].([]map[string]any)
	if !ok {
		t.Fatalf("expected assistant content to be []map[string]any, got %T", result[1]["content"])
	}
	if len(aBlocks) != 2 {
		t.Fatalf("expected 2 assistant blocks, got %d", len(aBlocks))
	}
	if aBlocks[0]["type"] != "text" {
		t.Errorf("expected first block type=text, got %v", aBlocks[0]["type"])
	}
	if aBlocks[1]["type"] != "tool_use" {
		t.Errorf("expected second block type=tool_use, got %v", aBlocks[1]["type"])
	}
	if aBlocks[1]["name"] != "read" {
		t.Errorf("expected name=read, got %v", aBlocks[1]["name"])
	}
}

func TestAnthropic_ConvertMessages_ThinkingWithSignature(t *testing.T) {
	model := &ai.Model{
		ID: "claude-sonnet", BaseURL: "https://api.anthropic.com", MaxTokens: 8192,
		Api: ai.ApiAnthropicMessages, Provider: ai.ProviderAnthropic,
	}

	assistantMsg := ai.AssistantMessage{
		Role:     "assistant",
		Provider: ai.ProviderAnthropic,
		Api:      ai.ApiAnthropicMessages,
		Model:    "claude-sonnet",
		Content: []ai.AssistantContent{
			ai.NewThinkingContent("Let me think..."),
			ai.NewTextContent("Here's my answer."),
		},
	}
	// Set signature on content before wrapping in Message
	assistantMsg.Content[0].Thinking.ThinkingSignature = "sig_abc"

	msgs := []ai.Message{
		ai.NewUserMsg("Question?", 1000),
		ai.NewAssistantMsg(assistantMsg),
	}

	result := convertAnthropicMessages(msgs, model, false, ai.CacheNone)

	aBlocks := result[1]["content"].([]map[string]any)
	if len(aBlocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(aBlocks))
	}
	if aBlocks[0]["type"] != "thinking" {
		t.Errorf("expected thinking block, got %v", aBlocks[0]["type"])
	}
	if aBlocks[0]["signature"] != "sig_abc" {
		t.Errorf("expected signature sig_abc, got %v", aBlocks[0]["signature"])
	}
}

func TestAnthropic_ConvertMessages_ThinkingWithoutSignature(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet", BaseURL: "https://api.anthropic.com", MaxTokens: 8192}

	assistantMsg := ai.AssistantMessage{
		Role: "assistant",
		Content: []ai.AssistantContent{
			ai.NewThinkingContent("Some internal thought"),
			ai.NewTextContent("The answer."),
		},
	}

	msgs := []ai.Message{
		ai.NewUserMsg("Question?", 1000),
		ai.NewAssistantMsg(assistantMsg),
	}

	result := convertAnthropicMessages(msgs, model, false, ai.CacheNone)

	aBlocks := result[1]["content"].([]map[string]any)
	// Thinking without signature should be converted to text
	if aBlocks[0]["type"] != "text" {
		t.Errorf("expected thinking without signature to become text, got %v", aBlocks[0]["type"])
	}
	if aBlocks[0]["text"] != "Some internal thought" {
		t.Errorf("expected thinking text, got %v", aBlocks[0]["text"])
	}
}

func TestAnthropic_ConvertMessages_ToolResult(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet", BaseURL: "https://api.anthropic.com", MaxTokens: 8192}

	msgs := []ai.Message{
		ai.NewUserMsg("Read test.txt", 1000),
		ai.NewAssistantMsg(ai.AssistantMessage{
			Role: "assistant",
			Content: []ai.AssistantContent{
				ai.NewToolCallContent("toolu_01", "read", map[string]any{"path": "test.txt"}),
			},
		}),
		ai.NewToolResultMsg(ai.ToolResultMessage{
			Role:       ai.RoleToolResult,
			ToolCallID: "toolu_01",
			ToolName:   "read",
			Content:    []ai.ToolResultContent{{Type: "text", Text: "file contents here"}},
			IsError:    false,
		}),
	}

	result := convertAnthropicMessages(msgs, model, false, ai.CacheNone)

	// Should be: user, assistant, user (tool results become user role)
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
	if result[2]["role"] != "user" {
		t.Errorf("expected tool result role=user, got %v", result[2]["role"])
	}

	content, ok := result[2]["content"].([]map[string]any)
	if !ok || len(content) == 0 {
		t.Fatal("expected tool result content array")
	}
	if content[0]["type"] != "tool_result" {
		t.Errorf("expected type=tool_result, got %v", content[0]["type"])
	}
	if content[0]["tool_use_id"] != "toolu_01" {
		t.Errorf("expected tool_use_id=toolu_01, got %v", content[0]["tool_use_id"])
	}
	if content[0]["content"] != "file contents here" {
		t.Errorf("expected content='file contents here', got %v", content[0]["content"])
	}
}

func TestAnthropic_ConvertMessages_ConsecutiveToolResults(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet", BaseURL: "https://api.anthropic.com", MaxTokens: 8192}
	msgs := []ai.Message{
		ai.NewUserMsg("call both tools", 1000),
		ai.NewAssistantMsg(ai.AssistantMessage{
			Content: []ai.AssistantContent{
				ai.NewToolCallContent("toolu_01", "read", map[string]any{"path": "a.txt"}),
				ai.NewToolCallContent("toolu_02", "read", map[string]any{"path": "b.txt"}),
			},
		}),
		ai.NewToolResultMsg(ai.ToolResultMessage{
			Role: ai.RoleToolResult, ToolCallID: "toolu_01", ToolName: "read",
			Content: []ai.ToolResultContent{{Type: "text", Text: "contents of a"}},
		}),
		ai.NewToolResultMsg(ai.ToolResultMessage{
			Role: ai.RoleToolResult, ToolCallID: "toolu_02", ToolName: "read",
			Content: []ai.ToolResultContent{{Type: "text", Text: "contents of b"}},
		}),
	}

	result := convertAnthropicMessages(msgs, model, false, ai.CacheNone)

	// User, assistant, user (with both tool results merged)
	if len(result) != 3 {
		t.Fatalf("expected 3 messages (consecutive tool results merged), got %d", len(result))
	}

	third := result[2]
	content, ok := third["content"].([]map[string]any)
	if !ok {
		t.Fatal("expected tool result content array")
	}
	if len(content) != 2 {
		t.Fatalf("expected 2 tool results merged, got %d", len(content))
	}
	if content[0]["tool_use_id"] != "toolu_01" {
		t.Errorf("first tool result ID: want toolu_01, got %v", content[0]["tool_use_id"])
	}
	if content[1]["tool_use_id"] != "toolu_02" {
		t.Errorf("second tool result ID: want toolu_02, got %v", content[1]["tool_use_id"])
	}
}

func TestAnthropic_ConvertMessages_OAuthToolNames(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet", BaseURL: "https://api.anthropic.com", MaxTokens: 8192}

	assistantMsg := ai.AssistantMessage{
		Role: "assistant",
		Content: []ai.AssistantContent{
			ai.NewToolCallContent("toolu_01", "read", map[string]any{"path": "test.txt"}),
		},
	}

	msgs := []ai.Message{
		ai.NewUserMsg("Read it", 1000),
		ai.NewAssistantMsg(assistantMsg),
	}

	// OAuth mode: tool names should be mapped to Claude Code names
	result := convertAnthropicMessages(msgs, model, true, ai.CacheNone)
	aBlocks := result[1]["content"].([]map[string]any)
	if aBlocks[0]["name"] != "Read" {
		t.Errorf("OAuth: expected tool name 'Read', got %v", aBlocks[0]["name"])
	}

	// Non-OAuth: tool names stay as-is
	result = convertAnthropicMessages(msgs, model, false, ai.CacheNone)
	aBlocks = result[1]["content"].([]map[string]any)
	if aBlocks[0]["name"] != "read" {
		t.Errorf("non-OAuth: expected tool name 'read', got %v", aBlocks[0]["name"])
	}
}

func TestAnthropic_ConvertMessages_CacheControl(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet", BaseURL: "https://api.anthropic.com", MaxTokens: 8192}

	msgs := []ai.Message{
		ai.NewUserMsg([]any{
			map[string]any{"type": "text", "text": "first part"},
			map[string]any{"type": "text", "text": "second part"},
		}, 1000),
	}

	// With cache enabled, last block of last user message gets cache_control
	result := convertAnthropicMessages(msgs, model, false, ai.CacheShort)
	content := result[0]["content"].([]map[string]any)
	if len(content) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(content))
	}
	if content[0]["cache_control"] != nil {
		t.Error("first block should not have cache_control")
	}
	cc, ok := content[1]["cache_control"].(map[string]any)
	if !ok {
		t.Fatal("last block should have cache_control")
	}
	if cc["type"] != "ephemeral" {
		t.Errorf("expected ephemeral, got %v", cc["type"])
	}
}

func TestAnthropic_ConvertMessages_NoCacheControl(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet", BaseURL: "https://api.anthropic.com", MaxTokens: 8192}

	msgs := []ai.Message{
		ai.NewUserMsg([]any{
			map[string]any{"type": "text", "text": "content"},
		}, 1000),
	}

	result := convertAnthropicMessages(msgs, model, false, ai.CacheNone)
	content := result[0]["content"].([]map[string]any)
	if content[0]["cache_control"] != nil {
		t.Error("CacheNone should not add cache_control")
	}
}

func TestAnthropic_ConvertMessages_UserImageContent(t *testing.T) {
	model := &ai.Model{
		ID:      "claude-sonnet",
		BaseURL: "https://api.anthropic.com",
		MaxTokens: 8192,
		Input:   []ai.InputModality{ai.InputText, ai.InputImage},
	}

	msgs := []ai.Message{
		ai.NewUserMsg([]any{
			map[string]any{"type": "text", "text": "describe this"},
			map[string]any{"type": "image", "data": "base64data", "mimeType": "image/png"},
		}, 1000),
	}

	result := convertAnthropicMessages(msgs, model, false, ai.CacheNone)
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}

	content := result[0]["content"].([]map[string]any)
	if len(content) != 2 {
		t.Fatalf("expected 2 blocks (text + image), got %d", len(content))
	}
	if content[0]["type"] != "text" {
		t.Errorf("expected first block type=text, got %v", content[0]["type"])
	}
	if content[1]["type"] != "image" {
		t.Errorf("expected second block type=image, got %v", content[1]["type"])
	}
	source := content[1]["source"].(map[string]any)
	if source["type"] != "base64" {
		t.Errorf("expected source type=base64, got %v", source["type"])
	}
	if source["media_type"] != "image/png" {
		t.Errorf("expected media_type=image/png, got %v", source["media_type"])
	}
}

// --- Convert tools tests ---

func TestAnthropic_ConvertTools_RequiredAsAnySlice(t *testing.T) {
	tools := []ai.Tool{{
		Name:        "test_tool",
		Description: "A test tool",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"a": map[string]any{"type": "string"},
				"b": map[string]any{"type": "string"},
			},
			"required": []any{"a", "b"},
		},
	}}

	result := convertAnthropicTools(tools, false)
	if len(result) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result))
	}

	schema, ok := result[0]["input_schema"].(map[string]any)
	if !ok {
		t.Fatal("expected input_schema")
	}
	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatalf("expected required as []string, got %T", schema["required"])
	}
	if len(required) != 2 || required[0] != "a" || required[1] != "b" {
		t.Errorf("unexpected required: %v", required)
	}
}

func TestAnthropic_ConvertTools_RequiredAsStringSlice(t *testing.T) {
	tools := []ai.Tool{{
		Name:        "test_tool",
		Description: "A test tool",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"a": map[string]any{"type": "string"},
			},
			"required": []string{"a"},
		},
	}}

	result := convertAnthropicTools(tools, false)
	schema := result[0]["input_schema"].(map[string]any)
	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatalf("expected required as []string, got %T", schema["required"])
	}
	if len(required) != 1 || required[0] != "a" {
		t.Errorf("unexpected required: %v", required)
	}
}

func TestAnthropic_ConvertTools_OAuthNames(t *testing.T) {
	tools := []ai.Tool{
		{Name: "read", Description: "Read a file", Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}},
		}},
		{Name: "custom_tool", Description: "Custom", Parameters: map[string]any{
			"type": "object", "properties": map[string]any{},
		}},
	}

	result := convertAnthropicTools(tools, true)
	if result[0]["name"] != "Read" {
		t.Errorf("OAuth: expected 'Read', got %v", result[0]["name"])
	}
	if result[1]["name"] != "custom_tool" {
		t.Errorf("OAuth: expected 'custom_tool' (unmapped), got %v", result[1]["name"])
	}
}

// --- StreamSimple tests ---

func TestAnthropic_StreamSimple_NoReasoning(t *testing.T) {
	srv := mockSSEServer(t, "anthropic_simple_response.sse")
	defer srv.Close()

	model := anthropicModel(srv.URL)
	ctx := ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("Hello!", 1000)},
	}
	opts := &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{ApiKey: "test-key"}}

	stream := StreamSimpleAnthropic(context.Background(), model, ctx, opts)
	events := collectEvents(t, stream)

	if len(events) < 2 {
		t.Fatalf("expected at least 2 events, got %d", len(events))
	}

	result := stream.Result()
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.StopReason != ai.StopReasonStop {
		t.Errorf("expected stop, got %s", result.StopReason)
	}
}

func TestAnthropic_StreamSimple_NilOptions(t *testing.T) {
	srv := mockSSEServer(t, "anthropic_simple_response.sse")
	defer srv.Close()

	model := anthropicModel(srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "test-key-env")
	ctx := ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("Hello!", 1000)},
	}

	stream := StreamSimpleAnthropic(context.Background(), model, ctx, nil)
	events := collectEvents(t, stream)

	if len(events) < 2 {
		t.Fatalf("expected at least 2 events, got %d", len(events))
	}
}

func TestAnthropic_StreamSimple_AdaptiveThinking(t *testing.T) {
	srv, captured := captureRequest(t, "anthropic_simple_response.sse")
	defer srv.Close()

	model := anthropicModel(srv.URL)
	model.ID = "claude-opus-4-6-20250101" // supports adaptive thinking
	ctx := ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("Think hard!", 1000)},
	}
	opts := &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{ApiKey: "test-key"},
		Reasoning:     ai.ThinkingHigh,
	}

	stream := StreamSimpleAnthropic(context.Background(), model, ctx, opts)
	collectEvents(t, stream)

	if len(*captured) == 0 {
		t.Fatal("expected captured request body")
	}

	var payload map[string]any
	if err := json.Unmarshal(*captured, &payload); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	thinking, ok := payload["thinking"].(map[string]any)
	if !ok {
		t.Fatal("expected thinking in payload")
	}
	if thinking["type"] != "adaptive" {
		t.Errorf("expected adaptive thinking, got %v", thinking["type"])
	}

	outputConfig, ok := payload["output_config"].(map[string]any)
	if !ok {
		t.Fatal("expected output_config in payload")
	}
	if outputConfig["effort"] != "high" {
		t.Errorf("expected effort=high, got %v", outputConfig["effort"])
	}
}

func TestAnthropic_StreamSimple_BudgetThinking(t *testing.T) {
	srv, captured := captureRequest(t, "anthropic_simple_response.sse")
	defer srv.Close()

	model := anthropicModel(srv.URL)
	model.ID = "claude-sonnet-4-20250514" // does NOT support adaptive thinking
	ctx := ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("Think!", 1000)},
	}
	opts := &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{ApiKey: "test-key"},
		Reasoning:     ai.ThinkingMedium,
	}

	stream := StreamSimpleAnthropic(context.Background(), model, ctx, opts)
	collectEvents(t, stream)

	if len(*captured) == 0 {
		t.Fatal("expected captured request body")
	}

	var payload map[string]any
	if err := json.Unmarshal(*captured, &payload); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	thinking, ok := payload["thinking"].(map[string]any)
	if !ok {
		t.Fatal("expected thinking in payload")
	}
	if thinking["type"] != "enabled" {
		t.Errorf("expected enabled thinking type, got %v", thinking["type"])
	}
	budget, ok := thinking["budget_tokens"].(float64)
	if !ok || budget <= 0 {
		t.Errorf("expected positive budget_tokens, got %v", thinking["budget_tokens"])
	}
}

// --- Register tests ---

func TestAnthropic_RegisterProvider(t *testing.T) {
	r := ai.NewRegistry()
	RegisterAnthropic(r)

	p := r.GetApiProvider(ai.ApiAnthropicMessages)
	if p == nil {
		t.Fatal("expected anthropic provider")
	}
	if p.Api != ai.ApiAnthropicMessages {
		t.Errorf("expected api %s, got %s", ai.ApiAnthropicMessages, p.Api)
	}
}
