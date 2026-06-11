// Ported from: packages/ai/src/providers/anthropic.ts
// Upstream hash: 1caadb2e
package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
)

// TestMain registers the canonical Claude-Code tool-name map for the test
// process. In production this map is supplied by the anthropic-auth builtin
// extension at handshake time; for unit tests we mirror that registration
// up front so the existing tool-name tests keep exercising the real
// translation path.
func TestMain(m *testing.M) {
	RegisterToolNameAliases("anthropic-auth-test", map[string]string{
		"read":  "Read",
		"write": "Write",
		"edit":  "Edit",
		"bash":  "Bash",
		"grep":  "Grep",
		"find":  "Glob",
	})
	os.Exit(m.Run())
}

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
	opts := &ai.StreamOptions{APIKey: "test-key"}

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
	opts := &ai.StreamOptions{APIKey: "test-key"}

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
	opts := &ai.StreamOptions{APIKey: "test-key"}

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

func TestAnthropic_RedactedThinking(t *testing.T) {
	srv := mockSSEServer(t, "anthropic_redacted_thinking.sse")
	defer srv.Close()

	model := anthropicModel(srv.URL)
	ctx := ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("What is the meaning of life?", 1000)},
	}
	opts := &ai.StreamOptions{APIKey: "test-key"}

	stream := StreamAnthropic(context.Background(), model, ctx, opts)
	events := collectEvents(t, stream)

	result := stream.Result()
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(result.Content))
	}
	// First block: redacted thinking
	if !result.Content[0].IsThinking() {
		t.Error("expected thinking content at index 0")
	}
	if !result.Content[0].Thinking.Redacted {
		t.Error("expected Redacted=true")
	}
	if result.Content[0].Thinking.ThinkingSignature != "EncryptedOpaqueBlobABC123==" {
		t.Errorf("expected encrypted data, got %q", result.Content[0].Thinking.ThinkingSignature)
	}
	if result.Content[0].Thinking.Thinking != "" {
		t.Errorf("expected empty thinking text for redacted block, got %q", result.Content[0].Thinking.Thinking)
	}
	// Second block: text
	if !result.Content[1].IsText() {
		t.Error("expected text content at index 1")
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

func TestAnthropic_ConvertMessages_RedactedThinking(t *testing.T) {
	model := &ai.Model{
		ID:        "claude-sonnet-4-6",
		Provider:  ai.ProviderAnthropic,
		API:       ai.ApiAnthropicMessages,
		BaseURL:   "https://api.anthropic.com",
		MaxTokens: 64000,
	}

	redacted := ai.NewThinkingContent("")
	redacted.Thinking.Redacted = true
	redacted.Thinking.ThinkingSignature = "EncryptedOpaqueBlobABC123=="

	assistantMsg := ai.AssistantMessage{
		Role:     "assistant",
		Provider: ai.ProviderAnthropic,
		API:      ai.ApiAnthropicMessages,
		Model:    "claude-sonnet-4-6",
		Content: []ai.AssistantContent{
			redacted,
			ai.NewTextContent("The answer is 42."),
		},
	}

	msgs := []ai.Message{
		ai.NewUserMsg("Question?", 1000),
		ai.NewAssistantMsg(assistantMsg),
	}

	result := convertAnthropicMessages(msgs, model, false, ai.CacheNone)

	aBlocks := result[1]["content"].([]map[string]any)
	if len(aBlocks) != 2 {
		t.Fatalf("expected 2 assistant blocks, got %d", len(aBlocks))
	}
	// Redacted thinking must be sent back as-is with type "redacted_thinking"
	if aBlocks[0]["type"] != "redacted_thinking" {
		t.Errorf("expected redacted_thinking block, got %v", aBlocks[0]["type"])
	}
	if aBlocks[0]["data"] != "EncryptedOpaqueBlobABC123==" {
		t.Errorf("expected encrypted data preserved, got %v", aBlocks[0]["data"])
	}
	if _, hasThinking := aBlocks[0]["thinking"]; hasThinking {
		t.Error("redacted_thinking block must not have a 'thinking' field")
	}
	if aBlocks[1]["type"] != "text" {
		t.Errorf("expected text block, got %v", aBlocks[1]["type"])
	}
}

// TestAnthropic_ConvertMessages_DropsEmptyTextBesideThinking verifies that
// empty/whitespace-only text blocks are dropped even when the assistant turn
// carries a signed thinking block. Anthropic's Messages API validates input
// blocks before any thinking-modification check and rejects empty text with
// 400 "messages: text content blocks must be non-empty"; replaying such
// blocks verbatim therefore breaks resumed sessions. The thinking block must
// still survive intact.
//
// (Earlier code preserved empty siblings on the theory that dropping them
// would trigger a "thinking blocks cannot be modified" 400. Production
// evidence — request id req_011CaiKVdgvopStQzBuvt3kq — shows the empty-text
// rejection fires first, so the safe path is to drop empty text always.)
func TestAnthropic_ConvertMessages_DropsEmptyTextBesideThinking(t *testing.T) {
	model := &ai.Model{
		ID:        "claude-opus-4-7",
		Provider:  ai.ProviderAnthropic,
		API:       ai.ApiAnthropicMessages,
		BaseURL:   "https://api.anthropic.com",
		MaxTokens: 64000,
	}

	thinking := ai.NewThinkingContent("Let me think.")
	thinking.Thinking.ThinkingSignature = "sig-abc"

	emptyText := ai.NewTextContent("")

	assistantMsg := ai.AssistantMessage{
		Role:     "assistant",
		Provider: ai.ProviderAnthropic,
		API:      ai.ApiAnthropicMessages,
		Model:    "claude-opus-4-7",
		Content: []ai.AssistantContent{
			thinking,
			emptyText,
			ai.NewToolCallContent("call_1", "echo", map[string]any{"x": 1}),
		},
	}

	msgs := []ai.Message{
		ai.NewUserMsg("Question?", 1000),
		ai.NewAssistantMsg(assistantMsg),
	}

	result := convertAnthropicMessages(msgs, model, false, ai.CacheNone)
	if len(result) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(result))
	}
	aBlocks := result[1]["content"].([]map[string]any)
	if len(aBlocks) != 2 {
		t.Fatalf("expected 2 assistant blocks (thinking, tool_use) — empty text must be dropped, got %d: %v", len(aBlocks), aBlocks)
	}
	if aBlocks[0]["type"] != "thinking" {
		t.Errorf("block 0: expected thinking, got %v", aBlocks[0]["type"])
	}
	if aBlocks[0]["signature"] != "sig-abc" {
		t.Errorf("block 0: thinking signature mutated, got %v", aBlocks[0]["signature"])
	}
	if aBlocks[1]["type"] != "tool_use" {
		t.Errorf("block 1: expected tool_use, got %v", aBlocks[1]["type"])
	}
	// And no block must carry an empty text payload.
	for i, b := range aBlocks {
		if b["type"] == "text" && b["text"] == "" {
			t.Errorf("block %d: empty text block survived (Anthropic 400-rejects it): %v", i, b)
		}
	}
}

func TestAnthropic_StreamingError(t *testing.T) {
	// Use zero retry delay so the test doesn't sleep through 5 backoff windows.
	prev := anthropicRetryDelayFn
	anthropicRetryDelayFn = func(_ string, _ int, _ *int) time.Duration { return 0 }
	t.Cleanup(func() { anthropicRetryDelayFn = prev })

	srv := mockSSEServer(t, "anthropic_error.sse")
	defer srv.Close()

	model := anthropicModel(srv.URL)
	ctx := ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("Hello!", 1000)},
	}
	opts := &ai.StreamOptions{APIKey: "test-key"}

	stream := StreamAnthropic(context.Background(), model, ctx, opts)
	events := collectEvents(t, stream)

	result := stream.Result()
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.StopReason != ai.StopReasonError {
		t.Errorf("expected error, got %s", result.StopReason)
	}
	if result.ErrorMessage != "Overloaded (overloaded_error)" {
		t.Errorf("expected 'Overloaded (overloaded_error)', got %q", result.ErrorMessage)
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

// TestAnthropic_OverloadedRetry verifies that StreamAnthropic transparently
// retries on overloaded_error and succeeds once the server recovers.
func TestAnthropic_OverloadedRetry(t *testing.T) {
	// Use zero retry delay so the test doesn't sleep.
	prev := anthropicRetryDelayFn
	anthropicRetryDelayFn = func(_ string, _ int, _ *int) time.Duration { return 0 }
	t.Cleanup(func() { anthropicRetryDelayFn = prev })

	overloadedData := loadFixture(t, "anthropic_error.sse")
	successData := loadFixture(t, "anthropic_simple_response.sse")

	attempts := 0
	srv := mockSSEServerFunc(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		attempts++
		if attempts < 3 {
			// First two requests return overloaded_error.
			w.Write(overloadedData)
		} else {
			// Third request succeeds.
			w.Write(successData)
		}
	})
	defer srv.Close()

	model := anthropicModel(srv.URL)
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello!", 1000)}}
	opts := &ai.StreamOptions{APIKey: "test-key"}

	stream := StreamAnthropic(context.Background(), model, ctx, opts)
	events := collectEvents(t, stream)

	result := stream.Result()
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.StopReason == ai.StopReasonError {
		t.Errorf("expected success, got error: %s", result.ErrorMessage)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts (2 overloaded + 1 success), got %d", attempts)
	}

	// EventStart and EventDone should be present; no EventError.
	var hasStart, hasDone, hasErr bool
	for _, evt := range events {
		switch evt.Type {
		case ai.EventStart:
			hasStart = true
		case ai.EventDone:
			hasDone = true
		case ai.EventError:
			hasErr = true
		}
	}
	if !hasStart {
		t.Error("missing EventStart")
	}
	if !hasDone {
		t.Error("missing EventDone")
	}
	if hasErr {
		t.Error("unexpected EventError — retry should have been transparent")
	}
}

