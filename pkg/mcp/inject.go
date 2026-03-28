package mcp

import (
	"context"
	"fmt"
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
	mgr.OnChannelMessage.Store(func(cm ChannelMessage) {
		serverName := cm.ServerName
		source := cm.SourceName()
		text := fmt.Sprintf("[Channel message from %s via %s]\n%s", source, serverName, cm.Text())
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
