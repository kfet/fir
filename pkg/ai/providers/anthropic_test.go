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

	// Should have: start, text_start, text_delta*3, text_end, done
	if len(events) < 4 {
		t.Fatalf("expected at least 4 events, got %d", len(events))
	}

	// First event should be start
	if events[0].Type != ai.EventStart {
		t.Errorf("expected start, got %s", events[0].Type)
	}

	// Last event should be done
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

	// First block: text
	if !result.Content[0].IsText() {
		t.Error("expected text content")
	}
	if result.Content[0].Text.Text != "I'll read that file for you." {
		t.Errorf("unexpected text: %q", result.Content[0].Text.Text)
	}

	// Second block: tool call
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

	// Check events contain toolcall events
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

	// First block: thinking
	if !result.Content[0].IsThinking() {
		t.Error("expected thinking content")
	}
	if result.Content[0].Thinking.Thinking != "Let me think about this..." {
		t.Errorf("unexpected thinking: %q", result.Content[0].Thinking.Thinking)
	}
	if result.Content[0].Thinking.ThinkingSignature != "sig123" {
		t.Errorf("expected signature sig123, got %q", result.Content[0].Thinking.ThinkingSignature)
	}

	// Second block: text
	if !result.Content[1].IsText() {
		t.Error("expected text content")
	}
	if result.Content[1].Text.Text != "The answer is 42." {
		t.Errorf("unexpected text: %q", result.Content[1].Text.Text)
	}

	// Check events
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

	// Should have start and error events
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

	// Check model
	if payload["model"] != "claude-sonnet-4-20250514" {
		t.Errorf("expected model claude-sonnet-4-20250514, got %v", payload["model"])
	}

	// Check stream
	if payload["stream"] != true {
		t.Error("expected stream=true")
	}

	// Check system prompt exists
	system, ok := payload["system"].([]any)
	if !ok || len(system) == 0 {
		t.Error("expected system prompt")
	}

	// Check tools
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
