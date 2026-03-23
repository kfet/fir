package mcp

import (
	"context"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExtractMessageID(t *testing.T) {
	result := &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: "msg-123"},
		},
	}
	if id := extractMessageID(result); id != "msg-123" {
		t.Errorf("expected msg-123, got %s", id)
	}
	if id := extractMessageID(nil); id != "" {
		t.Errorf("expected empty, got %s", id)
	}
	if id := extractMessageID(&sdk.CallToolResult{}); id != "" {
		t.Errorf("expected empty, got %s", id)
	}
}

func TestTypingIndicator_StopWithoutStart(t *testing.T) {
	ti := &TypingIndicator{}
	if err := ti.Stop(context.Background(), "hello"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNewTypingIndicator(t *testing.T) {
	meta := map[string]any{"chat_id": "c1"}
	ti := NewTypingIndicator(nil, "srv", meta)
	if ti.serverName != "srv" {
		t.Errorf("expected 'srv', got %s", ti.serverName)
	}
	if ti.meta["chat_id"] != "c1" {
		t.Errorf("expected chat_id c1, got %v", ti.meta["chat_id"])
	}
}
