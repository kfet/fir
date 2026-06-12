package providers

import (
	"encoding/json"
	"strings"
	"testing"

	skipstone "github.com/kfet/skipstone"

	"github.com/kfet/fir/pkg/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func TestBuildConverseStreamInput_Basic(t *testing.T) {
	model := &ai.Model{
		ID:       "anthropic.claude-3-5-sonnet-20241022-v2:0",
		API:      ai.ApiBedrockConverseStream,
		Provider: ai.ProviderAmazonBedrock,
	}
	ctx := ai.Context{
		SystemPrompt: "You are helpful.",
		Messages:     []ai.Message{ai.NewUserMsg("Hello", 0)},
		Tools: []ai.Tool{
			{Name: "read", Description: "Read a file", Parameters: map[string]any{"type": "object"}},
		},
	}

	maxTokens := 1024
	temp := 0.7
	input, err := buildConverseStreamInput(model, ctx, &ai.StreamOptions{
		MaxTokens:   &maxTokens,
		Temperature: &temp,
		ToolChoice:  "auto",
	})
	require.NoError(t, err)
	require.NotNil(t, input)

	assert.Equal(t, "anthropic.claude-3-5-sonnet-20241022-v2:0", input.ModelID)
	assert.Len(t, input.System, 1)
	assert.Len(t, input.Messages, 1)
	require.NotNil(t, input.Inference)
	assert.Equal(t, 1024, *input.Inference.MaxTokens)
	require.Len(t, input.Tools, 1)
	require.NotNil(t, input.ToolChoice)
}

func TestBuildConverseStreamInput_ToolChoice(t *testing.T) {
	model := &ai.Model{
		ID:       "test-model",
		API:      ai.ApiBedrockConverseStream,
		Provider: ai.ProviderAmazonBedrock,
	}
	ctx := ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("Hello", 0)},
		Tools:    []ai.Tool{{Name: "read", Description: "Read", Parameters: map[string]any{"type": "object"}}},
	}

	tests := []struct {
		choice   string
		wantType skipstone.ToolChoiceType
		wantName string
	}{
		{"auto", skipstone.ToolChoiceAuto, ""},
		{"any", skipstone.ToolChoiceAny, ""},
		{"read", skipstone.ToolChoiceTool, "read"},
	}

	for _, tt := range tests {
		t.Run(tt.choice, func(t *testing.T) {
			input, err := buildConverseStreamInput(model, ctx, &ai.StreamOptions{ToolChoice: tt.choice})
			require.NoError(t, err)
			require.NotNil(t, input.ToolChoice)
			assert.Equal(t, tt.wantType, input.ToolChoice.Type)
			if tt.wantName != "" {
				assert.Equal(t, tt.wantName, input.ToolChoice.Name)
			}
		})
	}
}

