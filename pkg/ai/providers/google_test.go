package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kfet/pi-go/pkg/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func googleTestModel(serverURL string) *ai.Model {
	return &ai.Model{
		ID:            "gemini-2.5-pro",
		Name:          "Gemini 2.5 Pro",
		Api:           ai.ApiGoogleGenerativeAI,
		Provider:      ai.ProviderGoogle,
		BaseURL:       serverURL,
		Reasoning:     false,
		Input:         []ai.InputModality{ai.InputText, ai.InputImage},
		Cost:          ai.ModelCost{Input: 1.25, Output: 10.0, CacheRead: 0.31, CacheWrite: 0},
		ContextWindow: 1048576,
		MaxTokens:     65536,
	}
}

func TestStreamGoogle_SimpleResponse(t *testing.T) {
	srv := mockSSEServer(t, "google_simple_response.sse")
	defer srv.Close()

	model := googleTestModel(srv.URL)
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}

	stream := StreamGoogle(context.Background(), model, ctx, &ai.StreamOptions{ApiKey: "test-key"})
	events, result := stream.Collect()
	require.NotNil(t, result)

	assert.Equal(t, ai.StopReasonStop, result.StopReason)

	// Check text content
	require.True(t, len(result.Content) > 0)
	require.True(t, result.Content[0].IsText())
	assert.Equal(t, "Hello", result.Content[0].Text.Text)

	// Check usage
	assert.Equal(t, 25, result.Usage.Input)
	assert.Equal(t, 12, result.Usage.Output)

	// Check events
	hasStart := false
	hasDone := false
	for _, e := range events {
		switch e.Type {
		case ai.EventStart:
			hasStart = true
		case ai.EventDone:
			hasDone = true
		}
	}
	assert.True(t, hasStart)
	assert.True(t, hasDone)
}

func TestStreamGoogle_NoApiKey(t *testing.T) {
	model := googleTestModel("http://localhost:0")
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}

	t.Setenv("GEMINI_API_KEY", "")

	stream := StreamGoogle(context.Background(), model, ctx, &ai.StreamOptions{})
	result := stream.Result()
	require.NotNil(t, result)
	assert.Equal(t, ai.StopReasonError, result.StopReason)
	assert.Contains(t, result.ErrorMessage, "no API key")
}

func TestMapGoogleStopReason(t *testing.T) {
	assert.Equal(t, ai.StopReasonStop, mapGoogleStopReason("STOP"))
	assert.Equal(t, ai.StopReasonLength, mapGoogleStopReason("MAX_TOKENS"))
	assert.Equal(t, ai.StopReasonError, mapGoogleStopReason("SAFETY"))
	assert.Equal(t, ai.StopReasonError, mapGoogleStopReason("RECITATION"))
	assert.Equal(t, ai.StopReasonStop, mapGoogleStopReason("UNKNOWN"))
}

func TestRegisterGoogle(t *testing.T) {
	reg := ai.NewRegistry()
	RegisterGoogle(reg)

	p := reg.GetApiProvider(ai.ApiGoogleGenerativeAI)
	require.NotNil(t, p)
	assert.Equal(t, ai.ApiGoogleGenerativeAI, p.Api)
}

func TestStreamGoogle_ToolCall(t *testing.T) {
	srv := mockSSEServer(t, "google_tool_call.sse")
	defer srv.Close()

	model := googleTestModel(srv.URL)
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Read the file", 0)}}

	stream := StreamGoogle(context.Background(), model, ctx, &ai.StreamOptions{ApiKey: "test-key"})
	events, result := stream.Collect()
	require.NotNil(t, result)

	assert.Equal(t, ai.StopReasonStop, result.StopReason)

	// Check tool call content
	var toolCall *ai.ToolCall
	for _, c := range result.Content {
		if c.IsToolCall() {
			toolCall = c.ToolCall
			break
		}
	}
	require.NotNil(t, toolCall, "expected a tool call in response")
	assert.Equal(t, "read_file", toolCall.Name)
	assert.Equal(t, "/tmp/test.txt", toolCall.Arguments["path"])

	// Check usage
	assert.Equal(t, 30, result.Usage.Input)
	assert.Equal(t, 15, result.Usage.Output)

	// Check events include tool call start/end
	hasToolStart := false
	hasToolEnd := false
	for _, e := range events {
		switch e.Type {
		case ai.EventToolcallStart:
			hasToolStart = true
		case ai.EventToolcallEnd:
			hasToolEnd = true
		}
	}
	assert.True(t, hasToolStart, "expected toolcall_start event")
	assert.True(t, hasToolEnd, "expected toolcall_end event")
}

