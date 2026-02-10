package providers

import (
	"context"
	"testing"

	"github.com/kfet/pi-go/pkg/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func bedrockTestModel(serverURL string) *ai.Model {
	return &ai.Model{
		ID:            "anthropic.claude-3-5-sonnet-20241022-v2:0",
		Name:          "Claude 3.5 Sonnet (Bedrock)",
		Api:           ai.ApiBedrockConverseStream,
		Provider:      ai.ProviderAmazonBedrock,
		BaseURL:       serverURL,
		Reasoning:     false,
		Input:         []ai.InputModality{ai.InputText, ai.InputImage},
		Cost:          ai.ModelCost{Input: 3.0, Output: 15.0, CacheRead: 0.3},
		ContextWindow: 200000,
		MaxTokens:     8192,
	}
}

func TestStreamBedrock_SimpleResponse(t *testing.T) {
	srv := mockSSEServer(t, "bedrock_simple_response.sse")
	defer srv.Close()

	model := bedrockTestModel(srv.URL)
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}

	stream := StreamBedrock(context.Background(), model, ctx, &ai.StreamOptions{ApiKey: "test-key"})
	events, result := stream.Collect()
	require.NotNil(t, result)

	assert.Equal(t, ai.StopReasonStop, result.StopReason)

	// Check text content
	require.True(t, len(result.Content) > 0)
	require.True(t, result.Content[0].IsText())
	assert.Equal(t, "Hello there!", result.Content[0].Text.Text)

	// Check usage
	assert.Equal(t, 20, result.Usage.Input)
	assert.Equal(t, 8, result.Usage.Output)
	assert.Equal(t, 28, result.Usage.TotalTokens)

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

func TestStreamBedrock_ToolCall(t *testing.T) {
	srv := mockSSEServer(t, "bedrock_tool_call.sse")
	defer srv.Close()

	model := bedrockTestModel(srv.URL)
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Read test.txt", 0)}}

	stream := StreamBedrock(context.Background(), model, ctx, &ai.StreamOptions{ApiKey: "test-key"})
	_, result := stream.Collect()
	require.NotNil(t, result)

	assert.Equal(t, ai.StopReasonToolUse, result.StopReason)
	require.Len(t, result.Content, 2)

	// First: text
	require.True(t, result.Content[0].IsText())
	assert.Equal(t, "Let me read that.", result.Content[0].Text.Text)

	// Second: tool call
	require.True(t, result.Content[1].IsToolCall())
	tc := result.Content[1].ToolCall
	assert.Equal(t, "tool_01", tc.ID)
	assert.Equal(t, "read", tc.Name)
	assert.Equal(t, "test.txt", tc.Arguments["path"])
}

func TestStreamBedrock_Thinking(t *testing.T) {
	srv := mockSSEServer(t, "bedrock_thinking.sse")
	defer srv.Close()

	model := bedrockTestModel(srv.URL)
	model.Reasoning = true
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("What is 6*7?", 0)}}

	stream := StreamBedrock(context.Background(), model, ctx, &ai.StreamOptions{ApiKey: "test-key"})
	_, result := stream.Collect()
	require.NotNil(t, result)

	assert.Equal(t, ai.StopReasonStop, result.StopReason)
	require.Len(t, result.Content, 2)

	// Thinking block
	require.True(t, result.Content[0].IsThinking())
	assert.Equal(t, "Let me think...", result.Content[0].Thinking.Thinking)
	assert.Equal(t, "sig_bedrock", result.Content[0].Thinking.ThinkingSignature)

	// Text block
	require.True(t, result.Content[1].IsText())
	assert.Equal(t, "The answer is 42.", result.Content[1].Text.Text)
}

func TestStreamBedrock_Error(t *testing.T) {
	srv := mockSSEServer(t, "bedrock_error.sse")
	defer srv.Close()

	model := bedrockTestModel(srv.URL)
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}

	stream := StreamBedrock(context.Background(), model, ctx, &ai.StreamOptions{ApiKey: "test-key"})
	result := stream.Result()
	require.NotNil(t, result)
	assert.Equal(t, ai.StopReasonError, result.StopReason)
	assert.Contains(t, result.ErrorMessage, "Rate exceeded")
}

func TestStreamBedrock_NoBaseURL(t *testing.T) {
	model := &ai.Model{
		ID:       "test-model",
		Api:      ai.ApiBedrockConverseStream,
		Provider: ai.ProviderAmazonBedrock,
		// No BaseURL
	}
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}

	stream := StreamBedrock(context.Background(), model, ctx, &ai.StreamOptions{ApiKey: "test"})
	result := stream.Result()
	require.NotNil(t, result)
	assert.Equal(t, ai.StopReasonError, result.StopReason)
	assert.Contains(t, result.ErrorMessage, "baseURL")
}

func TestMapBedrockStopReason(t *testing.T) {
	assert.Equal(t, ai.StopReasonStop, mapBedrockStopReason("end_turn"))
	assert.Equal(t, ai.StopReasonStop, mapBedrockStopReason("stop_sequence"))
	assert.Equal(t, ai.StopReasonLength, mapBedrockStopReason("max_tokens"))
	assert.Equal(t, ai.StopReasonLength, mapBedrockStopReason("model_context_window_exceeded"))
	assert.Equal(t, ai.StopReasonToolUse, mapBedrockStopReason("tool_use"))
	assert.Equal(t, ai.StopReasonError, mapBedrockStopReason("unknown"))
}

func TestRegisterBedrock(t *testing.T) {
	reg := ai.NewRegistry()
	RegisterBedrock(reg)

	p := reg.GetApiProvider(ai.ApiBedrockConverseStream)
	require.NotNil(t, p)
	assert.Equal(t, ai.ApiBedrockConverseStream, p.Api)
}
