package mcp

import (
	"context"
	"fmt"
	"sort"
	"time"

	firlog "github.com/kfet/fir/pkg/log"
)

// MessageInjector is a function that injects a formatted text message into
// an agent conversation. The caller is responsible for building the
// AgentMessage; this package only provides the text.
type MessageInjector func(text string, ts int64)

// WireChannelInjection configures the manager's OnChannelMessage callback
// to format inbound channel messages, send a typing indicator when
// appropriate, and call inject to deliver the message to the agent.
func WireChannelInjection(mgr *Manager, inject MessageInjector) {
	mgr.SetOnChannelMessage(func(cm ChannelMessage) {
		serverName := cm.ServerName
		source := cm.SourceName()
		meta := formatMeta(cm.Meta)
		text := fmt.Sprintf("[Channel message from %s via %s%s]\n%s", source, serverName, meta, cm.Text())
		ts := time.Now().UnixMilli()
		firlog.Info("injecting channel message", "server", serverName, "source", source)

		// Send typing indicator if the server has a "reply" tool.
		if mgr.HasServerTools(serverName, "reply") {
			if err := SendTypingIndicator(context.Background(), mgr, serverName, cm.Meta); err != nil {
				firlog.Debug("typing indicator failed", "err", err)
			}
		}

		inject(text, ts)
	})
}

// formatMeta renders channel metadata as key=value pairs for inclusion in
// the message header. Keys are sorted for deterministic output. The "user"
// key is excluded since it's already in the "from" field. The "history"
// key is excluded since it carries bulk conversation history (handled
// separately by the caller).
func formatMeta(meta map[string]any) string {
	if len(meta) == 0 {
		return ""
	}
	keys := make([]string, 0, len(meta))
	for k := range meta {
		if k == "user" || k == "history" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var s string
	for _, k := range keys {
		s += fmt.Sprintf(" %s=%q", k, fmt.Sprint(meta[k]))
	}
	return s
}
