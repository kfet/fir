package providers

import (
	"context"
	"testing"

	"github.com/kfet/pi-go/pkg/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openaiResponsesTestModel(serverURL string) *ai.Model {
	return &ai.Model{
		ID:            "gpt-4o",
		Name:          "GPT-4o",
		Api:           ai.ApiOpenAIResponses,
		Provider:      ai.ProviderOpenAI,
		BaseURL:       serverURL,
		Reasoning:     false,
		Input:         []ai.InputModality{ai.InputText, ai.InputImage},
		Cost:          ai.ModelCost{Input: 2.5, Output: 10.0, CacheRead: 1.25},
		ContextWindow: 128000,
		MaxTokens:     16384,
	}
}

func TestStreamOpenAIResponses_SimpleResponse(t *testing.T) {
	srv := mockSSEServer(t, "openai_responses_simple.sse")
	defer srv.Close()

	model := openaiResponsesTestModel(srv.URL)
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}

	stream := StreamOpenAIResponses(context.Background(), model, ctx, &ai.StreamOptions{ApiKey: "sk-test"})
	events, result := stream.Collect()
	require.NotNil(t, result)

	assert.Equal(t, ai.StopReasonStop, result.StopReason)

	// Check text content
	require.True(t, len(result.Content) > 0)
	require.True(t, result.Content[0].IsText())
	assert.Equal(t, "Hello, world!", result.Content[0].Text.Text)
	assert.Equal(t, "msg_01", result.Content[0].Text.TextSignature)

	// Check usage — 20 input - 10 cached = 10 non-cached input
	assert.Equal(t, 10, result.Usage.Input)
	assert.Equal(t, 5, result.Usage.Output)
	assert.Equal(t, 10, result.Usage.CacheRead)

	// Check events
	hasStart := false
	hasTextDelta := false
	hasTextEnd := false
	hasDone := false
	for _, e := range events {
		switch e.Type {
		case ai.EventStart:
			hasStart = true
		case ai.EventTextDelta:
			hasTextDelta = true
		case ai.EventTextEnd:
			hasTextEnd = true
		case ai.EventDone:
			hasDone = true
		}
	}
	assert.True(t, hasStart)
	assert.True(t, hasTextDelta)
	assert.True(t, hasTextEnd)
	assert.True(t, hasDone)
}

func TestStreamOpenAIResponses_ToolCall(t *testing.T) {
	srv := mockSSEServer(t, "openai_responses_tool_call.sse")
	defer srv.Close()

	model := openaiResponsesTestModel(srv.URL)
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Read test.txt", 0)}}

	stream := StreamOpenAIResponses(context.Background(), model, ctx, &ai.StreamOptions{ApiKey: "sk-test"})
	_, result := stream.Collect()
	require.NotNil(t, result)

	assert.Equal(t, ai.StopReasonToolUse, result.StopReason)
	require.Len(t, result.Content, 2)

	// First content: text
	require.True(t, result.Content[0].IsText())
	assert.Equal(t, "Let me read that.", result.Content[0].Text.Text)

	// Second content: tool call
	require.True(t, result.Content[1].IsToolCall())
	tc := result.Content[1].ToolCall
	assert.Equal(t, "call_123|fc_01", tc.ID)
	assert.Equal(t, "read", tc.Name)
	assert.Equal(t, "test.txt", tc.Arguments["path"])
}

func TestStreamOpenAIResponses_Error(t *testing.T) {
	srv := mockSSEServer(t, "openai_responses_error.sse")
	defer srv.Close()

	model := openaiResponsesTestModel(srv.URL)
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}

	stream := StreamOpenAIResponses(context.Background(), model, ctx, &ai.StreamOptions{ApiKey: "sk-test"})
	result := stream.Result()
	require.NotNil(t, result)
	assert.Equal(t, ai.StopReasonError, result.StopReason)
	assert.Contains(t, result.ErrorMessage, "rate_limit_exceeded")
}

func TestStreamOpenAIResponses_NoApiKey(t *testing.T) {
	model := openaiResponsesTestModel("http://localhost:0")
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}

	t.Setenv("OPENAI_API_KEY", "")

	stream := StreamOpenAIResponses(context.Background(), model, ctx, &ai.StreamOptions{})
	result := stream.Result()
	require.NotNil(t, result)
	assert.Equal(t, ai.StopReasonError, result.StopReason)
	assert.Contains(t, result.ErrorMessage, "no API key")
}

func TestMapOpenAIResponsesStatus(t *testing.T) {
	assert.Equal(t, ai.StopReasonStop, mapOpenAIResponsesStatus("completed"))
	assert.Equal(t, ai.StopReasonLength, mapOpenAIResponsesStatus("incomplete"))
	assert.Equal(t, ai.StopReasonError, mapOpenAIResponsesStatus("failed"))
	assert.Equal(t, ai.StopReasonError, mapOpenAIResponsesStatus("cancelled"))
	assert.Equal(t, ai.StopReasonStop, mapOpenAIResponsesStatus("in_progress"))
}

func TestRegisterOpenAIResponses(t *testing.T) {
	reg := ai.NewRegistry()
	RegisterOpenAIResponses(reg)

	p := reg.GetApiProvider(ai.ApiOpenAIResponses)
	require.NotNil(t, p)
	assert.Equal(t, ai.ApiOpenAIResponses, p.Api)
}