func TestStreamGoogle_Thinking(t *testing.T) {
	srv := mockSSEServer(t, "google_thinking.sse")
	defer srv.Close()

	model := googleTestModel(srv.URL)
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("What is the meaning of life?", 0)}}

	stream := StreamGoogle(context.Background(), model, ctx, &ai.StreamOptions{ApiKey: "test-key"})
	events, result := stream.Collect()
	require.NotNil(t, result)

	assert.Equal(t, ai.StopReasonStop, result.StopReason)

	// Should have thinking content and text content
	var hasThinking, hasText bool
	for _, c := range result.Content {
		if c.IsThinking() {
			hasThinking = true
			assert.Equal(t, "Let me think about this...", c.Thinking.Thinking)
		}
		if c.IsText() {
			hasText = true
			assert.Equal(t, "The answer is 42.", c.Text.Text)
		}
	}
	assert.True(t, hasThinking, "expected thinking content")
	assert.True(t, hasText, "expected text content")

	// Check thinking events
	hasThinkingStart := false
	hasThinkingDelta := false
	hasThinkingEnd := false
	for _, e := range events {
		switch e.Type {
		case ai.EventThinkingStart:
			hasThinkingStart = true
		case ai.EventThinkingDelta:
			hasThinkingDelta = true
		case ai.EventThinkingEnd:
			hasThinkingEnd = true
		}
	}
	assert.True(t, hasThinkingStart, "expected thinking_start event")
	assert.True(t, hasThinkingDelta, "expected thinking_delta event")
	assert.True(t, hasThinkingEnd, "expected thinking_end event")
}

func TestStreamGoogle_HTTPError(t *testing.T) {
	srv := mockJSONServer(t, 500, []byte(`{"error":"internal server error"}`))
	defer srv.Close()

	model := googleTestModel(srv.URL)
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}

	stream := StreamGoogle(context.Background(), model, ctx, &ai.StreamOptions{ApiKey: "test-key"})
	result := stream.Result()
	require.NotNil(t, result)
	assert.Equal(t, ai.StopReasonError, result.StopReason)
	assert.Contains(t, result.ErrorMessage, "500")
}

func TestStreamGoogle_ContextCancelled(t *testing.T) {
	// Create a server that hangs
	srv := mockSSEServerFunc(t, func(w http.ResponseWriter, r *http.Request) {
		// Don't respond — just wait for context cancellation
		<-r.Context().Done()
	})
	defer srv.Close()

	model := googleTestModel(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())

	stream := StreamGoogle(ctx, model, ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}, &ai.StreamOptions{ApiKey: "test-key"})

	// Cancel the context immediately
	cancel()

	result := stream.Result()
	require.NotNil(t, result)
	assert.Equal(t, ai.StopReasonAborted, result.StopReason)
}

func TestStreamSimpleGoogle_NoApiKey(t *testing.T) {
	model := googleTestModel("http://localhost:0")
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}

	t.Setenv("GEMINI_API_KEY", "")

	stream := StreamSimpleGoogle(context.Background(), model, ctx, &ai.SimpleStreamOptions{})
	result := stream.Result()
	require.NotNil(t, result)
	assert.Equal(t, ai.StopReasonError, result.StopReason)
	assert.Contains(t, result.ErrorMessage, "no API key")
}

func TestStreamSimpleGoogle_WithApiKey(t *testing.T) {
	srv := mockSSEServer(t, "google_simple_response.sse")
	defer srv.Close()

	model := googleTestModel(srv.URL)
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}

	stream := StreamSimpleGoogle(context.Background(), model, ctx, &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{ApiKey: "test-key"},
	})
	result := stream.Result()
	require.NotNil(t, result)
	assert.Equal(t, ai.StopReasonStop, result.StopReason)
}

func TestBuildGoogleRequestBody_BasicMessage(t *testing.T) {
	model := googleTestModel("http://localhost")
	ctx := ai.Context{
		SystemPrompt: "You are helpful.",
		Messages:     []ai.Message{ai.NewUserMsg("Hello", 0)},
	}

	body, err := buildGoogleRequestBody(model, ctx, nil)
	require.NoError(t, err)

	var parsed map[string]any
	err = json.Unmarshal(body, &parsed)
	require.NoError(t, err)

	// Check system instruction
	sysInstr, ok := parsed["systemInstruction"].(map[string]any)
	require.True(t, ok, "expected systemInstruction")
	parts := sysInstr["parts"].([]any)
	require.Len(t, parts, 1)
	assert.Equal(t, "You are helpful.", parts[0].(map[string]any)["text"])

	// Check contents
	contents, ok := parsed["contents"].([]any)
	require.True(t, ok, "expected contents")
	require.Len(t, contents, 1)

	// Check generation config
	genConfig := parsed["generationConfig"].(map[string]any)
	assert.Equal(t, float64(32000), genConfig["maxOutputTokens"]) // capped at 32000
}

