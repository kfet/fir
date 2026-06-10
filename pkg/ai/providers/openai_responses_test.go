package providers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openaiResponsesTestModel(serverURL string) *ai.Model {
	return &ai.Model{
		ID:            "gpt-4o",
		Name:          "GPT-4o",
		API:           ai.ApiOpenAIResponses,
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

	stream := StreamOpenAIResponses(context.Background(), model, ctx, &ai.StreamOptions{APIKey: "sk-test"})
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

	stream := StreamOpenAIResponses(context.Background(), model, ctx, &ai.StreamOptions{APIKey: "sk-test"})
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
	prev := openaiRetryDelayFn
	openaiRetryDelayFn = func(_ int) time.Duration { return 0 }
	t.Cleanup(func() { openaiRetryDelayFn = prev })

	srv := mockSSEServer(t, "openai_responses_error.sse")
	defer srv.Close()

	model := openaiResponsesTestModel(srv.URL)
	ctx := ai.Context{Messages: []ai.Message{ai.NewUserMsg("Hello", 0)}}

	stream := StreamOpenAIResponses(context.Background(), model, ctx, &ai.StreamOptions{APIKey: "sk-test"})
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

	stream := StreamOpenAIResponses(context.Background(), model, ctx, &ai.StreamOptions{APIKey: "sk-test"})
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
		API:           ai.ApiOpenAIResponses,
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
	body, err := buildOpenAIResponsesBody(model, ctx, &ai.StreamOptions{APIKey: "sk-test"})
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))
	tools := parsed["tools"].([]any)
	assert.Equal(t, 1, len(tools)) // just the function tool

	// With code_execution server tool — adds shell tool
	opts := &ai.StreamOptions{
		APIKey: "sk-test",
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
		API:           ai.ApiOpenAIResponses,
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
		APIKey: "sk-test",
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
		APIKey: "sk-test",
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
		API:      ai.ApiOpenAIResponses,
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

func TestBuildOpenAIResponsesBody_PoeCompat(t *testing.T) {
	// Poe's /v1/responses proxy rejects OpenAI-only reasoning extensions:
	// - `include: ["reasoning.encrypted_content"]` — unknown include value
	// - `reasoning.effort: "none"` — enum is {low, medium, high, xhigh}
	// - the `developer` role — only system/user/assistant allowed
	model := &ai.Model{
		ID:            "gpt-5.3-codex-spark",
		API:           ai.ApiOpenAIResponses,
		Provider:      ai.ProviderPoe,
		BaseURL:       "https://api.poe.com/v1",
		Reasoning:     true,
		ContextWindow: 128000,
		MaxTokens:     16384,
	}
	ctx := ai.Context{
		SystemPrompt: "You are helpful.",
		Messages:     []ai.Message{ai.NewUserMsg("hi", 0)},
	}

	// No reasoning effort → `reasoning` field omitted entirely (not "none").
	body, err := buildOpenAIResponsesBody(model, ctx, &ai.StreamOptions{APIKey: "sk-test"})
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))
	_, hasReasoning := parsed["reasoning"]
	assert.False(t, hasReasoning, "reasoning should be omitted for Poe when no effort is set")
	_, hasInclude := parsed["include"]
	assert.False(t, hasInclude, "include should never be sent to Poe")

	// System prompt uses `system` role (not `developer`).
	input := parsed["input"].([]any)
	first := input[0].(map[string]any)
	assert.Equal(t, "system", first["role"], "Poe rejects the developer role")

	// With explicit reasoning effort → reasoning is sent but `include` is still omitted.
	effort := ai.ThinkingHigh
	opts := &ai.StreamOptions{APIKey: "sk-test", ReasoningEffort: effort}
	body, err = buildOpenAIResponsesBody(model, ctx, opts)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &parsed))
	reasoning := parsed["reasoning"].(map[string]any)
	assert.Equal(t, "high", reasoning["effort"])
	_, hasInclude = parsed["include"]
	assert.False(t, hasInclude, "include should not be sent to Poe even with explicit effort")
}

func TestBuildOpenAIResponsesBody_OpenAIStillGetsEncryptedContent(t *testing.T) {
	// Regression guard: the Poe carve-out must not strip encrypted-content
	// includes from real OpenAI requests.
	model := &ai.Model{
		ID:            "gpt-5",
		API:           ai.ApiOpenAIResponses,
		Provider:      ai.ProviderOpenAI,
		BaseURL:       "https://api.openai.com/v1",
		Reasoning:     true,
		ContextWindow: 128000,
		MaxTokens:     16384,
	}
	ctx := ai.Context{
		SystemPrompt: "sys",
		Messages:     []ai.Message{ai.NewUserMsg("hi", 0)},
	}
	effort := ai.ThinkingMedium
	body, err := buildOpenAIResponsesBody(model, ctx, &ai.StreamOptions{APIKey: "sk", ReasoningEffort: effort})
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))
	include := parsed["include"].([]any)
	assert.Equal(t, "reasoning.encrypted_content", include[0])

	input := parsed["input"].([]any)
	first := input[0].(map[string]any)
	assert.Equal(t, "developer", first["role"])
}

