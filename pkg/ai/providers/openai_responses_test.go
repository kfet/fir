package providers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/ai"
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
	assert.Equal(t, `{"id":"msg_01","v":1}`, result.Content[0].Text.TextSignature)

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

func TestStreamOpenAIResponses_ShellCall(t *testing.T) {
	srv := mockSSEServer(t, "openai_responses_shell_call.sse")
	defer srv.Close()

	model := openaiResponsesTestModel(srv.URL)
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Run echo hello world", 0)}}

	stream := StreamOpenAIResponses(context.Background(), model, ctx, &ai.StreamOptions{ApiKey: "sk-test"})
	events, result := stream.Collect()
	require.NotNil(t, result)

	assert.Equal(t, ai.StopReasonStop, result.StopReason)

	// Should have 3 content blocks: shell_call text, shell_call_output text, message text
	require.Equal(t, 3, len(result.Content))

	// Block 0: shell_call formatted as text
	require.True(t, result.Content[0].IsText())
	assert.Contains(t, result.Content[0].Text.Text, "echo hello world")
	assert.True(t, strings.HasPrefix(result.Content[0].Text.TextSignature, "shell:"))

	// Block 1: shell_call_output formatted as text
	require.True(t, result.Content[1].IsText())
	assert.Contains(t, result.Content[1].Text.Text, "hello world")
	assert.True(t, strings.HasPrefix(result.Content[1].Text.TextSignature, "shell_output:"))

	// Block 2: regular message
	require.True(t, result.Content[2].IsText())
	assert.Equal(t, "Done!", result.Content[2].Text.Text)

	// Verify events include text starts for shell items
	textStartCount := 0
	for _, e := range events {
		if e.Type == ai.EventTextStart {
			textStartCount++
		}
	}
	assert.Equal(t, 3, textStartCount) // shell_call + shell_call_output + message
}

func TestBuildOpenAIResponsesBody_ShellTool(t *testing.T) {
	// GPT-5 model — should add shell tool
	model := &ai.Model{
		ID:            "gpt-5",
		Name:          "GPT-5",
		Api:           ai.ApiOpenAIResponses,
		Provider:      ai.ProviderOpenAI,
		BaseURL:       "http://localhost",
		ContextWindow: 128000,
		MaxTokens:     16384,
	}
	ctx := ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("Hello", 0)},
		Tools:    []ai.Tool{{Name: "read", Description: "Read a file", Parameters: map[string]any{"type": "object"}}},
	}

	// Without server tools — no shell tool
	body, err := buildOpenAIResponsesBody(model, ctx, &ai.StreamOptions{ApiKey: "sk-test"})
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))
	tools := parsed["tools"].([]any)
	assert.Equal(t, 1, len(tools)) // just the function tool

	// With code_execution server tool — adds shell tool
	opts := &ai.StreamOptions{
		ApiKey: "sk-test",
		ServerTools: []ai.AnthropicServerTool{
			{Type: "code_execution_20250825"},
		},
	}
	body, err = buildOpenAIResponsesBody(model, ctx, opts)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &parsed))
	tools = parsed["tools"].([]any)
	assert.Equal(t, 2, len(tools))
	shellTool := tools[1].(map[string]any)
	assert.Equal(t, "shell", shellTool["type"])
	env := shellTool["environment"].(map[string]any)
	assert.Equal(t, "container_auto", env["type"])
}

func TestBuildOpenAIResponsesBody_ShellToolNoFunctionTools(t *testing.T) {
	model := &ai.Model{
		ID:            "gpt-5",
		Name:          "GPT-5",
		Api:           ai.ApiOpenAIResponses,
		Provider:      ai.ProviderOpenAI,
		BaseURL:       "http://localhost",
		ContextWindow: 128000,
		MaxTokens:     16384,
	}
	ctx := ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("Hello", 0)},
	}

	// With code_execution but no function tools — shell tool still added
	opts := &ai.StreamOptions{
		ApiKey: "sk-test",
		ServerTools: []ai.AnthropicServerTool{
			{Type: "code_execution_20250825"},
		},
	}
	body, err := buildOpenAIResponsesBody(model, ctx, opts)
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))
	tools := parsed["tools"].([]any)
	assert.Equal(t, 1, len(tools))
	shellTool := tools[0].(map[string]any)
	assert.Equal(t, "shell", shellTool["type"])
}

func TestBuildOpenAIResponsesBody_ShellToolUnsupportedModel(t *testing.T) {
	// GPT-4o does NOT support shell tool
	model := openaiResponsesTestModel("http://localhost")
	ctx := ai.Context{
		Messages: []ai.Message{ai.NewUserMsg("Hello", 0)},
		Tools:    []ai.Tool{{Name: "read", Description: "Read a file", Parameters: map[string]any{"type": "object"}}},
	}

	opts := &ai.StreamOptions{
		ApiKey: "sk-test",
		ServerTools: []ai.AnthropicServerTool{
			{Type: "code_execution_20250825"},
		},
	}
	body, err := buildOpenAIResponsesBody(model, ctx, opts)
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))
	tools := parsed["tools"].([]any)
	assert.Equal(t, 1, len(tools)) // only function tool, no shell
	funcTool := tools[0].(map[string]any)
	assert.Equal(t, "function", funcTool["type"])
}

func TestConvertResponsesInput_ShellCallReplay(t *testing.T) {
	model := openaiResponsesTestModel("http://localhost")
	shellJSON := `{"type":"shell_call","id":"sh_001","action":{"commands":["ls"]}}`
	shellOutputJSON := `{"type":"shell_call_output","id":"sho_001","output":[{"stdout":"file.txt\n"}]}`

	assistantMsg := &ai.AssistantMessage{
		Role:     ai.RoleAssistant,
		Provider: ai.ProviderOpenAI,
		Api:      ai.ApiOpenAIResponses,
		Model:    "gpt-4o",
		Content: []ai.AssistantContent{
			{Text: &ai.TextContent{Text: "[shell] ls\n", TextSignature: "shell:" + shellJSON}},
			{Text: &ai.TextContent{Text: "file.txt\n", TextSignature: "shell_output:" + shellOutputJSON}},
			{Text: &ai.TextContent{Text: "Found it!", TextSignature: "msg_01"}},
		},
	}

	ctx := ai.Context{
		Messages: []ai.Message{
			ai.NewUserMsg("list files", 0),
			ai.NewAssistantMsg(*assistantMsg),
			ai.NewUserMsg("thanks", 0),
		},
	}

	input := convertResponsesInput(model, ctx)

	// Find the shell_call and shell_call_output items
	var shellItem, shellOutputItem map[string]any
	for _, item := range input {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch m["type"] {
		case "shell_call":
			shellItem = m
		case "shell_call_output":
			shellOutputItem = m
		}
	}
	require.NotNil(t, shellItem, "shell_call item should be replayed")
	assert.Equal(t, "sh_001", shellItem["id"])

	require.NotNil(t, shellOutputItem, "shell_call_output item should be replayed")
	assert.Equal(t, "sho_001", shellOutputItem["id"])
}