func TestAnthropic_NoAPIKey(t *testing.T) {
	model := &ai.Model{
		ID:       "test-model",
		API:      ai.ApiAnthropicMessages,
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
	opts := &ai.StreamOptions{APIKey: "test-key"}

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

func TestAnthropic_ClaudeCodeNames_UnmappedPassthrough(t *testing.T) {
	// Fir tools without a Claude-Code counterpart (e.g. bash_output,
	// bash_kill) must pass through unchanged — the OAuth endpoint
	// accepts custom tool names in that case.
	for _, name := range []string{"bash_output", "bash_kill"} {
		if got := toClaudeCodeName(name); got != name {
			t.Errorf("toClaudeCodeName(%q) = %q, want passthrough", name, got)
		}
	}

	// And the reverse: the LLM sending back a custom name we registered
	// must resolve to the fir tool name.
	tools := []ai.Tool{{Name: "bash_output"}, {Name: "bash_kill"}}
	for _, name := range []string{"bash_output", "bash_kill"} {
		if got := fromClaudeCodeName(name, tools); got != name {
			t.Errorf("fromClaudeCodeName(%q) = %q, want %q", name, got, name)
		}
	}

	// find ↔ Glob still round-trips via the registered alias.
	if got := toClaudeCodeName("find"); got != "Glob" {
		t.Errorf("toClaudeCodeName(find) = %q, want Glob", got)
	}
	if got := fromClaudeCodeName("Glob", []ai.Tool{{Name: "find"}}); got != "find" {
		t.Errorf("fromClaudeCodeName(Glob) = %q, want find", got)
	}
}

func TestAnthropic_ToolNameAliases_UnregisterClears(t *testing.T) {
	// Register under a unique ext name and a tool name not in the global
	// test map so we can observe pure register/unregister behaviour.
	RegisterToolNameAliases("test-ext-unregister", map[string]string{"frobnicate": "Frobnicate"})
	if toClaudeCodeName("frobnicate") != "Frobnicate" {
		t.Fatal("register failed")
	}
	UnregisterToolNameAliases("test-ext-unregister")
	if got := toClaudeCodeName("frobnicate"); got != "frobnicate" {
		t.Errorf("after unregister: got %q, want passthrough frobnicate", got)
	}
}

func TestAnthropic_FromClaudeCodeNameFallback(t *testing.T) {
	tools := []ai.Tool{{Name: "read"}, {Name: "bash"}}
	got := fromClaudeCodeName("UnknownTool", tools)
	if got != "UnknownTool" {
		t.Errorf("expected UnknownTool, got %s", got)
	}
}

func TestAnthropic_IsOAuthModel(t *testing.T) {
	tests := []struct {
		name  string
		model *ai.Model
		want  bool
	}{
		{"nil model", nil, false},
		{"no headers", &ai.Model{}, false},
		{"non-oauth", &ai.Model{Headers: map[string]string{"x-api-key": "sk-xxx"}}, false},
		{"oauth with beta prefix", &ai.Model{Headers: map[string]string{"x-anthropic-oauth-beta-prefix": "claude-code-20250219,oauth-2025-04-20"}}, true},
		{"empty beta prefix", &ai.Model{Headers: map[string]string{"x-anthropic-oauth-beta-prefix": ""}}, false},
	}
	for _, tt := range tests {
		if got := isOAuthModel(tt.model); got != tt.want {
			t.Errorf("isOAuthModel(%s) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestAnthropic_ThinkingLevelMapping(t *testing.T) {
	// Declarative: "xhigh" is emitted only when the model advertises it in
	// ReasoningEffortValues; otherwise it clamps down to "high". No model-ID
	// matching.
	noXHigh := &ai.Model{ID: "no-xhigh", ReasoningEffortValues: []string{"low", "medium", "high", "max"}}
	withXHigh := &ai.Model{ID: "with-xhigh", ReasoningEffortValues: []string{"low", "medium", "high", "xhigh", "max"}}
	tests := []struct {
		level ai.ThinkingLevel
		model *ai.Model
		want  string
	}{
		// Model without an "xhigh" tier: xhigh clamps to high, max passes through.
		{ai.ThinkingOff, noXHigh, "low"}, // adaptive models can't disable; off => low effort
		{ai.ThinkingMinimal, noXHigh, "low"},
		{ai.ThinkingLow, noXHigh, "low"},
		{ai.ThinkingMedium, noXHigh, "medium"},
		{ai.ThinkingHigh, noXHigh, "high"},
		{ai.ThinkingXHigh, noXHigh, "high"},
		{ai.ThinkingMax, noXHigh, "max"},
		{"", noXHigh, "high"}, // default
		// Model advertising "xhigh": it's a distinct tier.
		{ai.ThinkingHigh, withXHigh, "high"},
		{ai.ThinkingXHigh, withXHigh, "xhigh"},
		{ai.ThinkingMax, withXHigh, "max"},
		// nil model / no advertised enum: xhigh clamps to high (conservative).
		{ai.ThinkingXHigh, nil, "high"},
		{ai.ThinkingXHigh, &ai.Model{ID: "bare"}, "high"},
	}
	for _, tt := range tests {
		got := mapThinkingLevelToEffort(tt.level, tt.model)
		if got != tt.want {
			t.Errorf("mapThinkingLevelToEffort(%q, %v) = %q, want %q", tt.level, tt.model, got, tt.want)
		}
	}
}

// --- Header tests ---

func TestAnthropic_PoeHeaders(t *testing.T) {
	model := &ai.Model{
		ID:       "claude-sonnet-4.5",
		Provider: "poe",
		BaseURL:  "https://api.poe.com/v1",
	}
	headers := buildAnthropicHeaders(model, "poe-token-xyz", false, nil, false)

	if got := headers["authorization"]; got != "Bearer poe-token-xyz" {
		t.Errorf("expected Bearer auth, got %q", got)
	}
	if headers["x-api-key"] != "" {
		t.Errorf("Poe must not set x-api-key, got %q", headers["x-api-key"])
	}
	if headers["anthropic-beta"] != "" {
		t.Errorf("Poe must not send anthropic-beta headers, got %q", headers["anthropic-beta"])
	}
	if headers["anthropic-version"] != "2023-06-01" {
		t.Errorf("expected anthropic-version 2023-06-01, got %q", headers["anthropic-version"])
	}
}

func TestAnthropic_AnthropicVersionHeader(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet", BaseURL: "https://api.anthropic.com"}

	// Non-OAuth
	headers := buildAnthropicHeaders(model, "sk-test", false, nil, false)
	if headers["anthropic-version"] != "2023-06-01" {
		t.Errorf("expected anthropic-version 2023-06-01, got %q", headers["anthropic-version"])
	}

	// OAuth
	oauthHeaders := buildAnthropicHeaders(model, "sk-ant-oat-test", true, nil, false)
	if oauthHeaders["anthropic-version"] != "2023-06-01" {
		t.Errorf("expected anthropic-version 2023-06-01 for OAuth, got %q", oauthHeaders["anthropic-version"])
	}
}

func TestAnthropic_OAuthHeaders(t *testing.T) {
	// OAuth headers (authorization, user-agent, x-app) are now set by the
	// anthropic_auth extension via model.Headers. The provider reads the
	// beta prefix from x-anthropic-oauth-beta-prefix.
	model := &ai.Model{
		ID:      "claude-sonnet",
		BaseURL: "https://api.anthropic.com",
		Headers: map[string]string{
			"authorization":                   "Bearer sk-ant-oat-test",
			"user-agent":                      "claude-cli/2.1.75 (external, cli)",
			"x-app":                           "cli",
			"x-anthropic-oauth-beta-prefix":   "claude-code-20250219,oauth-2025-04-20",
			"x-anthropic-oauth-system-prefix": "You are Claude Code, Anthropic's official CLI for Claude.",
		},
	}
	headers := buildAnthropicHeaders(model, "sk-ant-oat-test", true, nil, false)

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
	headers := buildAnthropicHeaders(model, "sk-ant-api03-test", false, nil, false)

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
	headers := buildAnthropicHeaders(model, "test-key", false, nil, false)

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
		APIKey: "test-key",
		Headers: map[string]string{
			"x-anthropic-thinking-enabled": "true",
			"x-anthropic-thinking-budget":  "2048",
			"x-custom-header":              "keep-me",
		},
	}
	headers := buildAnthropicHeaders(model, "test-key", false, opts, false)

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
		APIKey:    "test-key",
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
		APIKey: "test-key",
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
		APIKey:      "test-key",
		Temperature: &temp,
	}

	params := buildAnthropicParams(model, ctx, false, opts)

	if params["temperature"] != 0.5 {
		t.Errorf("expected temperature=0.5, got %v", params["temperature"])
	}
}

func TestAnthropic_BuildParams_OAuthSystemPrompt(t *testing.T) {
	model := &ai.Model{
		ID: "claude-sonnet", BaseURL: "https://api.anthropic.com", MaxTokens: 8192,
		Headers: map[string]string{
			"x-anthropic-oauth-beta-prefix":   "claude-code-20250219,oauth-2025-04-20",
			"x-anthropic-oauth-system-prefix": "You are Claude Code, Anthropic's official CLI for Claude.",
		},
	}
	ctx := ai.Context{
		SystemPrompt: "Be helpful.",
		Messages:     []ai.Message{ai.NewUserMsg("test", 1000)},
	}

	// OAuth mode: should prepend Claude Code system prompt from model headers
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
	modelPlain := &ai.Model{ID: "claude-sonnet", BaseURL: "https://api.anthropic.com", MaxTokens: 8192}
	params = buildAnthropicParams(modelPlain, ctx, false, nil)
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

// TestAnthropic_BuildParams_OAuthPrefixNoCacheControl ensures the OAuth
// identity prefix block does NOT carry cache_control. Putting a breakpoint
// there is wasteful — it's a strict prefix of the next system block, so the
// later breakpoint already covers it. Burning a breakpoint here would
// reduce the number of breakpoints available for messages.
func TestAnthropic_BuildParams_OAuthPrefixNoCacheControl(t *testing.T) {
	model := &ai.Model{
		ID: "claude-sonnet", BaseURL: "https://api.anthropic.com", MaxTokens: 8192,
		Headers: map[string]string{
			"x-anthropic-oauth-beta-prefix":   "claude-code-20250219,oauth-2025-04-20",
			"x-anthropic-oauth-system-prefix": "You are Claude Code.",
		},
	}
	ctx := ai.Context{
		SystemPrompt: "Be helpful.",
		Messages:     []ai.Message{ai.NewUserMsg("hi", 1000)},
	}

	params := buildAnthropicParams(model, ctx, true, &ai.StreamOptions{CacheRetention: ai.CacheShort})
	system := params["system"].([]map[string]any)
	if len(system) != 2 {
		t.Fatalf("expected 2 system blocks, got %d", len(system))
	}
	if system[0]["cache_control"] != nil {
		t.Errorf("OAuth prefix block must not carry cache_control, got %v", system[0]["cache_control"])
	}
	if system[1]["cache_control"] == nil {
		t.Error("user system block should carry cache_control")
	}
}

// TestAnthropic_ConvertTools_StableOrder verifies tools are emitted in
// alphabetical order regardless of input order. This keeps the Anthropic
// prompt-cache prefix stable when extensions and MCP servers register
// tools asynchronously.
func TestAnthropic_ConvertTools_StableOrder(t *testing.T) {
	mkTool := func(name string) ai.Tool {
		return ai.Tool{
			Name:        name,
			Description: "x",
			Parameters: map[string]any{
				"type": "object", "properties": map[string]any{}, "required": []string{},
			},
		}
	}

	a := []ai.Tool{mkTool("zebra"), mkTool("apple"), mkTool("mango")}
	b := []ai.Tool{mkTool("mango"), mkTool("zebra"), mkTool("apple")}

	ra := convertAnthropicTools(a, false)
	rb := convertAnthropicTools(b, false)

	if len(ra) != len(rb) {
		t.Fatalf("len mismatch: %d vs %d", len(ra), len(rb))
	}
	want := []string{"apple", "mango", "zebra"}
	for i, w := range want {
		if ra[i]["name"] != w {
			t.Errorf("ra[%d].name = %v, want %s", i, ra[i]["name"], w)
		}
		if rb[i]["name"] != w {
			t.Errorf("rb[%d].name = %v, want %s", i, rb[i]["name"], w)
		}
	}
}

// --- Cache control tests ---

func TestAnthropic_CacheControlBlock(t *testing.T) {
	// Short retention on anthropic.com
	model := &ai.Model{BaseURL: "https://api.anthropic.com"}
	cc := cacheControlBlock(model, ai.CacheShort)
	if cc["type"] != "ephemeral" {
		t.Errorf("expected ephemeral, got %v", cc["type"])
	}
	if cc["ttl"] != nil {
		t.Error("short retention should not have ttl")
	}

	// Long retention on anthropic.com
	cc = cacheControlBlock(model, ai.CacheLong)
	if cc["ttl"] != "1h" {
		t.Errorf("expected 1h ttl, got %v", cc["ttl"])
	}

	// Long retention on non-anthropic — default compat supports long cache
	model2 := &ai.Model{BaseURL: "https://custom.proxy.com"}
	cc = cacheControlBlock(model2, ai.CacheLong)
	if cc["ttl"] != "1h" {
		t.Errorf("non-anthropic with default compat (supportsLongCacheRetention=true) should have ttl, got %v", cc["ttl"])
	}

	// Long retention on non-anthropic with compat that disables long cache
	model3 := &ai.Model{BaseURL: "https://custom.proxy.com", Compat: &ai.AnthropicMessagesCompat{SupportsLongCacheRetention: ai.BoolPtr(false)}}
	cc = cacheControlBlock(model3, ai.CacheLong)
	if cc["ttl"] != nil {
		t.Error("non-anthropic with supportsLongCacheRetention=false should not have ttl")
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
	t.Setenv("FIR_CACHE_RETENTION", "")
	if got := resolveCacheRetention(""); got != ai.CacheShort {
		t.Errorf("expected CacheShort default, got %v", got)
	}

	// Env var override
	t.Setenv("FIR_CACHE_RETENTION", "long")
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

func TestAnthropic_ConvertToolResultContent(t *testing.T) {
	modelWithImages := &ai.Model{
		ID:    "claude-sonnet",
		Input: []ai.InputModality{ai.InputText, ai.InputImage},
	}
	modelNoImages := &ai.Model{
		ID:    "claude-sonnet",
		Input: []ai.InputModality{ai.InputText},
	}

	t.Run("text only returns joined string", func(t *testing.T) {
		content := []ai.ToolResultContent{
			{Type: "text", Text: "line 1"},
			{Type: "text", Text: "line 2"},
		}
		result := convertToolResultContent(content, modelWithImages)
		s, ok := result.(string)
		if !ok {
			t.Fatalf("expected string, got %T", result)
		}
		if s != "line 1\nline 2" {
			t.Errorf("expected 'line 1\\nline 2', got %q", s)
		}
	})

	t.Run("single text returns string", func(t *testing.T) {
		content := []ai.ToolResultContent{
			{Type: "text", Text: "hello"},
		}
		result := convertToolResultContent(content, modelWithImages)
		s, ok := result.(string)
		if !ok {
			t.Fatalf("expected string, got %T", result)
		}
		if s != "hello" {
			t.Errorf("expected 'hello', got %q", s)
		}
	})

	t.Run("mixed text and image returns blocks", func(t *testing.T) {
		content := []ai.ToolResultContent{
			{Type: "text", Text: "description"},
			{Type: "image", Data: "base64data", MimeType: "image/png"},
		}
		result := convertToolResultContent(content, modelWithImages)
		blocks, ok := result.([]map[string]any)
		if !ok {
			t.Fatalf("expected []map[string]any, got %T", result)
		}
		if len(blocks) != 2 {
			t.Fatalf("expected 2 blocks, got %d", len(blocks))
		}
		if blocks[0]["type"] != "text" {
			t.Errorf("expected first block type=text, got %v", blocks[0]["type"])
		}
		if blocks[0]["text"] != "description" {
			t.Errorf("expected text='description', got %v", blocks[0]["text"])
		}
		if blocks[1]["type"] != "image" {
			t.Errorf("expected second block type=image, got %v", blocks[1]["type"])
		}
		source, ok := blocks[1]["source"].(map[string]any)
		if !ok {
			t.Fatal("expected source map")
		}
		if source["type"] != "base64" {
			t.Errorf("expected source type=base64, got %v", source["type"])
		}
		if source["media_type"] != "image/png" {
			t.Errorf("expected media_type=image/png, got %v", source["media_type"])
		}
		if source["data"] != "base64data" {
			t.Errorf("expected data=base64data, got %v", source["data"])
		}
	})

	t.Run("image ignored when model does not support images", func(t *testing.T) {
		content := []ai.ToolResultContent{
			{Type: "text", Text: "some text"},
			{Type: "image", Data: "base64data", MimeType: "image/png"},
		}
		result := convertToolResultContent(content, modelNoImages)
		// Images not supported → treated as text-only → returns string
		s, ok := result.(string)
		if !ok {
			t.Fatalf("expected string (images not supported), got %T", result)
		}
		if s != "some text" {
			t.Errorf("expected 'some text', got %q", s)
		}
	})

	t.Run("empty content returns empty string", func(t *testing.T) {
		result := convertToolResultContent(nil, modelWithImages)
		s, ok := result.(string)
		if !ok {
			t.Fatalf("expected string, got %T", result)
		}
		if s != "" {
			t.Errorf("expected empty string, got %q", s)
		}
	})
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

func TestAnthropic_UpdateUsagePreservesFieldsOnNull(t *testing.T) {
	model := &ai.Model{ID: "test", Cost: ai.ModelCost{}}
	output := &ai.AssistantMessage{Usage: ai.ZeroUsage()}

	// First update: message_start sets input_tokens
	usage1 := map[string]any{
		"input_tokens":            float64(100),
		"output_tokens":           float64(0),
		"cache_read_input_tokens": float64(50),
	}
	updateAnthropicUsage(output, usage1, model)
	if output.Usage.Input != 100 {
		t.Errorf("expected input=100 after message_start, got %d", output.Usage.Input)
	}

	// Second update: message_delta only has output_tokens (proxy omits input)
	// Simulates proxies that don't include input_tokens in message_delta
	usage2 := map[string]any{
		"output_tokens": float64(25),
	}
	updateAnthropicUsage(output, usage2, model)
	if output.Usage.Input != 100 {
		t.Errorf("expected input=100 preserved from message_start, got %d", output.Usage.Input)
	}
	if output.Usage.Output != 25 {
		t.Errorf("expected output=25, got %d", output.Usage.Output)
	}
	if output.Usage.CacheRead != 50 {
		t.Errorf("expected cacheRead=50 preserved, got %d", output.Usage.CacheRead)
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
	content, ok := result[0]["content"].([]map[string]any)
	if !ok || len(content) != 1 {
		t.Fatalf("expected content to be []map with 1 block, got %v", result[0]["content"])
	}
	if content[0]["text"] != "Hello world" {
		t.Errorf("expected text='Hello world', got %v", content[0]["text"])
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
	content, ok := result[0]["content"].([]map[string]any)
	if !ok || len(content) != 1 {
		t.Fatalf("expected content to be []map with 1 block, got %v", result[0]["content"])
	}
	if content[0]["text"] != "valid" {
		t.Errorf("expected text='valid', got %v", content[0]["text"])
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

	if len(result) != 3 {
		t.Fatalf("expected 3 messages (user, assistant, synthetic tool result), got %d", len(result))
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

	// Third message should be a synthetic tool result for the unresolved tool call.
	// Anthropic wraps tool_result blocks in a "user" role message.
	synthResult := result[2]
	if synthResult["role"] != "user" {
		t.Errorf("expected synthetic result role=user (Anthropic wraps tool_result in user), got %v", synthResult["role"])
	}
	synthContent, ok := synthResult["content"].([]map[string]any)
	if !ok {
		t.Fatalf("expected synthetic result content to be []map[string]any, got %T", synthResult["content"])
	}
	if len(synthContent) != 1 || synthContent[0]["type"] != "tool_result" {
		t.Errorf("expected single tool_result block, got %v", synthContent)
	}
}

func TestAnthropic_ConvertMessages_ThinkingWithSignature(t *testing.T) {
	model := &ai.Model{
		ID: "claude-sonnet", BaseURL: "https://api.anthropic.com", MaxTokens: 8192,
		API: ai.ApiAnthropicMessages, Provider: ai.ProviderAnthropic,
	}

	assistantMsg := ai.AssistantMessage{
		Role:     "assistant",
		Provider: ai.ProviderAnthropic,
		API:      ai.ApiAnthropicMessages,
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
		ID:        "claude-sonnet",
		BaseURL:   "https://api.anthropic.com",
		MaxTokens: 8192,
		Input:     []ai.InputModality{ai.InputText, ai.InputImage},
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
	// Sort is on the input (fir) tool name: custom_tool < read.
	if result[0]["name"] != "custom_tool" {
		t.Errorf("OAuth: expected 'custom_tool' (unmapped) first, got %v", result[0]["name"])
	}
	if result[1]["name"] != "Read" {
		t.Errorf("OAuth: expected 'Read' second, got %v", result[1]["name"])
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
	opts := &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{APIKey: "test-key"}}

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
	model.ID = "claude-opus-4-6-20250101"
	model.AdaptiveThinking = true // supports adaptive thinking (declarative)
	ctx := ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("Think hard!", 1000)},
	}
	opts := &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{APIKey: "test-key"},
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
	model.ID = "claude-sonnet-4-20250514"
	model.AdaptiveThinking = false // budget-based thinking (declarative)
	ctx := ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("Think!", 1000)},
	}
	opts := &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{APIKey: "test-key"},
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

func TestAnthropic_ServerTools_InParams(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet-4-20250514", BaseURL: "https://api.anthropic.com", MaxTokens: 8192}
	ctx := ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("search for Go tutorials", 1000)},
		Tools: []ai.Tool{{
			Name:        "read",
			Description: "Read a file",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		}},
	}

	opts := &ai.StreamOptions{
		APIKey: "test-key",
		ServerTools: []ai.AnthropicServerTool{
			{
				Type:           "web_search_20250305",
				Name:           "web_search",
				MaxUses:        5,
				AllowedDomains: []string{"golang.org"},
				UserLocation:   &ai.AnthropicUserLocation{Type: "approximate", Country: "US"},
			},
			{
				Type: "code_execution_20250522",
			},
		},
	}

	params := buildAnthropicParams(model, ctx, false, opts)

	tools, ok := params["tools"].([]map[string]any)
	if !ok {
		t.Fatal("expected tools as []map[string]any")
	}
	// 1 regular tool + 2 server tools
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}

	// First tool is the regular tool
	if tools[0]["name"] != "read" {
		t.Errorf("expected first tool name=read, got %v", tools[0]["name"])
	}

	// Second tool is web_search
	ws := tools[1]
	if ws["type"] != "web_search_20250305" {
		t.Errorf("expected type=web_search_20250305, got %v", ws["type"])
	}
	if ws["name"] != "web_search" {
		t.Errorf("expected name=web_search, got %v", ws["name"])
	}
	if ws["max_uses"] != 5 {
		t.Errorf("expected max_uses=5, got %v", ws["max_uses"])
	}
	domains, _ := ws["allowed_domains"].([]string)
	if len(domains) != 1 || domains[0] != "golang.org" {
		t.Errorf("expected allowed_domains=[golang.org], got %v", ws["allowed_domains"])
	}
	loc, _ := ws["user_location"].(*ai.AnthropicUserLocation)
	if loc == nil || loc.Country != "US" {
		t.Errorf("expected user_location with country=US, got %v", ws["user_location"])
	}

	// Third tool is code_execution
	ce := tools[2]
	if ce["type"] != "code_execution_20250522" {
		t.Errorf("expected type=code_execution_20250522, got %v", ce["type"])
	}
}

func TestAnthropic_ServerTools_BetaHeaders(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet-4-20250514", BaseURL: "https://api.anthropic.com"}
	opts := &ai.StreamOptions{
		ServerTools: []ai.AnthropicServerTool{
			{Type: "web_search_20260209"},
			{Type: "web_fetch_20260209"},
			{Type: "code_execution_20250825"},
		},
	}

	headers := buildAnthropicHeaders(model, "test-key", false, opts, false)
	beta := headers["anthropic-beta"]
	if !strings.Contains(beta, "web-search-2025-03-05") {
		t.Errorf("expected web-search beta in header, got %s", beta)
	}
	if !strings.Contains(beta, "web-fetch-2025-09-10") {
		t.Errorf("expected web-fetch beta in header, got %s", beta)
	}
	if !strings.Contains(beta, "code-execution-2025-08-25") {
		t.Errorf("expected code-execution beta in header, got %s", beta)
	}
}

func TestAnthropic_ServerTools_BetaHeaders_NoDuplicates(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet-4-20250514", BaseURL: "https://api.anthropic.com"}
	opts := &ai.StreamOptions{
		ServerTools: []ai.AnthropicServerTool{
			{Type: "web_search_20260209", Name: "search1"},
			{Type: "web_search_20260209", Name: "search2"},
		},
	}

	headers := buildAnthropicHeaders(model, "test-key", false, opts, false)
	beta := headers["anthropic-beta"]
	// Count occurrences of the beta string
	count := strings.Count(beta, "web-search-2025-03-05")
	if count != 1 {
		t.Errorf("expected exactly 1 occurrence of web-search beta, got %d in %q", count, beta)
	}
}

// TestAnthropic_FineGrainedToolStreamingBeta_NoTools verifies the fine-grained
// tool streaming beta is NOT sent when there are no tools.
func TestAnthropic_FineGrainedToolStreamingBeta_NoTools(t *testing.T) {
	// Default model (no compat) should not use the legacy beta when no tools.
	model := &ai.Model{ID: "claude-sonnet-4-20250514", BaseURL: "https://api.anthropic.com"}
	headers := buildAnthropicHeaders(model, "test-key", false, nil, false)
	if strings.Contains(headers["anthropic-beta"], "fine-grained-tool-streaming") {
		t.Errorf("expected no fine-grained-tool-streaming beta when no tools, got %q", headers["anthropic-beta"])
	}
}

// TestAnthropic_FineGrainedToolStreamingBeta_WithTools_DefaultEager verifies
// the legacy beta is NOT sent when the model supports eager tool input streaming
// (the default).
func TestAnthropic_FineGrainedToolStreamingBeta_WithTools_DefaultEager(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet-4-20250514", BaseURL: "https://api.anthropic.com"}
	headers := buildAnthropicHeaders(model, "test-key", false, nil, true)
	if strings.Contains(headers["anthropic-beta"], "fine-grained-tool-streaming") {
		t.Errorf("default model with tools should not send legacy beta, got %q", headers["anthropic-beta"])
	}
}

// TestAnthropic_FineGrainedToolStreamingBeta_WithTools_NoEager verifies the
// legacy beta IS sent when the model's compat opts out of eager streaming
// (e.g. third-party Anthropic-compatible endpoints).
func TestAnthropic_FineGrainedToolStreamingBeta_WithTools_NoEager(t *testing.T) {
	f := false
	model := &ai.Model{
		ID:      "claude-sonnet-4-20250514",
		BaseURL: "https://api.anthropic.com",
		Compat:  &ai.AnthropicMessagesCompat{SupportsEagerToolInputStreaming: &f},
	}
	headers := buildAnthropicHeaders(model, "test-key", false, nil, true)
	if !strings.Contains(headers["anthropic-beta"], "fine-grained-tool-streaming-2025-05-14") {
		t.Errorf("expected fine-grained-tool-streaming beta when compat opts out of eager, got %q", headers["anthropic-beta"])
	}
}

// TestAnthropic_CacheControlLongRetention verifies the 1h ttl is applied
// only when the model's compat allows long cache retention.
func TestAnthropic_CacheControlLongRetention(t *testing.T) {
	// Default model: long retention supported.
	model := &ai.Model{ID: "claude-sonnet-4-20250514", BaseURL: "https://api.anthropic.com"}
	cc := cacheControlBlock(model, ai.CacheLong)
	if cc["ttl"] != "1h" {
		t.Errorf("default model should set ttl=1h for long cache retention, got %v", cc)
	}

	// Compat opts out: no ttl.
	f := false
	modelNoLong := &ai.Model{
		ID:      "claude-sonnet-4-20250514",
		BaseURL: "https://api.anthropic.com",
		Compat:  &ai.AnthropicMessagesCompat{SupportsLongCacheRetention: &f},
	}
	cc = cacheControlBlock(modelNoLong, ai.CacheLong)
	if _, has := cc["ttl"]; has {
		t.Errorf("compat without long retention should not set ttl, got %v", cc)
	}

	// Short retention: no ttl regardless of compat.
	cc = cacheControlBlock(model, ai.CacheNone)
	if _, has := cc["ttl"]; has {
		t.Errorf("CacheNone should not set ttl, got %v", cc)
	}
}

func TestAnthropic_FormatWebSearchResult(t *testing.T) {
	cb := map[string]any{
		"type": "web_search_tool_result",
		"content": []any{
			map[string]any{
				"type":         "web_search_result",
				"title":        "Go Tutorial",
				"url":          "https://golang.org/doc/tutorial",
				"page_snippet": "Learn Go programming.",
			},
		},
	}
	text := formatWebSearchResult(cb)
	if !strings.Contains(text, "Go Tutorial") {
		t.Errorf("expected title in output, got %q", text)
	}
	if !strings.Contains(text, "https://golang.org/doc/tutorial") {
		t.Errorf("expected URL in output, got %q", text)
	}
}

func TestAnthropic_FormatCodeExecutionResult(t *testing.T) {
	cb := map[string]any{
		"type": "code_execution_tool_result",
		"content": []any{
			map[string]any{
				"type":   "code_execution_output",
				"output": "42",
			},
		},
	}
	text := formatCodeExecutionResult(cb)
	if !strings.Contains(text, "42") {
		t.Errorf("expected output in result, got %q", text)
	}

	// Error case
	cbErr := map[string]any{
		"type": "code_execution_tool_result",
		"content": []any{
			map[string]any{
				"type":          "code_execution_error",
				"error_name":    "ValueError",
				"error_message": "invalid input",
			},
		},
	}
	text = formatCodeExecutionResult(cbErr)
	if !strings.Contains(text, "ValueError") || !strings.Contains(text, "invalid input") {
		t.Errorf("expected error info in result, got %q", text)
	}
}

func TestAnthropic_UnknownServerTool_NoBeta(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet-4-20250514", BaseURL: "https://api.anthropic.com"}
	opts := &ai.StreamOptions{
		ServerTools: []ai.AnthropicServerTool{
			{Type: "unknown_tool_20260101"},
		},
	}

	headers := buildAnthropicHeaders(model, "test-key", false, opts, false)
	beta := headers["anthropic-beta"]
	// Unknown tools should not add any extra beta.
	// With conditional betas, base betas may be empty when no tools/thinking.
	if strings.Contains(beta, "unknown") {
		t.Errorf("unknown tool beta should not appear, got %s", beta)
	}
}

func TestAnthropic_FormatToolOutput(t *testing.T) {
	// String output
	cb := map[string]any{
		"type":   "tool_output",
		"output": "result: 42",
	}
	text := formatToolOutput(cb)
	if !strings.Contains(text, "result: 42") {
		t.Errorf("expected output text, got %q", text)
	}

	// Content array output
	cb2 := map[string]any{
		"type": "tool_output",
		"content": []any{
			map[string]any{"type": "text", "text": "line 1"},
			map[string]any{"type": "text", "text": "line 2"},
		},
	}
	text = formatToolOutput(cb2)
	if !strings.Contains(text, "line 1") || !strings.Contains(text, "line 2") {
		t.Errorf("expected content array text, got %q", text)
	}
}

func TestAnthropic_CodeExecution_ImageInResult(t *testing.T) {
	cb := map[string]any{
		"type": "code_execution_tool_result",
		"content": []any{
			map[string]any{
				"type":   "code_execution_output",
				"output": "Plot saved",
			},
			map[string]any{
				"type": "image",
				"source": map[string]any{
					"type":       "base64",
					"media_type": "image/png",
					"data":       "iVBOR...",
				},
			},
		},
	}
	text := formatCodeExecutionResult(cb)
	if !strings.Contains(text, "Plot saved") {
		t.Errorf("expected output text, got %q", text)
	}
	if !strings.Contains(text, "[generated image]") {
		t.Errorf("expected image placeholder, got %q", text)
	}
}

func TestAnthropic_ServerTools_DomainLimits(t *testing.T) {
	// AllowedDomains over limit gets truncated
	domains := make([]string, 15)
	for i := range domains {
		domains[i] = fmt.Sprintf("domain%d.com", i)
	}
	st := ai.AnthropicServerTool{
		Type:           "web_search_20250305",
		AllowedDomains: domains,
	}
	tool := convertAnthropicServerTool(st)
	allowed, _ := tool["allowed_domains"].([]string)
	if len(allowed) != 10 {
		t.Errorf("expected allowed_domains truncated to 10, got %d", len(allowed))
	}

	// BlockedDomains over limit gets truncated
	blocked := make([]string, 30)
	for i := range blocked {
		blocked[i] = fmt.Sprintf("block%d.com", i)
	}
	st2 := ai.AnthropicServerTool{
		Type:           "web_search_20250305",
		BlockedDomains: blocked,
	}
	tool2 := convertAnthropicServerTool(st2)
	blockedResult, _ := tool2["blocked_domains"].([]string)
	if len(blockedResult) != 25 {
		t.Errorf("expected blocked_domains truncated to 25, got %d", len(blockedResult))
	}
}

func TestAnthropic_ServerToolDefaultName(t *testing.T) {
	tests := []struct {
		toolType string
		want     string
	}{
		{"web_search_20250305", "web_search"},
		{"code_execution_20250522", "code_execution"},
		{"some_tool_20260101", "some_tool"},
		{"notool", "notool"},
	}
	for _, tt := range tests {
		got := serverToolDefaultName(tt.toolType)
		if got != tt.want {
			t.Errorf("serverToolDefaultName(%q) = %q, want %q", tt.toolType, got, tt.want)
		}
	}
}

func TestAnthropic_ConvertServerTool_DefaultName(t *testing.T) {
	st := ai.AnthropicServerTool{Type: "web_search_20250305"}
	tool := convertAnthropicServerTool(st)
	if tool["name"] != "web_search" {
		t.Errorf("expected default name web_search, got %v", tool["name"])
	}

	// Explicit name wins
	st2 := ai.AnthropicServerTool{Type: "web_search_20250305", Name: "my_search"}
	tool2 := convertAnthropicServerTool(st2)
	if tool2["name"] != "my_search" {
		t.Errorf("expected name my_search, got %v", tool2["name"])
	}
}

func TestAnthropic_CompactionParams(t *testing.T) {
	model := &ai.Model{ID: "claude-opus-4-6", BaseURL: "https://api.anthropic.com", MaxTokens: 8192}
	ctx := ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("test", 1000)},
	}
	opts := &ai.StreamOptions{
		APIKey: "test-key",
		Compaction: &ai.AnthropicCompaction{
			Enabled:       true,
			TriggerTokens: 100000,
			Instructions:  "Keep code snippets and variable names.",
		},
	}

	params := buildAnthropicParams(model, ctx, false, opts)

	cm, ok := params["context_management"].(map[string]any)
	if !ok {
		t.Fatal("expected context_management param")
	}
	edits, ok := cm["edits"].([]map[string]any)
	if !ok || len(edits) != 1 {
		t.Fatal("expected exactly 1 compaction edit")
	}
	edit := edits[0]
	if edit["type"] != "compact_20260112" {
		t.Errorf("expected type=compact_20260112, got %v", edit["type"])
	}
	trigger, _ := edit["trigger"].(map[string]any)
	if trigger == nil || trigger["value"] != 100000 {
		t.Errorf("expected trigger value=100000, got %v", trigger)
	}
	if edit["instructions"] != "Keep code snippets and variable names." {
		t.Errorf("unexpected instructions: %v", edit["instructions"])
	}
}

func TestAnthropic_CompactionBetaHeader(t *testing.T) {
	model := &ai.Model{ID: "claude-opus-4-6", BaseURL: "https://api.anthropic.com"}
	opts := &ai.StreamOptions{
		Compaction: &ai.AnthropicCompaction{Enabled: true},
	}

	headers := buildAnthropicHeaders(model, "test-key", false, opts, false)
	beta := headers["anthropic-beta"]
	if !strings.Contains(beta, "compact-2026-01-12") {
		t.Errorf("expected compact beta in header, got %s", beta)
	}
}

func TestAnthropic_CompactionDisabled_NoBeta(t *testing.T) {
	model := &ai.Model{ID: "claude-opus-4-6", BaseURL: "https://api.anthropic.com"}
	opts := &ai.StreamOptions{
		Compaction: &ai.AnthropicCompaction{Enabled: false},
	}

	headers := buildAnthropicHeaders(model, "test-key", false, opts, false)
	beta := headers["anthropic-beta"]
	if strings.Contains(beta, "compact") {
		t.Errorf("did not expect compact beta when disabled, got %s", beta)
	}
}

func TestAnthropic_FormatWebFetchResult(t *testing.T) {
	cb := map[string]any{
		"type": "web_fetch_tool_result",
		"content": []any{
			map[string]any{
				"type":    "web_fetch_result",
				"url":     "https://golang.org/doc/tutorial",
				"content": "Welcome to the Go tutorial...",
			},
		},
	}
	text := formatWebFetchResult(cb)
	if !strings.Contains(text, "https://golang.org/doc/tutorial") {
		t.Errorf("expected URL in output, got %q", text)
	}
	if !strings.Contains(text, "Welcome to the Go tutorial") {
		t.Errorf("expected content in output, got %q", text)
	}
}

func TestAnthropic_CompactionStopReason(t *testing.T) {
	got := mapAnthropicStopReason("compaction")
	if got != ai.StopReasonStop {
		t.Errorf("expected StopReasonStop for compaction, got %v", got)
	}
}

func TestAnthropic_ServerToolsSentForAnyModel(t *testing.T) {
	// Backward-compatible: if model capabilities are not declared, send configured tools.
	model := &ai.Model{ID: "custom-model", BaseURL: "https://my-proxy.example.com", MaxTokens: 8192}
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("test", 1000)}}
	opts := &ai.StreamOptions{APIKey: "test-key", ServerTools: []ai.AnthropicServerTool{{Type: "web_search_20250305"}}}

	params := buildAnthropicParams(model, ctx, false, opts)
	tools, ok := params["tools"].([]map[string]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected 1 server tool for custom model, got %v", params["tools"])
	}
}

func TestAnthropic_ServerToolsFilteredByModelCapability(t *testing.T) {
	model := &ai.Model{
		ID:          "custom-model",
		BaseURL:     "https://my-proxy.example.com",
		MaxTokens:   8192,
		ServerTools: []string{"web_search_20260209"},
	}
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("test", 1000)}}
	opts := &ai.StreamOptions{APIKey: "test-key", ServerTools: []ai.AnthropicServerTool{
		{Type: "web_search_20260209"},
		{Type: "web_fetch_20260209"},
	}}

	params := buildAnthropicParams(model, ctx, false, opts)
	tools, ok := params["tools"].([]map[string]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected only supported tool, got %v", params["tools"])
	}
	if tools[0]["type"] != "web_search_20260209" {
		t.Fatalf("expected web_search_20260209, got %v", tools[0]["type"])
	}
}