func TestBuildConverseStreamInput_Thinking(t *testing.T) {
	model := &ai.Model{
		ID:        "anthropic.claude-opus-4-6-v1",
		API:       ai.ApiBedrockConverseStream,
		Provider:  ai.ProviderAmazonBedrock,
		Reasoning: true,
	}
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}

	input, err := buildConverseStreamInput(model, ctx, &ai.StreamOptions{
		Headers: map[string]string{"x-bedrock-reasoning": "high"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, input.AdditionalModelRequestFields)

	var fields map[string]any
	require.NoError(t, json.Unmarshal(input.AdditionalModelRequestFields, &fields))
	assert.Contains(t, fields, "thinking")
}

func TestBuildConverseStreamInput_ToolResults(t *testing.T) {
	model := &ai.Model{
		ID:       "test-model",
		API:      ai.ApiBedrockConverseStream,
		Provider: ai.ProviderAmazonBedrock,
	}

	msgs := []ai.Message{
		ai.NewUserMsg("Read test.txt", 0),
		ai.NewAssistantMsg(ai.AssistantMessage{Content: []ai.AssistantContent{
			ai.NewToolCallContent("tool_01", "read", map[string]any{"path": "test.txt"}),
		}}),
		ai.NewToolResultMsg(ai.ToolResultMessage{ToolCallID: "tool_01", Content: []ai.ToolResultContent{
			{Type: ai.ContentTypeText, Text: "file content"},
		}}),
	}
	ctx := ai.Context{Messages: msgs}

	input, err := buildConverseStreamInput(model, ctx, nil)
	require.NoError(t, err)
	require.Len(t, input.Messages, 3)

	lastMsg := input.Messages[2]
	assert.Equal(t, skipstone.RoleUser, lastMsg.Role)
	require.Len(t, lastMsg.Content, 1)
	require.NotNil(t, lastMsg.Content[0].ToolResult)
	tr := lastMsg.Content[0].ToolResult
	assert.Equal(t, "tool_01", tr.ToolUseID)
	assert.Equal(t, "success", tr.Status)
}

func TestBuildConverseStreamInput_CachePoints(t *testing.T) {
	model := &ai.Model{
		ID:       "anthropic.claude-4-sonnet-20250514-v1:0",
		API:      ai.ApiBedrockConverseStream,
		Provider: ai.ProviderAmazonBedrock,
		Cost:     ai.ModelCost{CacheRead: 0.3, CacheWrite: 3.75},
	}
	ctx := ai.Context{
		SystemPrompt: "System prompt",
		Messages:     []ai.Message{ai.NewUserMsg("Hello", 0)},
	}

	input, err := buildConverseStreamInput(model, ctx, nil)
	require.NoError(t, err)

	require.Len(t, input.System, 2)
	assert.NotNil(t, input.System[1].CachePoint)

	lastMsg := input.Messages[len(input.Messages)-1]
	lastContent := lastMsg.Content[len(lastMsg.Content)-1]
	assert.NotNil(t, lastContent.CachePoint)
}

func TestBuildConverseStreamInput_AssistantThinking(t *testing.T) {
	model := &ai.Model{
		ID:        "anthropic.claude-3-7-sonnet-20250219-v1:0",
		API:       ai.ApiBedrockConverseStream,
		Provider:  ai.ProviderAmazonBedrock,
		Reasoning: true,
	}

	msgs := []ai.Message{
		ai.NewUserMsg("Hello", 0),
		ai.NewAssistantMsg(ai.AssistantMessage{
			Provider: ai.ProviderAmazonBedrock,
			API:      ai.ApiBedrockConverseStream,
			Model:    "anthropic.claude-3-7-sonnet-20250219-v1:0",
			Content: []ai.AssistantContent{
				{Thinking: &ai.ThinkingContent{Type: ai.ContentTypeThinking, Thinking: "I think...", ThinkingSignature: "sig-1"}},
				ai.NewTextContent("Response"),
			},
		}),
	}
	ctx := ai.Context{Messages: msgs}

	input, err := buildConverseStreamInput(model, ctx, nil)
	require.NoError(t, err)

	require.Len(t, input.Messages, 2)
	assistantMsg := input.Messages[1]
	require.Len(t, assistantMsg.Content, 2)
	assert.NotNil(t, assistantMsg.Content[0].Reasoning)
	assert.Equal(t, "Response", assistantMsg.Content[1].Text)
}

func TestConvertBedrockToolConfig(t *testing.T) {
	tools := []ai.Tool{
		{
			Name:        "bash",
			Description: "Run a command",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}},
		},
	}

	out, choice := convertBedrockToolConfig(tools, "auto")
	require.Len(t, out, 1)
	assert.Equal(t, "bash", out[0].Name)
	assert.Equal(t, "Run a command", out[0].Description)
	assert.NotEmpty(t, out[0].InputSchema)

	require.NotNil(t, choice)
	assert.Equal(t, skipstone.ToolChoiceAuto, choice.Type)
}

