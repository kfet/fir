package mcp

import (
	"context"
	"fmt"
	"sync"

	firlog "github.com/kfet/fir/pkg/log"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TypingIndicator sends a "Thinking..." placeholder message via an MCP
// channel server's "reply" tool, then replaces it with the final content
// via "edit_message" when Stop is called.
type TypingIndicator struct {
	mgr        *Manager
	serverName string
	meta       map[string]any

	mu        sync.Mutex
	messageID string
}

// NewTypingIndicator creates a typing indicator. serverName is the MCP server
// key; meta is forwarded as tool arguments (e.g. chat_id, thread_ts).
func NewTypingIndicator(mgr *Manager, serverName string, meta map[string]any) *TypingIndicator {
	return &TypingIndicator{
		mgr:        mgr,
		serverName: serverName,
		meta:       meta,
	}
}

// Start sends the placeholder message and stores its ID for later editing.
func (ti *TypingIndicator) Start(ctx context.Context) error {
	args := make(map[string]any, len(ti.meta)+1)
	for k, v := range ti.meta {
		args[k] = v
	}
	args["content"] = "Thinking..."

	result, err := ti.mgr.CallTool(ctx, ti.serverName, "reply", args)
	if err != nil {
		return fmt.Errorf("typing indicator: reply failed: %w", err)
	}

	msgID := extractMessageID(result)
	if msgID == "" {
		return fmt.Errorf("typing indicator: reply did not return a message_id")
	}

	ti.mu.Lock()
	ti.messageID = msgID
	ti.mu.Unlock()

	firlog.Debug("typing indicator started", "server", ti.serverName, "message_id", msgID)
	return nil
}

// Stop replaces the placeholder with finalContent via edit_message.
func (ti *TypingIndicator) Stop(ctx context.Context, finalContent string) error {
	ti.mu.Lock()
	msgID := ti.messageID
	ti.mu.Unlock()

	if msgID == "" {
		return nil
	}

	args := make(map[string]any, len(ti.meta)+2)
	for k, v := range ti.meta {
		args[k] = v
	}
	args["message_id"] = msgID
	args["content"] = finalContent

	if _, err := ti.mgr.CallTool(ctx, ti.serverName, "edit_message", args); err != nil {
		return fmt.Errorf("typing indicator: final edit failed: %w", err)
	}
	firlog.Debug("typing indicator stopped", "server", ti.serverName, "message_id", msgID)
	return nil
}

// MessageID returns the placeholder message ID, or "" if Start hasn't succeeded.
func (ti *TypingIndicator) MessageID() string {
	ti.mu.Lock()
	defer ti.mu.Unlock()
	return ti.messageID
}

// extractMessageID pulls a message_id from a CallTool result's text content.
func extractMessageID(result *sdk.CallToolResult) string {
	if result == nil {
		return ""
	}
	for _, c := range result.Content {
		if tc, ok := c.(*sdk.TextContent); ok && tc.Text != "" {
			return tc.Text
		}
	}
	return ""
}