func TestAnthropic_CompactionSkippedForUnsupportedModel(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet-4-20250514", BaseURL: "https://api.anthropic.com", MaxTokens: 8192, Compaction: false}
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("test", 1000)}}
	opts := &ai.StreamOptions{APIKey: "test-key", Compaction: &ai.AnthropicCompaction{Enabled: true, TriggerTokens: 100000}}

	params := buildAnthropicParams(model, ctx, false, opts)
	if _, ok := params["context_management"]; ok {
		t.Error("expected no context_management for unsupported model")
	}
}

func TestAnthropic_ServerToolsBetaHeaders_AnyModel(t *testing.T) {
	// Beta headers are added for any model using the Anthropic API.
	model := &ai.Model{ID: "my-custom-claude", BaseURL: "https://proxy.example.com"}
	opts := &ai.StreamOptions{
		ServerTools: []ai.AnthropicServerTool{
			{Type: "web_search_20250305"},
		},
	}

	headers := buildAnthropicHeaders(model, "test-key", false, opts, false)
	beta := headers["anthropic-beta"]
	if !strings.Contains(beta, "web-search-2025-03-05") {
		t.Errorf("expected web-search beta for custom model, got %s", beta)
	}
}

// --- OnPayload hook tests ---

func TestAnthropic_OnPayload_Called(t *testing.T) {
	srv := mockSSEServer(t, "anthropic_simple_response.sse")
	defer srv.Close()

	var capturedPayload map[string]any
	model := anthropicModel(srv.URL)
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}
	opts := &ai.StreamOptions{
		APIKey: "test-key",
		OnPayload: func(payload any, _ *ai.Model) any {
			if m, ok := payload.(map[string]any); ok {
				capturedPayload = m
			}
			return nil // keep original
		},
	}

	stream := StreamAnthropic(context.Background(), model, ctx, opts)
	stream.Result()

	if capturedPayload == nil {
		t.Fatal("OnPayload was not called")
	}
	if capturedPayload["model"] != model.ID {
		t.Errorf("expected model %q in payload, got %v", model.ID, capturedPayload["model"])
	}
}

