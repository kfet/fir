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
	mu         sync.Mutex
	reply      ReplyFunc
	messageID  string
	started    bool
	closed     bool
	sendCh     chan sendReq // never closed; lives for the lifetime of State
	inThinking bool        // currently inside a thinking block

	// Plan rendering: track plan tool args so we can render rich markdown
	// instead of the generic "Plan updated" text.
	planArgs        map[string]any // captured at ToolExecutionStart
	planUpdateCount int            // how many plan updates this message
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
	// Channel is never closed — this goroutine lives as long as the State.
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
	s.inThinking = false
	s.planArgs = nil
	s.planUpdateCount = 0
}

// Wire subscribes to agent events and streams them to Poe.
// Returns an unsubscribe function.
func (s *State) Wire(sub EventSubscriber) func() {
	firlog.Info("auto-reply: Wire() subscribing to agent events")
	return sub.Subscribe(func(ae agent.AgentEvent) {
		firlog.Debug("auto-reply: event received", "type", ae.Type)
		switch ae.Type {
		case agent.EventMessageUpdate:
			if ae.AssistantMessageEvent == nil {
				return
			}
			switch ae.AssistantMessageEvent.Type {
			case ai.EventTextDelta:
				s.endThinkingIfNeeded()
				s.sendChunk(ae.AssistantMessageEvent.Delta, false, false)
			case ai.EventThinkingStart:
				s.mu.Lock()
				s.inThinking = true
				s.mu.Unlock()
				s.sendChunk("\n\n*Thinking...*\n> *", false, false)
			case ai.EventThinkingDelta:
				delta := ae.AssistantMessageEvent.Delta
				// In blockquote, newlines need "> " prefix to continue the quote
				delta = strings.ReplaceAll(delta, "\n", "*\n> *")
				s.sendChunk(delta, false, false)
			case ai.EventThinkingEnd:
				s.endThinkingIfNeeded()
			}

		case agent.EventToolExecutionStart:
			if ae.ToolName != "" && ae.ToolName != "reply" && ae.ToolName != "mcp__poe__reply" {
				// Capture plan args for rich rendering; skip the generic code block.
				if isPlanTool(ae.ToolName) {
					s.mu.Lock()
					if m, ok := ae.Args.(map[string]any); ok {
						s.planArgs = m
					}
					s.mu.Unlock()
				} else {
					lang := toolLang(ae.ToolName)
					argStr := formatToolArgs(ae.ToolName, ae.Args)
					text := fmt.Sprintf("\n\n```%s\n%s%s\n```\n", lang, ae.ToolName, argStr)
					s.sendChunk(text, false, false)
				}
			}

		case agent.EventToolExecutionEnd:
			if ae.ToolName != "" && ae.ToolName != "reply" && ae.ToolName != "mcp__poe__reply" {
				if isPlanTool(ae.ToolName) {
					s.mu.Lock()
					args := s.planArgs
					s.planArgs = nil
					s.planUpdateCount++
					count := s.planUpdateCount
					s.mu.Unlock()
					if args != nil {
						text := formatPlanMarkdown(args, count, ae.IsError)
						s.sendChunk(text, false, false)
					}
				} else {
					text := formatToolResult(ae.Result, ae.IsError)
					if text != "" {
						s.sendChunk(text, false, false)
					}
				}
			}

		case agent.EventMessageEnd:
			// Don't finalize here — message_end fires for each content block,
			// including internal/system blocks before the assistant response.
			// Only finalize on agent_end (true end of turn).

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
	s.closed = true
	msgID := s.messageID
	s.mu.Unlock()

	// Send the final empty chunk. Channel is never closed — it's reused
	// across messages. The closed flag prevents further sends.
	select {
	case s.sendCh <- sendReq{args: map[string]any{
		"message_id": msgID,
		"text":       "",
		"final":      true,
	}}:
	default:
		firlog.Debug("auto-reply finalize: send queue full")
	}
}

func (s *State) endThinkingIfNeeded() {
	s.mu.Lock()
	was := s.inThinking
	s.inThinking = false
	s.mu.Unlock()
	if was {
		s.sendChunk("*\n\n", false, false)
	}
}

// toolLang returns a markdown code-fence language hint for a tool name.
func toolLang(name string) string {
	lower := strings.ToLower(name)
	switch {
	case lower == "bash":
		return "bash"
	case lower == "read" || lower == "write" || lower == "edit":
		return "text"
	case strings.HasPrefix(lower, "mcp__"):
		return "tool"
	default:
		return "tool"
	}
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
			if len(cmd) > 120 {
				cmd = cmd[:117] + "..."
			}
			return "\n$ " + cmd
		}
	case strings.EqualFold(toolName, "read"):
		if p, ok := m["path"].(string); ok {
			return " " + p
		}
	case strings.EqualFold(toolName, "write") || strings.EqualFold(toolName, "edit"):
		if p, ok := m["path"].(string); ok {
			return " " + p
		}
	}
	return ""
}

// collapsibleThreshold is the line count above which tool output is wrapped
// in a <details> block so it doesn't dominate the chat.
const collapsibleThreshold = 8

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
	totalLines := len(lines)
	truncated := false
	if totalLines > 40 {
		text = strings.Join(lines[:40], "\n") + fmt.Sprintf("\n... (%d more lines)", totalLines-40)
		lines = lines[:40]
		truncated = true
	}
	if len(text) > 3000 {
		text = text[:2997] + "..."
		truncated = true
	}

	prefix := ""
	if isError {
		prefix = "❌ "
	}

	codeBlock := fmt.Sprintf("```\n%s%s\n```\n", prefix, text)

	// Wrap long output in a collapsible <details> block.
	if len(lines) > collapsibleThreshold && !isError {
		summary := fmt.Sprintf("output (%d lines", totalLines)
		if truncated {
			summary += ", truncated"
		}
		summary += ")"
		return fmt.Sprintf("<details>\n<summary>%s</summary>\n\n%s\n</details>\n", summary, codeBlock)
	}

	return codeBlock
}

