package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync/atomic"
	"time"

	firlog "github.com/kfet/fir/pkg/log"
	"github.com/kfet/fir/pkg/mcp/history"
)

// MessageInjector is a function that injects a formatted text message into
// an agent conversation. The caller is responsible for building the
// AgentMessage; this package only provides the text.
type MessageInjector func(text string, ts int64)

// SessionLengthFunc returns the number of messages in the current session.
// Used to decide whether to inject conversation history on the first message.
type SessionLengthFunc func() int

// ChannelReplyHook is called when a channel message arrives from a server
// that has a "reply" tool. It receives the server name and message_id.
// Used to wire auto-reply streaming.
type ChannelReplyHook func(serverName, messageID string)

// WireChannelInjection configures the manager's OnChannelMessage callback
// to format inbound channel messages, send a typing indicator when
// appropriate, and call inject to deliver the message to the agent.
//
// If sessionLen is non-nil and returns 0 when the first channel message
// with meta["history"] arrives, the conversation history is formatted and
// injected as a preamble before the user's message.
func WireChannelInjection(mgr *Manager, inject MessageInjector, sessionLen ...SessionLengthFunc) {
	WireChannelInjectionWithReplyHook(mgr, inject, nil, sessionLen...)
}

// WireChannelInjectionWithReplyHook is like WireChannelInjection but accepts
// an optional hook that's called with the server name and message_id when a
// reply-capable channel message arrives.
func WireChannelInjectionWithReplyHook(mgr *Manager, inject MessageInjector, replyHook ChannelReplyHook, sessionLen ...SessionLengthFunc) {
	var getSessionLen SessionLengthFunc
	if len(sessionLen) > 0 {
		getSessionLen = sessionLen[0]
	}
	var historyInjected atomic.Bool

	mgr.SetOnChannelMessage(func(cm ChannelMessage) {
		serverName := cm.ServerName
		source := cm.SourceName()
		meta := formatMeta(cm.Meta)
		ts := time.Now().UnixMilli()
		firlog.Info("injecting channel message", "server", serverName, "source", source)

		// Send typing indicator if the server has a "reply" tool.
		if mgr.HasServerTools(serverName, "reply") {
			if err := SendTypingIndicator(context.Background(), mgr, serverName, cm.Meta); err != nil {
				firlog.Debug("typing indicator failed", "err", err)
			}
			// Notify reply hook with message_id for auto-reply wiring.
			if replyHook != nil {
				if msgID, ok := cm.Meta["message_id"].(string); ok && msgID != "" {
					replyHook(serverName, msgID)
				}
			}
		}

		// On first message, inject conversation history preamble if the
		// session is empty and history is available in meta.
		if !historyInjected.Swap(true) {
			if rawHistory := cm.Meta["history"]; rawHistory != nil {
				empty := getSessionLen == nil || getSessionLen() == 0
				if empty {
					var queryJSON json.RawMessage
					switch v := rawHistory.(type) {
					case json.RawMessage:
						queryJSON = v
					case []byte:
						queryJSON = v
					default:
						// meta["history"] might be pre-parsed; re-marshal it
						queryJSON, _ = json.Marshal(v)
					}
					if preamble, _ := history.FormatPreamble(queryJSON); preamble != "" {
						preambleText := fmt.Sprintf("[Channel message from %s via %s — conversation history]\n%s", source, serverName, preamble)
						inject(preambleText, ts)
						firlog.Info("injected conversation history preamble", "server", serverName)
					}
				}
			}
		}

		text := fmt.Sprintf("[Channel message from %s via %s%s]\n%s", source, serverName, meta, cm.Text())
		inject(text, ts)
	})
}

// formatMeta renders channel metadata as key=value pairs for inclusion in
// the message header. Keys are sorted for deterministic output. The "user"
// key is excluded since it's already in the "from" field. The "history"
// key is excluded since it carries bulk conversation history (handled
// separately above).
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
