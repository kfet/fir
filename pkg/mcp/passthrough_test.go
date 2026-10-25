package mcp

import (
	"context"
	"testing"

	"github.com/kfet/fir/pkg/mcp/mcptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startUIServer(t *testing.T) (*Manager, map[string]int) {
	t.Helper()
	mgr := NewManager(map[string]ServerConfig{"ui": {}}, false)
	mgr.dialFn = inMemoryDial(t, mcptest.NewUIServer())
	t.Cleanup(func() { _ = mgr.Close() })
	tools := startAndWait(t, mgr, context.Background())
	idx := map[string]int{}
	for i, tl := range tools {
		idx[tl.Name] = i
	}
	return mgr, idx
}

func TestPassthrough_ToolOrigin(t *testing.T) {
	mgr, idx := startUIServer(t)
	name := ToolName("ui", mcptest.UIToolName)
	require.Contains(t, idx, name)

	o, ok := mgr.ToolOrigin(name)
	require.True(t, ok)
	assert.Equal(t, "ui", o.Server)
	assert.Equal(t, mcptest.UIToolName, o.Name)
	assert.Equal(t, map[string]any{"resourceUri": mcptest.UIResourceURI}, o.Meta["ui"])

	_, ok = mgr.ToolOrigin("read")
	assert.False(t, ok)
	var nilMgr *Manager
	_, ok = nilMgr.ToolOrigin(name)
	assert.False(t, ok)
}

func TestPassthrough_ResultDetails(t *testing.T) {
	mgr, idx := startUIServer(t)
	tool := mgr.allTools()[idx[ToolName("ui", mcptest.UIToolName)]]

	res, err := tool.Execute(context.Background(), "", map[string]any{"n": 3.0}, nil)
	require.NoError(t, err)
	details, ok := res.Details.(map[string]any)
	require.True(t, ok)
	pt, ok := details[DetailsKey].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "ui", pt["server"])
	assert.Equal(t, mcptest.UIToolName, pt["tool"])
	assert.Equal(t, "result-meta", pt["_meta"].(map[string]any)["demo"])
	assert.Equal(t, map[string]any{"n": 3.0}, pt["structuredContent"])

	resources, ok := pt["resources"].([]any)
	require.True(t, ok)
	require.Len(t, resources, 2)
	emb := resources[0].(map[string]any)
	assert.Equal(t, "resource", emb["type"])
	assert.Equal(t, mcptest.UIResourceURI, emb["resource"].(map[string]any)["uri"])
	link := resources[1].(map[string]any)
	assert.Equal(t, "resource_link", link["type"])
	assert.Equal(t, mcptest.LinkURI, link["uri"])

	assert.Equal(t, []int{1, 2}, pt["flattened"])

	// The LLM-facing content stays text.
	for _, c := range res.Content {
		assert.Equal(t, "text", string(c.Type))
	}
}

func TestPassthrough_ReadResource(t *testing.T) {
	mgr, _ := startUIServer(t)
	res, err := mgr.ReadResource(context.Background(), "ui", mcptest.UIResourceURI)
	require.NoError(t, err)
	require.Len(t, res.Contents, 1)
	assert.Equal(t, mcptest.UIHTML, res.Contents[0].Text)

	_, err = mgr.ReadResource(context.Background(), "nope", mcptest.UIResourceURI)
	assert.Error(t, err)
}
