package extension

import (
	"context"
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

// TestSessionBridge_CallTool_ForwardsMetaAndDetails verifies the bridge
// round-trip for extension-visible tool results: a registered tool's Meta and
// Details survive CallTool (extension -> agent -> extension), and the text
// content stays clean (meta is never rendered into text on the bridge path).
func TestSessionBridge_CallTool_ForwardsMetaAndDetails(t *testing.T) {
	sess := newSetupTestSession(t, t.TempDir())
	sb := NewSessionBridge(sess)

	sb.RegisterTool(ToolDefinition{
		Name:        "meta_tool",
		Description: "returns meta",
		Parameters:  map[string]any{"type": "object"},
		Execute: func(ToolContext) (ToolResult, error) {
			return ToolResult{
				Content: []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: "clean output"}},
				Details: map[string]any{"hash": "deadbeef"},
				Meta:    map[string]string{"hash": "deadbeef"},
			}, nil
		},
	})

	res, err := sb.CallTool(context.Background(), "meta_tool", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "clean output" {
		t.Fatalf("expected clean single text block, got %+v", res.Content)
	}
	if res.Meta["hash"] != "deadbeef" {
		t.Errorf("expected meta forwarded, got %+v", res.Meta)
	}
	if res.Details["hash"] != "deadbeef" {
		t.Errorf("expected details forwarded, got %+v", res.Details)
	}
}

func TestDetailsAsMap(t *testing.T) {
	if m := detailsAsMap(nil); m != nil {
		t.Errorf("nil details: expected nil, got %v", m)
	}
	if m := detailsAsMap(map[string]any{"k": "v"}); m["k"] != "v" {
		t.Errorf("map details: expected passthrough, got %v", m)
	}
	type typed struct {
		Hash string `json:"hash"`
	}
	if m := detailsAsMap(&typed{Hash: "abc"}); m["hash"] != "abc" {
		t.Errorf("typed details: expected JSON roundtrip, got %v", m)
	}
	if m := detailsAsMap(42); m != nil {
		t.Errorf("non-object details: expected nil, got %v", m)
	}
}
