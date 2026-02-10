package providers

import (
	"context"
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
