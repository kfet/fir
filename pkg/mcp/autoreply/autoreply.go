// Package autoreply wires agent session events to automatic Poe reply
// streaming. When active, LLM text deltas and tool call info are forwarded
// to the Poe bridge via the reply tool — the LLM never needs to call
// reply() manually.
package autoreply

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	firlog "github.com/kfet/fir/pkg/log"
)

// ReplyFunc calls the reply tool on the bridge server.
type ReplyFunc func(ctx context.Context, args map[string]any) error

// EventSubscriber allows subscribing to agent events without importing session.
type EventSubscriber interface {
	Subscribe(fn func(agent.AgentEvent)) func()
}

// State tracks the auto-reply stream for one Poe message.
type State struct {
	mu        sync.Mutex
	reply     ReplyFunc
	messageID string
	started   bool
	closed    bool
	sendCh    chan sendReq
}

type sendReq struct {
	args map[string]any
}

// New creates an auto-reply state bound to the given reply function.
func New(reply ReplyFunc) *State {
	s := &State{
		reply:  reply,
		sendCh: make(chan sendReq, 64),
	}
	go s.sendLoop()
	return s
}

func (s *State) sendLoop() {
	for req := range s.sendCh {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := s.reply(ctx, req.args); err != nil {
			firlog.Debug("auto-reply chunk failed", "err", err)
		}
		cancel()
	}
}

// IsActive returns true if auto-reply is currently streaming for a message.
func (s *State) IsActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.messageID != "" && !s.closed
}

// InterceptReply checks if a manual reply() call should be absorbed.
// Returns true if the call was handled (absorbed or forwarded for replace/error).
// Returns false if auto-reply is not active and the call should proceed normally.
func (s *State) InterceptReply(ctx context.Context, args map[string]any) (bool, error) {
	s.mu.Lock()
	active := s.messageID != "" && !s.closed
	s.mu.Unlock()

	if !active {
		return false, nil // not active, let the call through
	}

	// Allow replace and error calls through — those are intentional overrides
	if replace, _ := args["replace"].(bool); replace {
		return false, nil
	}
	if isErr, _ := args["error"].(bool); isErr {
		return false, nil
	}

	// Absorb normal text/final calls — auto-reply handles these
	firlog.Debug("auto-reply intercepted manual reply()", "message_id", args["message_id"])
	return true, nil
}

// SetMessageID sets the Poe message_id for the current reply stream.
func (s *State) SetMessageID(msgID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messageID = msgID
	s.started = false
	s.closed = false
}

// Wire subscribes to agent events and streams them to Poe.
// Returns an unsubscribe function.
func (s *State) Wire(sub EventSubscriber) func() {
	return sub.Subscribe(func(ae agent.AgentEvent) {
		switch ae.Type {
		case agent.EventMessageUpdate:
			if ae.AssistantMessageEvent != nil && ae.AssistantMessageEvent.Type == ai.EventTextDelta {
				s.sendChunk(ae.AssistantMessageEvent.Delta, false, false)
			}

		case agent.EventToolExecutionStart:
			if ae.ToolName != "" && ae.ToolName != "reply" && ae.ToolName != "mcp__poe__reply" {
				argStr := formatToolArgs(ae.ToolName, ae.Args)
				text := fmt.Sprintf("\n\n⚙️ `%s%s`\n", ae.ToolName, argStr)
				s.sendChunk(text, false, false)
			}

		case agent.EventToolExecutionEnd:
			if ae.ToolName != "" && ae.ToolName != "reply" && ae.ToolName != "mcp__poe__reply" {
				text := formatToolResult(ae.Result, ae.IsError)
				if text != "" {
					s.sendChunk(text, false, false)
				}
			}

		case agent.EventMessageEnd:
			s.finalize()

		case agent.EventAgentEnd:
			s.finalize()
		}
	})
}

func (s *State) sendChunk(text string, final bool, replace bool) {
	s.mu.Lock()
	if s.messageID == "" || s.closed {
		s.mu.Unlock()
		return
	}
	msgID := s.messageID
	s.started = true
	if final {
		s.closed = true
	}
	s.mu.Unlock()

	args := map[string]any{
		"message_id": msgID,
		"text":       text,
		"final":      final,
	}
	if replace {
		args["replace"] = true
	}

	// Non-blocking send to the ordered queue. Drop if full (backpressure).
	select {
	case s.sendCh <- sendReq{args: args}:
	default:
		firlog.Debug("auto-reply send queue full, dropping chunk")
	}
}

func (s *State) finalize() {
	s.mu.Lock()
	if s.closed || s.messageID == "" {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.sendChunk("", true, false)
}

func formatToolArgs(toolName string, args any) string {
	if args == nil {
		return ""
	}
	m, ok := args.(map[string]any)
	if !ok {
		return ""
	}
	switch {
	case strings.EqualFold(toolName, "bash"):
		if cmd, ok := m["command"].(string); ok {
			if len(cmd) > 80 {
				cmd = cmd[:77] + "..."
			}
			return ": " + cmd
		}
	case strings.EqualFold(toolName, "read"):
		if p, ok := m["path"].(string); ok {
			return ": " + p
		}
	case strings.EqualFold(toolName, "write") || strings.EqualFold(toolName, "edit"):
		if p, ok := m["path"].(string); ok {
			return ": " + p
		}
	}
	return ""
}

func formatToolResult(result any, isError bool) string {
	if result == nil {
		if isError {
			return "```\n❌ error\n```\n"
		}
		return ""
	}

	var text string
	switch v := result.(type) {
	case string:
		text = v
	case map[string]any:
		if s, ok := v["output"].(string); ok {
			text = s
		} else if s, ok := v["text"].(string); ok {
			text = s
		} else if s, ok := v["content"].(string); ok {
			text = s
		}
	}

	if text == "" {
		return ""
	}

	lines := strings.Split(text, "\n")
	if len(lines) > 20 {
		text = strings.Join(lines[:20], "\n") + fmt.Sprintf("\n... (%d more lines)", len(lines)-20)
	}
	if len(text) > 2000 {
		text = text[:1997] + "..."
	}

	prefix := ""
	if isError {
		prefix = "❌ "
	}
	return fmt.Sprintf("```\n%s%s\n```\n", prefix, text)
}
