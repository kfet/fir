package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/kfet/pi-go/pkg/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openaiTestModel(serverURL string) *ai.Model {
	return &ai.Model{
		ID:            "gpt-4o",
		Name:          "GPT-4o",
		Api:           ai.ApiOpenAICompletions,
		Provider:      ai.ProviderOpenAI,
		BaseURL:       serverURL,
		Reasoning:     false,
		Input:         []ai.InputModality{ai.InputText, ai.InputImage},
		Cost:          ai.ModelCost{Input: 2.5, Output: 10.0, CacheRead: 1.25, CacheWrite: 0},
		ContextWindow: 128000,
		MaxTokens:     16384,
	}
}

func TestStreamOpenAI_SimpleResponse(t *testing.T) {
	srv := mockSSEServer(t, "openai_simple_response.sse")
	defer srv.Close()

	model := openaiTestModel(srv.URL)
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}

	stream := StreamOpenAICompletions(context.Background(), model, ctx, &ai.StreamOptions{ApiKey: "sk-test"})
	events, result := stream.Collect()
	require.NotNil(t, result)

	assert.Equal(t, ai.StopReasonStop, result.StopReason)

	// Check text content
	require.True(t, len(result.Content) > 0)
	require.True(t, result.Content[0].IsText())
	assert.Equal(t, "Hello! How can I help?", result.Content[0].Text.Text)

	// Check usage
	assert.Equal(t, 25, result.Usage.Input)
	assert.Equal(t, 12, result.Usage.Output)

	// Check events
	hasStart := false
	hasTextDelta := false
	hasDone := false
	for _, e := range events {
		switch e.Type {
		case ai.EventStart:
			hasStart = true
		case ai.EventTextDelta:
			hasTextDelta = true
		case ai.EventDone:
			hasDone = true
		}
	}
	assert.True(t, hasStart)
	assert.True(t, hasTextDelta)
	assert.True(t, hasDone)
}

func TestStreamOpenAI_NoApiKey(t *testing.T) {
	model := openaiTestModel("http://localhost:0")
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}

	t.Setenv("OPENAI_API_KEY", "")

	stream := StreamOpenAICompletions(context.Background(), model, ctx, &ai.StreamOptions{})
	result := stream.Result()
	require.NotNil(t, result)
	assert.Equal(t, ai.StopReasonError, result.StopReason)
	assert.Contains(t, result.ErrorMessage, "no API key")
}

func TestStreamOpenAI_RequestBody(t *testing.T) {
	var capturedBody []byte
	srv := tryNewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capturedBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.Write(loadFixture(t, "openai_simple_response.sse"))
	}))
	defer srv.Close()

	model := openaiTestModel(srv.URL)
	temp := 0.7
	ctx := ai.Context{
		SystemPrompt: "You are helpful.",
		Messages:     []ai.Message{ai.NewUserMsg("Hello", 0)},
		Tools: []ai.Tool{
			{Name: "read", Description: "Read a file", Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"path": map[string]any{"type": "string"}},
				"required":   []string{"path"},
			}},
		},
	}

	stream := StreamOpenAICompletions(context.Background(), model, ctx, &ai.StreamOptions{
		ApiKey:      "sk-test",
		Temperature: &temp,
	})
	stream.Result()

	var body map[string]any
	require.NoError(t, json.Unmarshal(capturedBody, &body))

	assert.Equal(t, "gpt-4o", body["model"])
	assert.Equal(t, true, body["stream"])
	assert.Equal(t, 0.7, body["temperature"])
	assert.NotNil(t, body["messages"])
	assert.NotNil(t, body["tools"])
}

func TestStreamOpenAI_Headers(t *testing.T) {
	var capturedHeaders http.Header
	srv := tryNewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.Write(loadFixture(t, "openai_simple_response.sse"))
	}))
	defer srv.Close()

	model := openaiTestModel(srv.URL)
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}

	stream := StreamOpenAICompletions(context.Background(), model, ctx, &ai.StreamOptions{ApiKey: "sk-test"})
	stream.Result()

	assert.Equal(t, "Bearer sk-test", capturedHeaders.Get("Authorization"))
	assert.Equal(t, "application/json", capturedHeaders.Get("Content-Type"))
}

func TestStreamOpenAI_HTTPError(t *testing.T) {
	srv := mockJSONServer(t, 429, []byte(`{"error":{"message":"rate limited"}}`))
	defer srv.Close()

	model := openaiTestModel(srv.URL)
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}

	stream := StreamOpenAICompletions(context.Background(), model, ctx, &ai.StreamOptions{ApiKey: "sk-test"})
	result := stream.Result()
	require.NotNil(t, result)
	assert.Equal(t, ai.StopReasonError, result.StopReason)
	assert.Contains(t, result.ErrorMessage, "429")
}

