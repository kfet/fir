package extension

import (
	"context"
	"testing"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/session"
)

// TestSessionBridge_CallTool_StampsInternal proves that tool calls issued via
// the extension path (SessionBridge.CallTool — used by pipe/wait/aside and all
// extensions) are stamped as internal re-entrant calls. This is the seam that
// keeps the default MCP tool-call timeout from clipping a timeout=-1 pipe/wait
// step whose body is itself a slow MCP tool: the MCP timeout wrapper bypasses
// its bound whenever session.IsInternalToolCall(ctx) is true.
func TestSessionBridge_CallTool_StampsInternal(t *testing.T) {
	sess := newSetupTestSession(t, t.TempDir())
	sb := NewSessionBridge(sess)

	var sawInternal bool
	sb.RegisterTool(ToolDefinition{
		Name:        "probe_tool",
		Description: "records whether the ctx is stamped internal",
		Parameters:  map[string]any{"type": "object"},
		Execute: func(tc ToolContext) (ToolResult, error) {
			sawInternal = session.IsInternalToolCall(tc.Context)
			return ToolResult{
				Content: []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: "ok"}},
			}, nil
		},
	})

	if _, err := sb.CallTool(context.Background(), "probe_tool", nil); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !sawInternal {
		t.Fatal("SessionBridge.CallTool must stamp the ctx as an internal tool call")
	}
}