func TestBuildGoogleRequestBody_WithTools(t *testing.T) {
	model := googleTestModel("http://localhost")
	ctx := ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("Read a file", 0)},
		Tools: []ai.Tool{
			{
				Name:        "read_file",
				Description: "Read a file",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
			},
		},
	}

	body, err := buildGoogleRequestBody(model, ctx, nil)
	require.NoError(t, err)

	var parsed map[string]any
	err = json.Unmarshal(body, &parsed)
	require.NoError(t, err)

	tools, ok := parsed["tools"].([]any)
	require.True(t, ok, "expected tools")
	require.Len(t, tools, 1)

	toolObj := tools[0].(map[string]any)
	funcDecls := toolObj["functionDeclarations"].([]any)
	require.Len(t, funcDecls, 1)
	assert.Equal(t, "read_file", funcDecls[0].(map[string]any)["name"])
}

func TestBuildGoogleRequestBody_WithMaxTokensOverride(t *testing.T) {
	model := googleTestModel("http://localhost")
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}

	maxTokens := 1000
	opts := &ai.StreamOptions{MaxTokens: &maxTokens}

	body, err := buildGoogleRequestBody(model, ctx, opts)
	require.NoError(t, err)

	var parsed map[string]any
	err = json.Unmarshal(body, &parsed)
	require.NoError(t, err)

	genConfig := parsed["generationConfig"].(map[string]any)
	assert.Equal(t, float64(1000), genConfig["maxOutputTokens"])
}

func TestBuildGoogleRequestBody_WithTemperature(t *testing.T) {
	model := googleTestModel("http://localhost")
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}

	temp := 0.7
	opts := &ai.StreamOptions{Temperature: &temp}

	body, err := buildGoogleRequestBody(model, ctx, opts)
	require.NoError(t, err)

	var parsed map[string]any
	err = json.Unmarshal(body, &parsed)
	require.NoError(t, err)

	genConfig := parsed["generationConfig"].(map[string]any)
	assert.Equal(t, 0.7, genConfig["temperature"])
}

func TestBuildGoogleRequestBody_NoSystemPrompt(t *testing.T) {
	model := googleTestModel("http://localhost")
	ctx := ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("Hello", 0)},
	}

	body, err := buildGoogleRequestBody(model, ctx, nil)
	require.NoError(t, err)

	var parsed map[string]any
	err = json.Unmarshal(body, &parsed)
	require.NoError(t, err)

	_, hasSysInstr := parsed["systemInstruction"]
	assert.False(t, hasSysInstr, "should not have systemInstruction when no system prompt")
}

func TestBuildGoogleRequestBody_AssistantAndToolResult(t *testing.T) {
	model := googleTestModel("http://localhost")

	assistantMsg := ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{
			ai.NewToolCallContent("call_1", "read_file", map[string]any{"path": "/tmp/test.txt"}),
		},
	})

	toolResultMsg := ai.NewToolResultMsg(ai.ToolResultMessage{
		ToolCallID: "call_1",
		ToolName:   "read_file",
		Content:    []ai.ToolResultContent{{Text: "file contents here"}},
	})

	ctx := ai.Context{
		Messages: []ai.Message{
			ai.NewUserMsg("Read the file", 0),
			assistantMsg,
			toolResultMsg,
		},
	}

	body, err := buildGoogleRequestBody(model, ctx, nil)
	require.NoError(t, err)

	var parsed map[string]any
	err = json.Unmarshal(body, &parsed)
	require.NoError(t, err)

	contents := parsed["contents"].([]any)
	// Should have: user message, model (assistant) with function call, user (tool result)
	require.Len(t, contents, 3)

	// Check assistant message has functionCall
	modelMsg := contents[1].(map[string]any)
	assert.Equal(t, "model", modelMsg["role"])
	parts := modelMsg["parts"].([]any)
	require.Len(t, parts, 1)
	fc := parts[0].(map[string]any)["functionCall"].(map[string]any)
	assert.Equal(t, "read_file", fc["name"])

	// Check tool result
	toolMsg := contents[2].(map[string]any)
	assert.Equal(t, "user", toolMsg["role"])
	toolParts := toolMsg["parts"].([]any)
	require.Len(t, toolParts, 1)
	fr := toolParts[0].(map[string]any)["functionResponse"].(map[string]any)
	assert.Equal(t, "read_file", fr["name"])
}