// isPlanTool returns true if the tool name is the plan tool.
func isPlanTool(name string) bool {
	return name == "plan"
}

// formatPlanMarkdown renders plan tool args as rich markdown.
// For the first plan update in a message, it renders visibly.
// For subsequent updates (count > 1), it wraps in a <details> block
// so the chat doesn't get cluttered with repeated plan snapshots.
func formatPlanMarkdown(args map[string]any, updateCount int, isError bool) string {
	if isError {
		return "\n\n> ⚠️ Plan update failed\n\n"
	}

	title, _ := args["title"].(string)
	metadata, _ := args["metadata"].(map[string]any)
	rawEntries, _ := args["entries"].([]any)

	type entry struct {
		content  string
		status   string
		priority string
	}

	entries := make([]entry, 0, len(rawEntries))
	for _, raw := range rawEntries {
		obj, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		content, _ := obj["content"].(string)
		status, _ := obj["status"].(string)
		priority, _ := obj["priority"].(string)
		if content != "" {
			entries = append(entries, entry{content, status, priority})
		}
	}

	if len(entries) == 0 {
		return "\n\n> 📋 Plan cleared\n\n"
	}

	// Count by status
	completed, inProgress, pending := 0, 0, 0
	for _, e := range entries {
		switch e.status {
		case "completed":
			completed++
		case "in_progress":
			inProgress++
		default:
			pending++
		}
	}

	// Build header
	var b strings.Builder
	b.WriteString("\n\n")

	header := "📋"
	if title != "" {
		header += " **" + title + "**"
	}
	header += fmt.Sprintf(" — %d/%d", completed, len(entries))
	if inProgress > 0 {
		header += fmt.Sprintf(" · %d 🔄", inProgress)
	}
	if pending > 0 {
		header += fmt.Sprintf(" · %d ⬜", pending)
	}

	// Render metadata if present (sorted for stable output)
	var metaLines string
	if len(metadata) > 0 {
		keys := make([]string, 0, len(metadata))
		for k := range metadata {
			if k == "next_update_in" {
				continue // internal, don't show to user
			}
			keys = append(keys, k)
		}
		// Simple insertion sort for small maps (typically 3-5 keys).
		for i := 1; i < len(keys); i++ {
			for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
				keys[j], keys[j-1] = keys[j-1], keys[j]
			}
		}
		var mb strings.Builder
		for _, k := range keys {
			s, _ := metadata[k].(string)
			if s != "" {
				mb.WriteString(fmt.Sprintf("> *%s: %s*\n", k, s))
			}
		}
		metaLines = mb.String()
	}

	// Render entries
	var eb strings.Builder
	for _, e := range entries {
		icon := "⬜"
		switch e.status {
		case "completed":
			icon = "✅"
		case "in_progress":
			icon = "🔄"
		}
		priorityMark := ""
		if e.priority == "high" {
			priorityMark = " ❗"
		}
		eb.WriteString(fmt.Sprintf("%s %s%s\n", icon, e.content, priorityMark))
	}
	entryBlock := eb.String()

	// First update: render visibly. Subsequent: collapsible.
	if updateCount <= 1 {
		b.WriteString(header + "\n")
		if metaLines != "" {
			b.WriteString(metaLines)
		}
		b.WriteString(entryBlock)
	} else {
		summary := fmt.Sprintf("📋 Plan updated (%d/%d done)", completed, len(entries))
		b.WriteString("<details>\n<summary>")
		b.WriteString(summary)
		b.WriteString("</summary>\n\n")
		b.WriteString(header + "\n")
		if metaLines != "" {
			b.WriteString(metaLines)
		}
		b.WriteString(entryBlock)
		b.WriteString("\n</details>")
	}

	b.WriteString("\n")
	return b.String()
}
