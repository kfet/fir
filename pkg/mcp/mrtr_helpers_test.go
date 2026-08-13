package mcp

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// rootsEchoTool is a tool handler that collects the client's roots through a
// Multi Round-Trip Request (SEP-2322) and echoes them back as a newline
// separated list of URIs.
//
// Protocol version 2026-07-28 forbids a server from initiating a roots/list
// request while serving a request; MRTR is the supported replacement. The
// client SDK's multi-round-trip middleware answers the request and retries the
// original tools/call with inputResponses attached, so the handler runs twice.
func rootsEchoTool(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
	resp, ok := req.Params.InputResponses[rootsInputKey]
	if !ok {
		return &sdk.CallToolResult{
			InputRequests: sdk.InputRequestMap{rootsInputKey: &sdk.ListRootsParams{}},
		}, nil
	}
	res, ok := resp.(*sdk.ListRootsResult)
	if !ok {
		return &sdk.CallToolResult{IsError: true}, nil
	}
	uris := make([]string, 0, len(res.Roots))
	for _, r := range res.Roots {
		uris = append(uris, r.URI)
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{&sdk.TextContent{Text: strings.Join(uris, "\n")}},
	}, nil
}

const rootsInputKey = "roots"

// elicitInputKey and samplingInputKey are the MRTR input-request IDs used by
// the elicitation and sampling integration tests.
const (
	elicitInputKey   = "elicit"
	samplingInputKey = "sampling"
)

// callRootsEcho invokes the rootsEchoTool named "noop" on server "s" through
// the Manager and returns the root URIs the server observed.
func callRootsEcho(t *testing.T, ctx context.Context, mgr *Manager) []string {
	t.Helper()
	res, err := mgr.CallTool(ctx, "s", "noop", nil)
	require.NoError(t, err)
	require.False(t, res.IsError, "roots echo tool reported an error")
	require.Len(t, res.Content, 1)
	text, ok := res.Content[0].(*sdk.TextContent)
	require.True(t, ok, "expected text content")
	if text.Text == "" {
		return nil
	}
	return strings.Split(text.Text, "\n")
}
