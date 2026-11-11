package acp

import (
	"context"
	"fmt"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/kfet/fir/pkg/mcp"
)

const (
	fileApprovalAllow  acpsdk.PermissionOptionId = "allow"
	fileApprovalReject acpsdk.PermissionOptionId = "reject"
)

// mcpFileApproval approves sending local files to an MCP server (the files
// extension) by asking the ACP client with session/request_permission.
// Anything but an explicit "allow" — cancel, error, another option — denies.
func mcpFileApproval(conn acpConn, sessionID string) mcp.FileApprovalFn {
	return func(ctx context.Context, req mcp.FileApprovalRequest) (bool, error) {
		names := make([]string, 0, len(req.Files))
		locs := make([]acpsdk.ToolCallLocation, 0, len(req.Files))
		for _, f := range req.Files {
			names = append(names, fmt.Sprintf("%s (%d bytes)", f.Path, f.Size))
			locs = append(locs, acpsdk.ToolCallLocation{Path: f.Path})
		}
		title := fmt.Sprintf("Send %s to MCP server %q (tool %s) and storage it designates?",
			strings.Join(names, ", "), req.Server, req.Tool)
		id := req.ToolCallID
		if id == "" {
			id = "mcp-files-" + req.Server + "-" + req.Tool
		}
		resp, err := conn.RequestPermission(ctx, acpsdk.RequestPermissionRequest{
			SessionId: acpsdk.SessionId(sessionID),
			ToolCall:  acpsdk.ToolCallUpdate{ToolCallId: acpsdk.ToolCallId(id), Title: &title, Locations: locs},
			Options: []acpsdk.PermissionOption{
				{OptionId: fileApprovalAllow, Name: "Send", Kind: acpsdk.PermissionOptionKindAllowOnce},
				{OptionId: fileApprovalReject, Name: "Don't send", Kind: acpsdk.PermissionOptionKindRejectOnce},
			},
		})
		if err != nil {
			return false, err
		}
		sel := resp.Outcome.Selected
		return sel != nil && sel.OptionId == fileApprovalAllow, nil
	}
}