func TestBedrockToolSchema(t *testing.T) {
	// Empty / non-object input is normalised.
	assert.Equal(t, "object", bedrockToolSchema(nil)["type"])
	assert.Equal(t, "object", bedrockToolSchema("string")["type"])

	// Existing object preserved with defaults filled in.
	out := bedrockToolSchema(map[string]any{"properties": map[string]any{"x": "y"}})
	assert.Equal(t, "object", out["type"])
	assert.Contains(t, out, "properties")
}

func TestBedrockImageFormat(t *testing.T) {
	assert.Equal(t, "png", bedrockImageFormat("image/png"))
	assert.Equal(t, "gif", bedrockImageFormat("image/gif"))
	assert.Equal(t, "webp", bedrockImageFormat("image/webp"))
	assert.Equal(t, "jpeg", bedrockImageFormat("image/jpeg"))
	assert.Equal(t, "jpeg", bedrockImageFormat("image/unknown"))
}

func TestSupportsBedrockPromptCaching(t *testing.T) {
	assert.True(t, supportsBedrockPromptCaching(&ai.Model{ID: "anthropic.claude-4-sonnet", Cost: ai.ModelCost{CacheRead: 0.3}}))
	assert.True(t, supportsBedrockPromptCaching(&ai.Model{ID: "anthropic.claude-3-7-sonnet-20250219-v1:0"}))
	assert.True(t, supportsBedrockPromptCaching(&ai.Model{ID: "anthropic.claude-3-5-haiku"}))
	assert.False(t, supportsBedrockPromptCaching(&ai.Model{ID: "some-other-model"}))
}

func TestSupportsBedrockThinkingSignature(t *testing.T) {
	assert.True(t, supportsBedrockThinkingSignature(&ai.Model{ID: "anthropic.claude-3-5-sonnet"}))
	assert.True(t, supportsBedrockThinkingSignature(&ai.Model{ID: "anthropic/claude-4"}))
	assert.False(t, supportsBedrockThinkingSignature(&ai.Model{ID: "mistral.mistral-large"}))
}

func TestSupportsBedrockAdaptiveThinking(t *testing.T) {
	assert.True(t, supportsBedrockAdaptiveThinking("anthropic.claude-opus-4-6-v1", ""))
	assert.True(t, supportsBedrockAdaptiveThinking("anthropic.claude-opus-4.6-v1", ""))
	assert.True(t, supportsBedrockAdaptiveThinking("anthropic.claude-opus-4-8", ""))
	assert.False(t, supportsBedrockAdaptiveThinking("anthropic.claude-3-7-sonnet", ""))
	assert.True(t, supportsBedrockAdaptiveThinking("arn:aws:bedrock:us-east-1:123:application-inference-profile/abcd", "Claude Opus 4.7"))
}

func TestBuildBedrockAdditionalFields_Adaptive(t *testing.T) {
	fields := buildBedrockAdditionalFields(&ai.Model{ID: "anthropic.claude-opus-4-6-v1"}, "high", &ai.StreamOptions{
		Headers: map[string]string{"x-bedrock-reasoning": "high"},
	})
	require.NotNil(t, fields)
	assert.Contains(t, fields, "thinking")
	assert.Contains(t, fields, "output_config")
}

func TestBuildBedrockAdditionalFields_BudgetBased(t *testing.T) {
	fields := buildBedrockAdditionalFields(&ai.Model{ID: "anthropic.claude-3-7-sonnet"}, "medium", &ai.StreamOptions{
		Headers: map[string]string{
			"x-bedrock-reasoning":            "medium",
			"x-bedrock-thinking-budget":      "4096",
			"x-bedrock-interleaved-thinking": "true",
		},
	})
	require.NotNil(t, fields)
	thinking, ok := fields["thinking"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "enabled", thinking["type"])
	assert.Equal(t, 4096, thinking["budget_tokens"])
	assert.Contains(t, fields, "anthropic_beta")
}