func TestMapOpenAIStopReason(t *testing.T) {
	assert.Equal(t, ai.StopReasonStop, mapOpenAIStopReason("stop"))
	assert.Equal(t, ai.StopReasonLength, mapOpenAIStopReason("length"))
	assert.Equal(t, ai.StopReasonToolUse, mapOpenAIStopReason("tool_calls"))
	assert.Equal(t, ai.StopReasonError, mapOpenAIStopReason("content_filter"))
	assert.Equal(t, ai.StopReasonStop, mapOpenAIStopReason("unknown"))
}

func TestStreamSimpleOpenAI_NoApiKey(t *testing.T) {
	model := openaiTestModel("http://localhost:0")
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}

	t.Setenv("OPENAI_API_KEY", "")

	stream := StreamSimpleOpenAICompletions(context.Background(), model, ctx, &ai.SimpleStreamOptions{})
	result := stream.Result()
	require.NotNil(t, result)
	assert.Equal(t, ai.StopReasonError, result.StopReason)
}

func TestRegisterOpenAICompletions(t *testing.T) {
	reg := ai.NewRegistry()
	RegisterOpenAICompletions(reg)

	p := reg.GetApiProvider(ai.ApiOpenAICompletions)
	require.NotNil(t, p)
	assert.Equal(t, ai.ApiOpenAICompletions, p.Api)
}

// --- detectCompat / getCompat tests ---

func TestDetectCompat_OpenAI(t *testing.T) {
	m := &ai.Model{Provider: "openai", BaseURL: "https://api.openai.com"}
	c := detectCompat(m)
	assert.True(t, c.SupportsStore)
	assert.True(t, c.SupportsDeveloperRole)
	assert.True(t, c.SupportsReasoningEffort)
	assert.Equal(t, ai.MaxTokensFieldMaxCompletionTokens, c.MaxTokensField)
	assert.False(t, c.RequiresMistralToolIds)
	assert.Equal(t, ai.ThinkingFormatOpenAI, c.ThinkingFormat)
}

func TestDetectCompat_Mistral(t *testing.T) {
	m := &ai.Model{Provider: "mistral", BaseURL: "https://api.mistral.ai"}
	c := detectCompat(m)
	assert.False(t, c.SupportsStore)
	assert.False(t, c.SupportsDeveloperRole)
	assert.Equal(t, ai.MaxTokensFieldMaxTokens, c.MaxTokensField)
	assert.True(t, c.RequiresMistralToolIds)
	assert.True(t, c.RequiresThinkingAsText)
	assert.True(t, c.RequiresToolResultName)
}

func TestDetectCompat_Zai(t *testing.T) {
	m := &ai.Model{Provider: "zai", BaseURL: "https://api.z.ai"}
	c := detectCompat(m)
	assert.False(t, c.SupportsStore)
	assert.False(t, c.SupportsReasoningEffort)
	assert.Equal(t, ai.ThinkingFormatZAI, c.ThinkingFormat)
}

func TestDetectCompat_XAI(t *testing.T) {
	m := &ai.Model{Provider: "xai", BaseURL: "https://api.x.ai"}
	c := detectCompat(m)
	assert.False(t, c.SupportsReasoningEffort)
}

func TestDetectCompat_Cerebras(t *testing.T) {
	m := &ai.Model{Provider: "cerebras", BaseURL: "https://api.cerebras.ai"}
	c := detectCompat(m)
	assert.False(t, c.SupportsStore)
	assert.False(t, c.SupportsDeveloperRole)
}

func TestDetectCompat_Chutes(t *testing.T) {
	m := &ai.Model{Provider: "chutes", BaseURL: "https://api.chutes.ai"}
	c := detectCompat(m)
	assert.Equal(t, ai.MaxTokensFieldMaxTokens, c.MaxTokensField)
}

func TestGetCompat_MergesExplicit(t *testing.T) {
	trueVal := true
	m := &ai.Model{
		Provider: "openai",
		BaseURL:  "https://api.openai.com",
		Compat: &ai.OpenAICompletionsCompat{
			RequiresMistralToolIds: &trueVal,
			MaxTokensField:        ai.MaxTokensFieldMaxTokens,
		},
	}
	c := getCompat(m)
	assert.True(t, c.RequiresMistralToolIds) // overridden
	assert.Equal(t, ai.MaxTokensFieldMaxTokens, c.MaxTokensField) // overridden
	assert.True(t, c.SupportsStore)          // detected default
}

func TestGetCompat_NilCompat(t *testing.T) {
	m := &ai.Model{Provider: "openai", BaseURL: "https://api.openai.com"}
	c := getCompat(m)
	assert.True(t, c.SupportsStore)
}

