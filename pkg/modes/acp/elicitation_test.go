package acp

import (
	"context"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPElicitation_NoClientSupport(t *testing.T) {
	fn, modes := mcpElicitation(newMockConn(), "s1", nil)
	assert.Nil(t, fn)
	assert.False(t, modes.Form || modes.URL)
}

func TestMCPElicitation_FormForwarded(t *testing.T) {
	conn := newMockConn()
	conn.elicitResponse = &acpsdk.UnstableCreateElicitationResponse{Accept: &acpsdk.UnstableCreateElicitationAccept{Action: "accept", Content: map[string]any{"name": "Ann"}}}
	fn, modes := mcpElicitation(conn, "s1", &acpsdk.ElicitationCapabilities{})
	require.NotNil(t, fn)
	assert.True(t, modes.Form)
	assert.False(t, modes.URL)

	res, err := fn(context.Background(), &sdk.ElicitRequest{Params: &sdk.ElicitParams{
		Message: "Name?", RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}},
	}})
	require.NoError(t, err)
	assert.Equal(t, "accept", res.Action)
	assert.Equal(t, "Ann", res.Content["name"])
	require.Len(t, conn.elicitations, 1)
	f := conn.elicitations[0].Form
	require.NotNil(t, f)
	assert.Equal(t, "Name?", f.Message)
	assert.Equal(t, "s1", f.Meta["sessionId"])
	assert.Contains(t, f.RequestedSchema.Properties, "name")

	// URL mode was not declared by the client: declined without asking.
	res, err = fn(context.Background(), &sdk.ElicitRequest{Params: &sdk.ElicitParams{Mode: "url", URL: "https://x.test"}})
	require.NoError(t, err)
	assert.Equal(t, "decline", res.Action)
	assert.Len(t, conn.elicitations, 1)
}

func TestMCPElicitation_URLAndCancel(t *testing.T) {
	conn := newMockConn()
	conn.elicitResponse = &acpsdk.UnstableCreateElicitationResponse{Cancel: &acpsdk.UnstableCreateElicitationCancel{Action: "cancel"}}
	fn, modes := mcpElicitation(conn, "s1", &acpsdk.ElicitationCapabilities{Url: &acpsdk.ElicitationUrlCapabilities{}})
	assert.True(t, modes.URL)
	assert.False(t, modes.Form)
	res, err := fn(context.Background(), &sdk.ElicitRequest{Params: &sdk.ElicitParams{Mode: "url", URL: "https://x.test", ElicitationID: "e1"}})
	require.NoError(t, err)
	assert.Equal(t, "cancel", res.Action)
	require.NotNil(t, conn.elicitations[0].Url)
	assert.Equal(t, "https://x.test", conn.elicitations[0].Url.Url)
}