func TestNewBedrockClient_SkipAuth(t *testing.T) {
	t.Setenv("AWS_BEDROCK_SKIP_AUTH", "1")
	t.Setenv("AWS_REGION", "us-west-2")

	model := &ai.Model{
		ID:       "test-model",
		API:      ai.ApiBedrockConverseStream,
		Provider: ai.ProviderAmazonBedrock,
		BaseURL:  "https://my-proxy.example.com",
	}

	client, err := newBedrockClient(model, nil)
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestNewBedrockClient_DefaultRegion(t *testing.T) {
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	t.Setenv("AWS_BEDROCK_SKIP_AUTH", "1") // avoid touching ~/.aws

	model := &ai.Model{ID: "test-model", API: ai.ApiBedrockConverseStream, Provider: ai.ProviderAmazonBedrock}
	client, err := newBedrockClient(model, nil)
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestConvertBedrockMessages_EmptyText(t *testing.T) {
	model := &ai.Model{ID: "test-model", API: ai.ApiBedrockConverseStream, Provider: ai.ProviderAmazonBedrock}
	msgs := []ai.Message{ai.NewUserMsg("  ", 0)}
	result := convertBedrockMessages(msgs, model, false, ai.CacheNone)
	assert.Empty(t, result)
}

func TestConvertBedrockMessages_MultipartContent(t *testing.T) {
	model := &ai.Model{
		ID:       "test-model",
		API:      ai.ApiBedrockConverseStream,
		Provider: ai.ProviderAmazonBedrock,
		Input:    []ai.InputModality{ai.InputText, ai.InputImage},
	}
	msgs := []ai.Message{
		ai.NewUserMsg([]any{
			map[string]any{"type": "text", "text": "Look at this"},
			map[string]any{"type": "image", "data": "aGVsbG8=", "mimeType": "image/png"},
		}, 0),
	}
	result := convertBedrockMessages(msgs, model, false, ai.CacheNone)
	require.Len(t, result, 1)
	require.Len(t, result[0].Content, 2)

	assert.Equal(t, "Look at this", result[0].Content[0].Text)
	assert.NotNil(t, result[0].Content[1].Image)
}

func TestConvertBedrockMessages_ToolResultError(t *testing.T) {
	model := &ai.Model{ID: "test-model", API: ai.ApiBedrockConverseStream, Provider: ai.ProviderAmazonBedrock}
	msgs := []ai.Message{
		ai.NewUserMsg("Do something", 0),
		ai.NewAssistantMsg(ai.AssistantMessage{Content: []ai.AssistantContent{
			ai.NewToolCallContent("t1", "bash", map[string]any{"command": "ls"}),
		}}),
		ai.NewToolResultMsg(ai.ToolResultMessage{ToolCallID: "t1", Content: []ai.ToolResultContent{
			{Type: ai.ContentTypeText, Text: "command failed"},
		}, IsError: true}),
	}
	result := convertBedrockMessages(msgs, model, false, ai.CacheNone)
	require.Len(t, result, 3)

	require.NotNil(t, result[2].Content[0].ToolResult)
	assert.Equal(t, "error", result[2].Content[0].ToolResult.Status)
}

func TestBuildConverseStreamInput_NoOptions(t *testing.T) {
	model := &ai.Model{ID: "test-model", API: ai.ApiBedrockConverseStream, Provider: ai.ProviderAmazonBedrock}
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}

	input, err := buildConverseStreamInput(model, ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, input)
	assert.Nil(t, input.Inference)
	assert.Empty(t, input.Tools)
	assert.Nil(t, input.ToolChoice)
}

func TestBedrockNormalizeID(t *testing.T) {
	normalize := func(id string) string {
		var b strings.Builder
		b.Grow(min(len(id), 64))
		for _, c := range id {
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
				b.WriteRune(c)
			} else {
				b.WriteByte('_')
			}
			if b.Len() >= 64 {
				break
			}
		}
		return b.String()
	}

	assert.Equal(t, "tool_call_123", normalize("tool_call_123"))
	assert.Equal(t, "tool_call_123", normalize("tool.call.123"))
	assert.Equal(t, "abc-def_ghi", normalize("abc-def_ghi"))
}