// --- Tool ID normalization tests ---

func TestNormalizeMistralToolID(t *testing.T) {
	assert.Equal(t, "abc123XYZ", normalizeMistralToolID("abc123XYZ"))
	assert.Equal(t, "callABCDE", normalizeMistralToolID("call"))
	assert.Equal(t, "abcdefghi", normalizeMistralToolID("abcdefghijklmno"))
	assert.Equal(t, "abc12ABCD", normalizeMistralToolID("abc-12_!@"))
}

func TestNormalizeOpenAIToolCallID_PipeSeparated(t *testing.T) {
	compat := resolvedCompat{}
	m := &ai.Model{Provider: "generic"}
	// Pipe-separated: extract call ID part
	result := normalizeOpenAIToolCallID("call_abc123|some/long+id=", m, compat)
	assert.Equal(t, "call_abc123", result)
}

func TestNormalizeOpenAIToolCallID_OpenAITruncation(t *testing.T) {
	compat := resolvedCompat{}
	m := &ai.Model{Provider: "openai"}
	longID := "call_" + string(make([]byte, 50))
	result := normalizeOpenAIToolCallID(longID, m, compat)
	assert.LessOrEqual(t, len(result), 40)
}

func TestNormalizeOpenAIToolCallID_MistralFormat(t *testing.T) {
	compat := resolvedCompat{RequiresMistralToolIds: true}
	m := &ai.Model{Provider: "mistral"}
	result := normalizeOpenAIToolCallID("call_abcdef", m, compat)
	assert.Len(t, result, 9)
}

func TestNormalizeOpenAIToolCallID_CopilotClaude(t *testing.T) {
	compat := resolvedCompat{}
	m := &ai.Model{Provider: "github-copilot", ID: "claude-3.5-sonnet"}
	result := normalizeOpenAIToolCallID("id-with-special+chars/ok", m, compat)
	assert.Equal(t, "id-with-special_chars_ok", result)
}

// --- hasToolHistory tests ---

func TestHasToolHistory_NoTools(t *testing.T) {
	msgs := []ai.Message{ai.NewUserMsg("hello", 0)}
	assert.False(t, hasToolHistory(msgs))
}

func TestHasToolHistory_WithToolResult(t *testing.T) {
	msgs := []ai.Message{
		ai.NewToolResultMsg(ai.ToolResultMessage{
			Role:       ai.RoleToolResult,
			ToolCallID: "call_1",
			Content:    []ai.ToolResultContent{{Type: "text", Text: "result"}},
		}),
	}
	assert.True(t, hasToolHistory(msgs))
}

func TestHasToolHistory_WithToolCall(t *testing.T) {
	msgs := []ai.Message{
		ai.NewAssistantMsg(ai.AssistantMessage{
			Role: ai.RoleAssistant,
			Content: []ai.AssistantContent{
				ai.NewToolCallContent("call_1", "read", map[string]any{"path": "foo"}),
			},
		}),
	}
	assert.True(t, hasToolHistory(msgs))
}

// --- buildOpenAIRequestBody tests ---

func TestBuildOpenAIRequestBody_DeveloperRole(t *testing.T) {
	m := &ai.Model{
		Provider:  "openai",
		BaseURL:   "https://api.openai.com",
		ID:        "o1",
		Reasoning: true,
		MaxTokens: 16384,
	}
	ctx := ai.Context{
		SystemPrompt: "You are helpful.",
		Messages:     []ai.Message{ai.NewUserMsg("Hello", 0)},
	}
	body, err := buildOpenAIRequestBody(m, ctx, &ai.StreamOptions{})
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))

	msgs, ok := parsed["messages"].([]any)
	require.True(t, ok)
	require.True(t, len(msgs) >= 1)
	first := msgs[0].(map[string]any)
	assert.Equal(t, "developer", first["role"])
}

func TestBuildOpenAIRequestBody_SystemRole(t *testing.T) {
	m := &ai.Model{
		Provider:  "openai",
		BaseURL:   "https://api.openai.com",
		ID:        "gpt-4o",
		Reasoning: false,
		MaxTokens: 16384,
	}
	ctx := ai.Context{
		SystemPrompt: "You are helpful.",
		Messages:     []ai.Message{ai.NewUserMsg("Hello", 0)},
	}
	body, err := buildOpenAIRequestBody(m, ctx, &ai.StreamOptions{})
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))

	msgs, ok := parsed["messages"].([]any)
	require.True(t, ok)
	first := msgs[0].(map[string]any)
	assert.Equal(t, "system", first["role"])
}