func TestAnthropic_OnPayload_WrongTypeNoPanic(t *testing.T) {
	// Returning a wrong type from OnPayload must not panic (checked assertion).
	srv := mockSSEServer(t, "anthropic_simple_response.sse")
	defer srv.Close()

	model := anthropicModel(srv.URL)
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}
	opts := &ai.StreamOptions{
		APIKey: "test-key",
		OnPayload: func(payload any, _ *ai.Model) any {
			return "not-a-map" // wrong type — should be silently ignored
		},
	}

	// Must not panic.
	stream := StreamAnthropic(context.Background(), model, ctx, opts)
	result := stream.Result()
	if result == nil {
		t.Fatal("expected a result")
	}
}

func TestAnthropic_ConvertMessages_StringUserGetsCacheControl(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet", BaseURL: "https://api.anthropic.com", MaxTokens: 8192}
	msgs := []ai.Message{
		ai.NewUserMsg("hello", 1000),
	}

	result := convertAnthropicMessages(msgs, model, false, ai.CacheLong)

	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	content, ok := result[0]["content"].([]map[string]any)
	if !ok || len(content) != 1 {
		t.Fatalf("expected block-form content with 1 block, got %v", result[0]["content"])
	}
	cc, hasCacheControl := content[0]["cache_control"]
	if !hasCacheControl {
		t.Fatal("expected cache_control on last user message block")
	}
	ccMap, ok := cc.(map[string]any)
	if !ok {
		t.Fatalf("expected cache_control to be map, got %T", cc)
	}
	if ccMap["type"] != "ephemeral" {
		t.Errorf("expected cache_control type=ephemeral, got %v", ccMap["type"])
	}
}

