package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
)

// ── unit tests ────────────────────────────────────────────────────────────────

func TestSamplingMessagesToAI_Text(t *testing.T) {
	msgs := []*sdk.SamplingMessage{
		{Role: "user", Content: &sdk.TextContent{Text: "hello"}},
		{Role: "assistant", Content: &sdk.TextContent{Text: "hi there"}},
	}
	got, err := samplingMessagesToAI(msgs)
	require.NoError(t, err)
	require.Len(t, got, 2)

	assert.Equal(t, "user", got[0].Role())
	assert.Equal(t, "assistant", got[1].Role())
	a := got[1].AsAssistant()
	require.NotNil(t, a)
	require.Len(t, a.Content, 1)
	assert.Equal(t, "hi there", a.Content[0].Text.Text)
}

func TestSamplingMessagesToAI_Image(t *testing.T) {
	msgs := []*sdk.SamplingMessage{
		{Role: "user", Content: &sdk.ImageContent{MIMEType: "image/png", Data: []byte{1, 2, 3}}},
	}
	got, err := samplingMessagesToAI(msgs)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "user", got[0].Role())
}

func TestSamplingMessagesToAI_EmptyInput(t *testing.T) {
	got, err := samplingMessagesToAI(nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestSamplingMessagesToAI_UnknownRole(t *testing.T) {
	msgs := []*sdk.SamplingMessage{
		{Role: "system", Content: &sdk.TextContent{Text: "be helpful"}},
	}
	_, err := samplingMessagesToAI(msgs)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "system")
}

func TestAssistantToCreateMessageResult(t *testing.T) {
	msg := &ai.AssistantMessage{
		Content:    []ai.AssistantContent{{Text: &ai.TextContent{Text: "pong"}}},
		StopReason: ai.StopReasonStop,
	}
	res := assistantToCreateMessageResult(msg, "claude-opus-4-5")
	assert.Equal(t, "claude-opus-4-5", res.Model)
	assert.Equal(t, "endTurn", res.StopReason)
	tc, ok := res.Content.(*sdk.TextContent)
	require.True(t, ok, "expected *sdk.TextContent")
	assert.Equal(t, "pong", tc.Text)
}

func TestSamplingStopReason(t *testing.T) {
	cases := []struct {
		in   ai.StopReason
		want string
	}{
		{ai.StopReasonStop, "endTurn"},
		{ai.StopReasonLength, "maxTokens"},
		{ai.StopReasonToolUse, "toolUse"},
		{ai.StopReasonError, "error"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, samplingStopReason(tc.in), "input: %s", tc.in)
	}
}

// ── integration test ──────────────────────────────────────────────────────────

// TestManager_SamplingFn verifies that when an MCP server issues a
// sampling/createMessage request, the Manager's SamplingFn is called and its
// response is returned to the server.
func TestManager_SamplingFn(t *testing.T) {
	// Build a server that exposes a "ping_llm" tool which asks the client for
	// sampling.
	//
	// Protocol 2026-07-28 forbids a server from initiating
	// sampling/createMessage while serving a request; it must ask via a Multi
	// Round-Trip Request (SEP-2322). The client SDK's MRTR middleware answers
	// it through the CreateMessageHandler and retries tools/call with
	// inputResponses attached, so this handler runs twice.
	server := sdk.NewServer(&sdk.Implementation{Name: "sample-srv", Version: "0"}, nil)
	server.AddTool(
		&sdk.Tool{Name: "ping_llm", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
		func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			resp, ok := req.Params.InputResponses[samplingInputKey]
			if !ok {
				return &sdk.CallToolResult{
					InputRequests: sdk.InputRequestMap{
						samplingInputKey: &sdk.CreateMessageParams{
							MaxTokens: 100,
							Messages: []*sdk.SamplingMessage{
								{Role: "user", Content: &sdk.TextContent{Text: "ping"}},
							},
						},
					},
				}, nil
			}
			// go-sdk fulfils a *CreateMessageParams input request via
			// CreateMessageWithTools, so the response is the with-tools
			// shape even though fir registers the basic CreateMessageHandler
			// (the SDK down/up-converts around fir's SamplingFn).
			result, ok := resp.(*sdk.CreateMessageWithToolsResult)
			if !ok {
				return &sdk.CallToolResult{
					IsError: true,
					Content: []sdk.Content{&sdk.TextContent{Text: "unexpected input response type"}},
				}, nil
			}
			if len(result.Content) != 1 {
				return &sdk.CallToolResult{
					IsError: true,
					Content: []sdk.Content{&sdk.TextContent{Text: "expected exactly one content block"}},
				}, nil
			}
			tc, ok := result.Content[0].(*sdk.TextContent)
			if !ok {
				return &sdk.CallToolResult{
					IsError: true,
					Content: []sdk.Content{&sdk.TextContent{Text: "sampling result was not text"}},
				}, nil
			}
			return &sdk.CallToolResult{
				Content: []sdk.Content{&sdk.TextContent{Text: tc.Text}},
			}, nil
		},
	)
	mgr := NewManager(map[string]ServerConfig{"ssrv": {}}, false)
	mgr.dialFn = inMemoryDial(t, server)

	// Install a mock SamplingFn that returns a canned response.
	samplingCalled := make(chan struct{}, 1)
	mgr.SamplingFn = func(_ context.Context, req *sdk.CreateMessageRequest) (*sdk.CreateMessageResult, error) {
		samplingCalled <- struct{}{}
		return &sdk.CreateMessageResult{
			Role:    "assistant",
			Model:   "mock-model",
			Content: &sdk.TextContent{Text: "pong"},
		}, nil
	}

	ctx := context.Background()
	tools := startAndWait(t, mgr, ctx)
	defer mgr.Close()

	// Find the ping_llm tool.
	var pingTool *agent.AgentTool
	for i := range tools {
		if tools[i].Name == "mcp__ssrv__ping_llm" {
			pingTool = &tools[i]
			break
		}
	}
	require.NotNil(t, pingTool, "ping_llm tool not found")

	// Call the tool — it will trigger sampling/createMessage on the server side.
	result, execErr := pingTool.Execute(ctx, "", nil, nil)
	require.NoError(t, execErr)

	// The sampling handler should have been called.
	select {
	case <-samplingCalled:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout: SamplingFn not called")
	}

	// The tool result should contain the text returned by SamplingFn.
	require.Len(t, result.Content, 1)
	assert.Contains(t, result.Content[0].Text, "pong")
}