func TestBuildOpenAIRequestBody_MistralMaxTokens(t *testing.T) {
	m := &ai.Model{
		Provider:  "mistral",
		BaseURL:   "https://api.mistral.ai",
		ID:        "devstral",
		MaxTokens: 32000,
	}
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}
	body, err := buildOpenAIRequestBody(m, ctx, &ai.StreamOptions{})
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))

	assert.NotNil(t, parsed["max_tokens"])
	assert.Nil(t, parsed["max_completion_tokens"])
}

func TestBuildOpenAIRequestBody_StoreAndStreamOptions(t *testing.T) {
	m := &ai.Model{Provider: "openai", BaseURL: "https://api.openai.com", ID: "gpt-4o", MaxTokens: 16384}
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}
	body, err := buildOpenAIRequestBody(m, ctx, &ai.StreamOptions{})
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))

	assert.Equal(t, false, parsed["store"])
	assert.NotNil(t, parsed["stream_options"])
}

func TestBuildOpenAIRequestBody_NonStandardNoStore(t *testing.T) {
	m := &ai.Model{Provider: "cerebras", BaseURL: "https://api.cerebras.ai", ID: "llama-4", MaxTokens: 16384}
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}
	body, err := buildOpenAIRequestBody(m, ctx, &ai.StreamOptions{})
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))

	assert.Nil(t, parsed["store"])
}

func TestBuildOpenAIRequestBody_ReasoningEffort(t *testing.T) {
	m := &ai.Model{Provider: "openai", BaseURL: "https://api.openai.com", ID: "o1", Reasoning: true, MaxTokens: 16384}
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}
	body, err := buildOpenAIRequestBody(m, ctx, &ai.StreamOptions{ReasoningEffort: "high"})
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))

	assert.Equal(t, "high", parsed["reasoning_effort"])
}

func TestBuildOpenAIRequestBody_ZaiThinking(t *testing.T) {
	m := &ai.Model{Provider: "zai", BaseURL: "https://api.z.ai", ID: "claude-3.5-sonnet", Reasoning: true, MaxTokens: 16384}
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}

	// With reasoning: should use "enabled"
	body, err := buildOpenAIRequestBody(m, ctx, &ai.StreamOptions{ReasoningEffort: "high"})
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))
	thinking, ok := parsed["thinking"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "enabled", thinking["type"])

	// Without reasoning: should use "disabled"
	body2, err := buildOpenAIRequestBody(m, ctx, &ai.StreamOptions{})
	require.NoError(t, err)
	var parsed2 map[string]any
	require.NoError(t, json.Unmarshal(body2, &parsed2))
	thinking2, ok := parsed2["thinking"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "disabled", thinking2["type"])
}

func TestBuildOpenAIRequestBody_EmptyToolsForToolHistory(t *testing.T) {
	m := &ai.Model{Provider: "openai", BaseURL: "https://api.openai.com", ID: "gpt-4o", MaxTokens: 16384}
	ctx := ai.Context{
		Messages: []ai.Message{
			ai.NewAssistantMsg(ai.AssistantMessage{
				Role:    ai.RoleAssistant,
				Content: []ai.AssistantContent{ai.NewToolCallContent("call_1", "read", map[string]any{"path": "foo"})},
			}),
			ai.NewToolResultMsg(ai.ToolResultMessage{
				Role:       ai.RoleToolResult,
				ToolCallID: "call_1",
				Content:    []ai.ToolResultContent{{Type: "text", Text: "file contents"}},
			}),
			ai.NewUserMsg("Thanks", 0),
		},
	}
	body, err := buildOpenAIRequestBody(m, ctx, &ai.StreamOptions{})
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))

	// Should have empty tools array since no tools provided but history has tool calls
	tools, ok := parsed["tools"].([]any)
	require.True(t, ok)
	assert.Len(t, tools, 0)
}

func TestConvertOpenAITools_StrictFalse(t *testing.T) {
	compat := resolvedCompat{SupportsStrictMode: true}
	tools := []ai.Tool{{Name: "read", Description: "Read", Parameters: map[string]any{"type": "object"}}}
	result := convertOpenAITools(tools, compat)
	require.Len(t, result, 1)
	fn := result[0]["function"].(map[string]any)
	assert.Equal(t, false, fn["strict"])
}

func TestConvertOpenAITools_NoStrictField(t *testing.T) {
	compat := resolvedCompat{SupportsStrictMode: false}
	tools := []ai.Tool{{Name: "read", Description: "Read", Parameters: map[string]any{"type": "object"}}}
	result := convertOpenAITools(tools, compat)
	require.Len(t, result, 1)
	fn := result[0]["function"].(map[string]any)
	_, hasStrict := fn["strict"]
	assert.False(t, hasStrict)
}