// TestAnthropic_AuthErrorRefreshRetry verifies that a 401/authentication_error
// triggers a token refresh via RefreshAPIKey and retries successfully.
func TestAnthropic_AuthErrorRefreshRetry(t *testing.T) {
	prev := anthropicRetryDelayFn
	anthropicRetryDelayFn = func(_ string, _ int, _ *int) time.Duration { return 0 }
	t.Cleanup(func() { anthropicRetryDelayFn = prev })

	authErrorData := loadFixture(t, "anthropic_auth_error.sse")
	successData := loadFixture(t, "anthropic_simple_response.sse")

	attempts := 0
	srv := mockSSEServerFunc(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		attempts++
		if attempts == 1 {
			w.Write(authErrorData)
		} else {
			w.Write(successData)
		}
	})
	defer srv.Close()

	model := anthropicModel(srv.URL)
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello!", 1000)}}

	refreshCalled := false
	opts := &ai.StreamOptions{
		APIKey: "expired-token",
		RefreshAPIKey: func(provider string) string {
			refreshCalled = true
			return "fresh-token"
		},
	}

	stream := StreamAnthropic(context.Background(), model, ctx, opts)
	_ = collectEvents(t, stream)

	result := stream.Result()
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.StopReason == ai.StopReasonError {
		t.Errorf("expected success after refresh, got error: %s", result.ErrorMessage)
	}
	if !refreshCalled {
		t.Error("expected RefreshAPIKey to be called on auth error")
	}
	if attempts != 2 {
		t.Errorf("expected 2 attempts (1 auth error + 1 success), got %d", attempts)
	}
}

