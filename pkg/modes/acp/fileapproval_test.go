package acp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kfet/fir/pkg/mcp"
)

func TestMCPFileApproval(t *testing.T) {
	req := mcp.FileApprovalRequest{ToolCallID: "tc1", Server: "drive", Tool: "save",
		Files: []mcp.FileApprovalItem{{Path: "/tmp/a.pdf", Size: 3}}}

	conn := &mockConn{}
	ok, err := mcpFileApproval(conn, "s1")(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, ok, "cancelled denies")
	require.Len(t, conn.permissions, 1)
	p := conn.permissions[0]
	assert.Equal(t, "s1", string(p.SessionId))
	assert.Equal(t, "tc1", string(p.ToolCall.ToolCallId))
	require.NotNil(t, p.ToolCall.Title)
	assert.Contains(t, *p.ToolCall.Title, "/tmp/a.pdf")
	assert.Contains(t, *p.ToolCall.Title, `"drive"`)

	conn.permissionOption = fileApprovalReject
	ok, err = mcpFileApproval(conn, "s1")(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, ok)

	conn.permissionOption = fileApprovalAllow
	ok, err = mcpFileApproval(conn, "s1")(context.Background(), req)
	require.NoError(t, err)
	assert.True(t, ok)
}
