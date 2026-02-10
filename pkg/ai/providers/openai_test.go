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