// TestAnthropic_AuthErrorHTTP401RefreshRetry verifies that an HTTP-level 401
// triggers token refresh and retry.
func TestAnthropic_AuthErrorHTTP401RefreshRetry(t *testing.T) {
	prev := anthropicRetryDelayFn
	anthropicRetryDelayFn = func(_ string, _ int, _ *int) time.Duration { return 0 }
	t.Cleanup(func() { anthropicRetryDelayFn = prev })

	successData := loadFixture(t, "anthropic_simple_response.sse")

	attempts := 0
	srv := mockSSEServerFunc(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"Invalid authentication credentials"}}`))
		} else {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.Write(successData)
		}
	})
	defer srv.Close()

	model := anthropicModel(srv.URL)
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello!", 1000)}}

	refreshCalled := false
	opts := &ai.StreamOptions{
		APIKey: "expired-token",
		RefreshAPIKey: func(provider string) string {
			refreshCalled = true
			return "fresh-token"
		},
	}

	stream := StreamAnthropic(context.Background(), model, ctx, opts)
	_ = collectEvents(t, stream)

	result := stream.Result()
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.StopReason == ai.StopReasonError {
		t.Errorf("expected success after refresh, got error: %s", result.ErrorMessage)
	}
	if !refreshCalled {
		t.Error("expected RefreshAPIKey to be called on HTTP 401")
	}
	if attempts != 2 {
		t.Errorf("expected 2 attempts (1 HTTP 401 + 1 success), got %d", attempts)
	}
}

// TestAnthropic_AuthErrorNoRefreshFails verifies that without RefreshAPIKey,
// an auth error is surfaced to the caller.
func TestAnthropic_AuthErrorNoRefreshFails(t *testing.T) {
	prev := anthropicRetryDelayFn
	anthropicRetryDelayFn = func(_ string, _ int, _ *int) time.Duration { return 0 }
	t.Cleanup(func() { anthropicRetryDelayFn = prev })

	authErrorData := loadFixture(t, "anthropic_auth_error.sse")

	srv := mockSSEServerFunc(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write(authErrorData)
	})
	defer srv.Close()

	model := anthropicModel(srv.URL)
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello!", 1000)}}
	opts := &ai.StreamOptions{APIKey: "expired-token"}

	stream := StreamAnthropic(context.Background(), model, ctx, opts)
	_ = collectEvents(t, stream)

	result := stream.Result()
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.StopReason != ai.StopReasonError {
		t.Errorf("expected error without refresh, got %s", result.StopReason)
	}
	if !strings.Contains(result.ErrorMessage, "authentication_error") {
		t.Errorf("expected authentication_error in message, got %q", result.ErrorMessage)
	}
}

// TestAnthropic_NoEmptyBetaHeader_APIKey verifies that when no beta features
// are needed, the anthropic-beta header is omitted entirely (not sent empty).
// The Anthropic API rejects empty beta headers with:
//
//	400 Unexpected value(s) `` for the `anthropic-beta` header.
func TestAnthropic_NoEmptyBetaHeader_APIKey(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet-4-20250514", BaseURL: "https://api.anthropic.com"}
	headers := buildAnthropicHeaders(model, "test-key", false, nil, false)
	if v, ok := headers["anthropic-beta"]; ok {
		t.Errorf("expected no anthropic-beta header when no betas needed, got %q", v)
	}
}

// TestAnthropic_NoEmptyBetaHeader_OAuth_NoPrefix verifies that an OAuth model
// with an empty oauth-beta-prefix and no feature betas does not emit an empty
// anthropic-beta header.
func TestAnthropic_NoEmptyBetaHeader_OAuth_NoPrefix(t *testing.T) {
	model := &ai.Model{
		ID:      "claude-sonnet-4-20250514",
		BaseURL: "https://api.anthropic.com",
		Headers: map[string]string{
			"x-anthropic-oauth-beta-prefix": "",
		},
	}
	headers := buildAnthropicHeaders(model, "test-token", true, nil, false)
	if v, ok := headers["anthropic-beta"]; ok && v == "" {
		t.Errorf("expected no empty anthropic-beta header, got empty value")
	}
}

// TestAnthropic_OAuthBetaPrefix_NoTrailingComma verifies the oauth prefix is
// joined with feature betas without a trailing/leading empty entry that would
// produce a value like "oauth-2025-04-20," (rejected by the API).
func TestAnthropic_OAuthBetaPrefix_NoTrailingComma(t *testing.T) {
	model := &ai.Model{
		ID:      "claude-sonnet-4-20250514",
		BaseURL: "https://api.anthropic.com",
		Headers: map[string]string{
			"x-anthropic-oauth-beta-prefix": "oauth-2025-04-20",
		},
	}
	headers := buildAnthropicHeaders(model, "test-token", true, nil, false)
	beta := headers["anthropic-beta"]
	if beta != "oauth-2025-04-20" {
		t.Errorf("expected clean oauth prefix only, got %q", beta)
	}
	for _, part := range strings.Split(beta, ",") {
		if strings.TrimSpace(part) == "" {
			t.Errorf("anthropic-beta has empty entry: %q", beta)
		}
	}
}

func TestJoinBetaParts(t *testing.T) {
	cases := []struct {
		in  []string
		out string
	}{
		{[]string{}, ""},
		{[]string{""}, ""},
		{[]string{"", ""}, ""},
		{[]string{"a"}, "a"},
		{[]string{"a", ""}, "a"},
		{[]string{"", "a"}, "a"},
		{[]string{"a,b", ""}, "a,b"},
		{[]string{"a", "b,c"}, "a,b,c"},
		{[]string{"a,", ",b"}, "a,b"},
		{[]string{" a , b "}, "a,b"},
	}
	for _, c := range cases {
		got := joinBetaParts(c.in...)
		if got != c.out {
			t.Errorf("joinBetaParts(%v) = %q, want %q", c.in, got, c.out)
		}
	}
}

// TestAnthropic_ConvertMessages_ThinkingEmptyTextWithSignature ensures that a
// thinking block whose Thinking text is empty but whose ThinkingSignature is
// non-empty is forwarded verbatim as a {"type":"thinking",...} block rather
// than being silently dropped.  Dropping it changes the assistant-message
// structure and triggers a 400 "cannot be modified" from the Anthropic API.
func TestAnthropic_ConvertMessages_ThinkingEmptyTextWithSignature(t *testing.T) {
	model := &ai.Model{
		ID: "claude-opus-4-7", BaseURL: "https://api.anthropic.com", MaxTokens: 8192,
		API: ai.ApiAnthropicMessages, Provider: ai.ProviderAnthropic,
	}

	// Simulate a thinking block where the model returned an empty thinking text
	// but a valid signature (e.g. zero-budget or adaptive thinking with no output).
	assistantMsg := ai.AssistantMessage{
		Role:     "assistant",
		Provider: ai.ProviderAnthropic,
		API:      ai.ApiAnthropicMessages,
		Model:    "claude-opus-4-7",
		Content: []ai.AssistantContent{
			func() ai.AssistantContent {
				c := ai.NewThinkingContent("") // empty thinking text
				c.Thinking.ThinkingSignature = "sig_empty_thinking"
				return c
			}(),
			ai.NewTextContent("My answer."),
		},
	}

	msgs := []ai.Message{
		ai.NewUserMsg("Hello?", 1000),
		ai.NewAssistantMsg(assistantMsg),
	}

	result := convertAnthropicMessages(msgs, model, false, ai.CacheNone)

	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	aBlocks, ok := result[1]["content"].([]map[string]any)
	if !ok {
		t.Fatal("content is not []map[string]any")
	}
	// Must keep both blocks: thinking (even with empty text) and text.
	if len(aBlocks) != 2 {
		t.Fatalf("expected 2 blocks (thinking + text), got %d — thinking block was wrongly dropped", len(aBlocks))
	}
	if aBlocks[0]["type"] != "thinking" {
		t.Errorf("expected block[0] type=thinking, got %v", aBlocks[0]["type"])
	}
	if aBlocks[0]["thinking"] != "" {
		t.Errorf("expected empty thinking text, got %q", aBlocks[0]["thinking"])
	}
	if aBlocks[0]["signature"] != "sig_empty_thinking" {
		t.Errorf("expected signature 'sig_empty_thinking', got %q", aBlocks[0]["signature"])
	}
}

// TestAnthropic_ConvertMessages_ThinkingWhitespaceTextWithSignature ensures
// that whitespace-only thinking text does not cause the block to be dropped
// when a ThinkingSignature is present.
func TestAnthropic_ConvertMessages_ThinkingWhitespaceTextWithSignature(t *testing.T) {
	model := &ai.Model{
		ID: "claude-opus-4-7", BaseURL: "https://api.anthropic.com", MaxTokens: 8192,
		API: ai.ApiAnthropicMessages, Provider: ai.ProviderAnthropic,
	}

	assistantMsg := ai.AssistantMessage{
		Role:     "assistant",
		Provider: ai.ProviderAnthropic,
		API:      ai.ApiAnthropicMessages,
		Model:    "claude-opus-4-7",
		Content: []ai.AssistantContent{
			func() ai.AssistantContent {
				c := ai.NewThinkingContent("   ") // whitespace only
				c.Thinking.ThinkingSignature = "sig_ws"
				return c
			}(),
			ai.NewTextContent("Answer."),
		},
	}

	msgs := []ai.Message{
		ai.NewUserMsg("Hi?", 1000),
		ai.NewAssistantMsg(assistantMsg),
	}

	result := convertAnthropicMessages(msgs, model, false, ai.CacheNone)
	aBlocks := result[1]["content"].([]map[string]any)
	if len(aBlocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d — whitespace thinking block was wrongly dropped", len(aBlocks))
	}
	if aBlocks[0]["type"] != "thinking" {
		t.Errorf("expected block[0] type=thinking, got %v", aBlocks[0]["type"])
	}
}

// TestAnthropic_ConvertMessages_RedactedThinkingCrossModelRoundtrip verifies
// the full pipeline: a redacted thinking block that survived TransformMessages
// (isSameProvider=true, isSameModel=false) is correctly serialised as a
// {"type":"redacted_thinking","data":"..."} block by convertAnthropicMessages,
// NOT as a {"type":"thinking",...} block.
func TestAnthropic_ConvertMessages_RedactedThinkingCrossModelRoundtrip(t *testing.T) {
	model := &ai.Model{
		ID: "claude-opus-4-7-20250514", BaseURL: "https://api.anthropic.com", MaxTokens: 8192,
		API: ai.ApiAnthropicMessages, Provider: ai.ProviderAnthropic,
	}

	// Simulate a redacted block that was preserved verbatim through TransformMessages.
	redactedBlock := ai.NewThinkingContent("")
	redactedBlock.Thinking.Redacted = true
	redactedBlock.Thinking.ThinkingSignature = "EncryptedOpaqueBlobXYZ=="

	assistantMsg := ai.AssistantMessage{
		Role:     "assistant",
		Provider: ai.ProviderAnthropic,
		API:      ai.ApiAnthropicMessages,
		Model:    "claude-opus-4-7", // original model ID differs from current
		Content: []ai.AssistantContent{
			redactedBlock,
			ai.NewTextContent("Here is my answer."),
		},
	}

	msgs := []ai.Message{
		ai.NewUserMsg("Question?", 1000),
		ai.NewAssistantMsg(assistantMsg),
	}

	result := convertAnthropicMessages(msgs, model, false, ai.CacheNone)

	aBlocks, ok := result[1]["content"].([]map[string]any)
	if !ok || len(aBlocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(aBlocks))
	}
	if aBlocks[0]["type"] != "redacted_thinking" {
		t.Errorf("expected type=redacted_thinking, got %v", aBlocks[0]["type"])
	}
	if aBlocks[0]["data"] != "EncryptedOpaqueBlobXYZ==" {
		t.Errorf("expected data='EncryptedOpaqueBlobXYZ==', got %v", aBlocks[0]["data"])
	}
	if _, hasThinking := aBlocks[0]["thinking"]; hasThinking {
		t.Error("redacted_thinking block must not have a 'thinking' field")
	}
}

// TestAnthropic_OnRetryCallback verifies StreamOptions.OnRetry is invoked
// once per retry with a 1-based attempt number, a non-zero (or zero via
// our injection) delay, and the last provider error message.
func TestAnthropic_OnRetryCallback(t *testing.T) {
	prev := anthropicRetryDelayFn
	anthropicRetryDelayFn = func(_ string, _ int, _ *int) time.Duration { return 0 }
	t.Cleanup(func() { anthropicRetryDelayFn = prev })

	overloadedData := loadFixture(t, "anthropic_error.sse")
	successData := loadFixture(t, "anthropic_simple_response.sse")

	attempts := 0
	srv := mockSSEServerFunc(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		attempts++
		if attempts < 3 {
			w.Write(overloadedData)
		} else {
			w.Write(successData)
		}
	})
	defer srv.Close()

	type call struct {
		attempt int
		errMsg  string
	}
	var calls []call
	opts := &ai.StreamOptions{
		APIKey: "test-key",
		OnRetry: func(attempt int, delaySeconds float64, errMsg string) {
			calls = append(calls, call{attempt: attempt, errMsg: errMsg})
		},
	}

	model := anthropicModel(srv.URL)
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello!", 1000)}}

	stream := StreamAnthropic(context.Background(), model, ctx, opts)
	collectEvents(t, stream)

	if attempts != 3 {
		t.Fatalf("expected 3 provider attempts, got %d", attempts)
	}
	// Two retries happened (attempts 2 and 3), so OnRetry should fire twice
	// with 1-based attempt numbers.
	if len(calls) != 2 {
		t.Fatalf("expected 2 OnRetry calls, got %d: %+v", len(calls), calls)
	}
	if calls[0].attempt != 1 || calls[1].attempt != 2 {
		t.Errorf("expected attempt numbers 1,2 got %d,%d", calls[0].attempt, calls[1].attempt)
	}
	for i, c := range calls {
		if !strings.Contains(c.errMsg, "Overloaded") {
			t.Errorf("call %d: errMsg missing 'Overloaded': %q", i, c.errMsg)
		}
	}
}

// TestAnthropic_ToolResultMeta_RenderedInRequest exercises the full
// provider-bound path (TransformMessages -> convertAnthropicMessages) and
// asserts the rendered meta line lands in the tool_result content string,
// while the original persisted message stays clean.
func TestAnthropic_ToolResultMeta_RenderedInRequest(t *testing.T) {
	model := &ai.Model{ID: "claude-sonnet", Provider: ai.ProviderAnthropic, API: ai.ApiAnthropicMessages, BaseURL: "https://api.anthropic.com", MaxTokens: 8192}

	orig := ai.ToolResultMessage{
		Role:       ai.RoleToolResult,
		ToolCallID: "toolu_01",
		ToolName:   "Bash",
		Content:    []ai.ToolResultContent{{Type: "text", Text: "command output"}},
		Meta:       map[string]string{"hash": "deadbeef00112233"},
	}
	msgs := []ai.Message{
		ai.NewUserMsg("run it", 1000),
		ai.NewAssistantMsg(ai.AssistantMessage{
			Role:     "assistant",
			Provider: ai.ProviderAnthropic, API: ai.ApiAnthropicMessages, Model: "claude-sonnet",
			Content: []ai.AssistantContent{
				ai.NewToolCallContent("toolu_01", "Bash", map[string]any{"command": "echo hi"}),
			},
			StopReason: ai.StopReasonToolUse,
		}),
		ai.NewToolResultMsg(orig),
	}

	transformed := TransformMessages(msgs, model, normalizeAnthropicToolCallID)
	// convertAnthropicMessages runs TransformMessages internally; calling it
	// on already-transformed input also proves the meta render is idempotent.
	result := convertAnthropicMessages(transformed, model, false, ai.CacheNone)

	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
	content, ok := result[2]["content"].([]map[string]any)
	if !ok || len(content) != 1 {
		t.Fatalf("expected single tool_result block, got %v", result[2]["content"])
	}
	got, _ := content[0]["content"].(string)
	want := "command output\n[hash: deadbeef00112233]"
	if got != want {
		t.Errorf("expected tool_result content %q, got %q", want, got)
	}
	// Persisted message untouched.
	if len(orig.Content) != 1 || orig.Content[0].Text != "command output" {
		t.Fatalf("original message mutated: %+v", orig.Content)
	}
}