func TestStreamGoogle_RequestHeaders(t *testing.T) {
	// Verify the API key is sent via x-goog-api-key header and not as URL parameter
	var capturedHeaders http.Header
	srv := mockSSEServerFunc(t, func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		data := loadFixture(t, "google_simple_response.sse")
		w.Write(data)
	})
	defer srv.Close()

	model := googleTestModel(srv.URL)
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}

	stream := StreamGoogle(context.Background(), model, ctx, &ai.StreamOptions{ApiKey: "test-api-key-123"})
	_ = stream.Result()

	assert.Equal(t, "test-api-key-123", capturedHeaders.Get("X-Goog-Api-Key"))
	assert.Equal(t, "application/json", capturedHeaders.Get("Content-Type"))
}

func TestParseGoogleResponse_TextMerging(t *testing.T) {
	// Two consecutive text parts should be merged into one text block
	input := `data: {"candidates":[{"content":{"parts":[{"text":"Hello "}],"role":"model"},"index":0}]}

data: {"candidates":[{"content":{"parts":[{"text":"world!"}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}
`
	model := googleTestModel("http://localhost")
	output := &ai.AssistantMessage{
		Role:    ai.RoleAssistant,
		Content: []ai.AssistantContent{},
		Usage:   ai.ZeroUsage(),
	}
	stream := ai.NewAssistantMessageEventStream()
	go func() {
		_ = parseGoogleResponse(strings.NewReader(input), model, output, stream)
		stream.End(nil)
	}()

	// Drain events
	for range stream.Events {
	}

	// Text parts from two chunks should be merged into one content block
	require.Len(t, output.Content, 1, "expected merged text blocks")
	assert.True(t, output.Content[0].IsText())
	assert.Equal(t, "Hello world!", output.Content[0].Text.Text)

	// Usage should be from last chunk
	assert.Equal(t, 10, output.Usage.Input)
	assert.Equal(t, 5, output.Usage.Output)
}

func TestParseGoogleResponse_MixedContent(t *testing.T) {
	// Thinking followed by text followed by tool call
	input := `data: {"candidates":[{"content":{"parts":[{"text":"thinking...","thought":true}],"role":"model"},"index":0}]}

data: {"candidates":[{"content":{"parts":[{"text":"Here is what I found."}],"role":"model"},"index":0}]}

data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"bash","args":{"command":"ls"}}}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":50,"candidatesTokenCount":30,"totalTokenCount":80}}
`
	model := googleTestModel("http://localhost")
	output := &ai.AssistantMessage{
		Role:    ai.RoleAssistant,
		Content: []ai.AssistantContent{},
		Usage:   ai.ZeroUsage(),
	}
	stream := ai.NewAssistantMessageEventStream()
	go func() {
		_ = parseGoogleResponse(strings.NewReader(input), model, output, stream)
		stream.End(nil)
	}()
	for range stream.Events {
	}

	// Should have 3 content blocks: thinking, text, tool call
	require.Len(t, output.Content, 3)
	assert.True(t, output.Content[0].IsThinking())
	assert.Equal(t, "thinking...", output.Content[0].Thinking.Thinking)
	assert.True(t, output.Content[1].IsText())
	assert.Equal(t, "Here is what I found.", output.Content[1].Text.Text)
	assert.True(t, output.Content[2].IsToolCall())
	assert.Equal(t, "bash", output.Content[2].ToolCall.Name)
}

func TestParseGoogleResponse_EmptyDataLines(t *testing.T) {
	// Lines without "data: " prefix should be skipped
	input := `event: ping

data: {"candidates":[{"content":{"parts":[{"text":"ok"}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2,"totalTokenCount":7}}

`
	model := googleTestModel("http://localhost")
	output := &ai.AssistantMessage{
		Role:    ai.RoleAssistant,
		Content: []ai.AssistantContent{},
		Usage:   ai.ZeroUsage(),
	}
	stream := ai.NewAssistantMessageEventStream()
	go func() {
		_ = parseGoogleResponse(strings.NewReader(input), model, output, stream)
		stream.End(nil)
	}()
	for range stream.Events {
	}

	require.Len(t, output.Content, 1)
	assert.Equal(t, "ok", output.Content[0].Text.Text)
}

func TestMapGoogleStopReason_AllValues(t *testing.T) {
	tests := []struct {
		input    string
		expected ai.StopReason
	}{
		{"STOP", ai.StopReasonStop},
		{"MAX_TOKENS", ai.StopReasonLength},
		{"SAFETY", ai.StopReasonError},
		{"RECITATION", ai.StopReasonError},
		{"BLOCKLIST", ai.StopReasonError},
		{"PROHIBITED_CONTENT", ai.StopReasonError},
		{"SPII", ai.StopReasonError},
		{"UNKNOWN_REASON", ai.StopReasonStop},
		{"", ai.StopReasonStop},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, mapGoogleStopReason(tt.input))
		})
	}
}
