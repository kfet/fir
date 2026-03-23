package mcp

import (
	"context"
	"fmt"

	firlog "github.com/kfet/fir/pkg/log"
)

// SendTypingIndicator sends a "…" message via an MCP channel server's
// "reply" tool to signal that a response is incoming. The placeholder
// stays in the chat — the agent's own reply follows it.
func SendTypingIndicator(ctx context.Context, mgr *Manager, serverName string, meta map[string]any) error {
	args := make(map[string]any, len(meta)+1)
	for k, v := range meta {
		args[k] = v
	}
	args["text"] = "…"

	if _, err := mgr.CallTool(ctx, serverName, "reply", args); err != nil {
		return fmt.Errorf("typing indicator: %w", err)
	}
	firlog.Debug("typing indicator sent", "server", serverName)
	return nil
}