// Regression: Poe's /v1/responses only sends `response.output_text.delta`
// (for text) and a final `response.completed` carrying the full
// `response.output` array — no `response.output_item.added` / `.done`
// events. fir must still capture text and tool calls.
func TestResponsesSSE_PoeNoItemEvents_Text(t *testing.T) {
	output := &ai.AssistantMessage{
		Role:       ai.RoleAssistant,
		Content:    []ai.AssistantContent{},
		StopReason: ai.StopReasonStop,
		Usage:      ai.ZeroUsage(),
	}
	stream := ai.NewAssistantMessageEventStream()
	proc := &responsesSSEProcessor{output: output, stream: stream, model: &ai.Model{}}

	// Consume events in a goroutine so Push() doesn't block.
	go func() {
		for range stream.Events {
		}
	}()

	// Exact shape Poe sends: the delta event's item_id is the
	// *response* id (resp_...), while the final response.output[]
	// carries the real message id (msg_...). Previously this mismatch
	// caused the reconciler to duplicate the message.
	events := []string{
		`{"type":"response.output_text.delta","item_id":"resp_123","output_index":0,"content_index":0,"delta":"Hi"}`,
		`{"type":"response.completed","response":{"id":"resp_123","status":"completed","output":[` +
			`{"id":"rs_1","type":"reasoning","summary":[]},` +
			`{"id":"msg_1","type":"message","role":"assistant","status":"completed",` +
			`"content":[{"type":"output_text","text":"Hi"}]}` +
			`],"usage":{"input_tokens":10,"output_tokens":1,"total_tokens":11}}}`,
	}
	for _, e := range events {
		done, err := proc.processEvent(e)
		require.NoError(t, err)
		_ = done
	}
	stream.End(nil)

	// Lazy-open happened first (text delta), then reasoning was
	// replayed from response.output at completion. Order reflects
	// arrival, not the server's output[] array.
	require.Len(t, output.Content, 2, "must not duplicate the lazy-opened message item")
	require.True(t, output.Content[0].IsText())
	assert.Equal(t, "Hi", output.Content[0].Text.Text)
	// Text signature should be set from response.output[] so multi-turn
	// replay can reference the real msg_ id.
	assert.Contains(t, output.Content[0].Text.TextSignature, "msg_1")
	assert.True(t, output.Content[1].IsThinking())
	assert.Equal(t, ai.StopReasonStop, output.StopReason)
}

func TestResponsesSSE_PoeNoItemEvents_ToolCall(t *testing.T) {
	output := &ai.AssistantMessage{
		Role:       ai.RoleAssistant,
		Content:    []ai.AssistantContent{},
		StopReason: ai.StopReasonStop,
		Usage:      ai.ZeroUsage(),
	}
	stream := ai.NewAssistantMessageEventStream()
	proc := &responsesSSEProcessor{output: output, stream: stream, model: &ai.Model{}}
	go func() {
		for range stream.Events {
		}
	}()

	// Poe emits NO delta events for tool-call-only responses — just
	// `response.completed` with the entire output array.
	events := []string{
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[` +
			`{"id":"rs_1","type":"reasoning","summary":[]},` +
			`{"id":"fc_1","type":"function_call","call_id":"call_abc","name":"list_dir",` +
			`"arguments":"{\"path\":\"/tmp\"}","status":"completed"}` +
			`],"usage":{"input_tokens":48,"output_tokens":76,"total_tokens":124}}}`,
	}
	for _, e := range events {
		_, err := proc.processEvent(e)
		require.NoError(t, err)
	}
	stream.End(nil)

	// Expect reasoning + tool call, and stop reason flipped to tool_use.
	require.Len(t, output.Content, 2)
	assert.True(t, output.Content[0].IsThinking())
	require.True(t, output.Content[1].IsToolCall())
	tc := output.Content[1].ToolCall
	assert.Equal(t, "list_dir", tc.Name)
	assert.Equal(t, "call_abc|fc_1", tc.ID)
	assert.Equal(t, "/tmp", tc.Arguments["path"])
	assert.Equal(t, ai.StopReasonToolUse, output.StopReason)
}

// Regression guard: the Poe-shaped reconciler in handleResponseCompleted
// must be a no-op for real OpenAI streams where every output item was
// already streamed via response.output_item.added / .done. No content
// blocks should be duplicated.
func TestResponsesSSE_NormalOpenAIStream_NoDuplication(t *testing.T) {
	output := &ai.AssistantMessage{
		Role:       ai.RoleAssistant,
		Content:    []ai.AssistantContent{},
		StopReason: ai.StopReasonStop,
		Usage:      ai.ZeroUsage(),
	}
	stream := ai.NewAssistantMessageEventStream()
	proc := &responsesSSEProcessor{output: output, stream: stream, model: &ai.Model{}}
	go func() {
		for range stream.Events {
		}
	}()

	events := []string{
		`{"type":"response.output_item.added","item":{"id":"msg_1","type":"message","role":"assistant"}}`,
		`{"type":"response.output_text.delta","item_id":"msg_1","delta":"Hello"}`,
		`{"type":"response.output_item.done","item":{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello"}]}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[` +
			`{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello"}]}` +
			`],"usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}}}`,
	}
	for _, e := range events {
		_, err := proc.processEvent(e)
		require.NoError(t, err)
	}
	stream.End(nil)

	require.Len(t, output.Content, 1, "normal OpenAI stream must not duplicate items via the Poe reconciler")
	assert.True(t, output.Content[0].IsText())
	assert.Equal(t, "Hello", output.Content[0].Text.Text)
}
