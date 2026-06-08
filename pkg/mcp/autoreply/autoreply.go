// Package autoreply wires agent session events to automatic channel reply
// streaming. When active, LLM text deltas and tool call info are forwarded
// through a message_id-addressed reply tool — the LLM never needs to call
// reply() manually.
package autoreply

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"


	"github.com/kfet/fir/pkg/ai"
	firlog "github.com/kfet/fir/pkg/log"
)

// ReplyFunc calls the reply tool on the bridge server.
type ReplyFunc func(ctx context.Context, args map[string]any) error

// EventSubscriber allows subscribing to agent events without importing session.
type EventSubscriber interface {
	Subscribe(fn func(agent.AgentEvent)) func()
}

// State tracks the auto-reply stream for one channel message.
type State struct {
	mu         sync.Mutex
	reply      ReplyFunc
	messageID  string
	started    bool
	closed     bool
	sendCh     chan sendReq // never closed; lives for the lifetime of State
	inThinking bool         // currently inside a thinking block

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
			firlog.Trace("auto-reply chunk failed", "err", err)
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

// SetMessageID sets the channel message_id for the current reply stream.
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

// Wire subscribes to agent events and streams them to the reply tool.
// Returns an unsubscribe function.
func (s *State) Wire(sub EventSubscriber) func() {
	firlog.Info("auto-reply: Wire() subscribing to agent events")
	return sub.Subscribe(func(ae agent.AgentEvent) {
		firlog.Trace("auto-reply: event received", "type", ae.Type)
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
			if ae.ToolName != "" && !isReplyTool(ae.ToolName) {
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
			if ae.ToolName != "" && !isReplyTool(ae.ToolName) {
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

// isReplyTool reports whether name is the direct reply tool or an MCP reply
// tool imported from any server (mcp__<server>__reply).
func isReplyTool(name string) bool {
	return name == "reply" || strings.HasPrefix(name, "mcp__") && strings.HasSuffix(name, "__reply")
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
		firlog.Trace("auto-reply send queue full, dropping chunk")
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
		firlog.Trace("auto-reply finalize: send queue full")
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

// truncateThreshold is the line count above which tool output is
// truncated to keep channel messages readable. Markdown details support is
// inconsistent across channels, so we truncate and show a line count instead.
const truncateThreshold = 8

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

	// For long output, show just a summary + first/last few lines
	// to keep the chat readable without relying on HTML collapsibles.
	if len(lines) > truncateThreshold && !isError {
		head := strings.Join(lines[:3], "\n")
		tail := strings.Join(lines[len(lines)-2:], "\n")
		note := fmt.Sprintf("(%d lines", totalLines)
		if truncated {
			note += ", truncated"
		}
		note += ")"
		return fmt.Sprintf("```\n%s\n...\n%s\n```\n*%s*\n", head, tail, note)
	}

	return fmt.Sprintf("```\n%s%s\n```\n", prefix, text)
}

// isPlanTool returns true if the tool name is the plan tool.
func isPlanTool(name string) bool {
	return name == "plan"
}

// formatPlanMarkdown renders plan tool args as rich markdown.
// First update: full elegant plan with progress bar and all entries.
// Subsequent updates: compact blockquote with
// just the progress bar and active items.
func formatPlanMarkdown(args map[string]any, updateCount int, isError bool) string {
	if isError {
		return "\n\n> ⚠️ Plan update failed\n\n"
	}

	title, _ := args["title"].(string)
	metadata, _ := args["metadata"].(map[string]any)
	// Decode the (possibly compressed) wire form to canonical full names so
	// both old (content/full-enum) and new (c/short-code) transcripts render.
	rawEntries, _ := tools.DecodePlanParams(args)["entries"].([]any)

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

	bar := planProgressBar(completed, inProgress, len(entries))
	pct := completed * 100 / len(entries)

	var b strings.Builder
	b.WriteString("\n\n")

	if updateCount <= 1 {
		// ── Full plan ──────────────────────────────
		//
		// 📋 **Deploy Service**
		// `▓▓▓▓▓▓░░░░` 3/5 complete
		//
		//   ✓ ~~Build binary~~
		//   ✓ ~~Run unit tests~~
		//   ✓ ~~Push to staging~~
		//   → **Integration tests** ❗
		//   ○ Push to production

		// Title
		if title != "" {
			b.WriteString("📋 **" + title + "**\n")
		} else {
			b.WriteString("📋 **Plan**\n")
		}

		// Progress bar
		b.WriteString(fmt.Sprintf("`%s` %d/%d complete", bar, completed, len(entries)))
		if pct > 0 && pct < 100 {
			b.WriteString(fmt.Sprintf(" (%d%%)", pct))
		}
		b.WriteString("\n")

		// Metadata
		if len(metadata) > 0 {
			for _, k := range sortedMetaKeys(metadata) {
				s, _ := metadata[k].(string)
				if s != "" {
					b.WriteString(fmt.Sprintf("*%s: %s*\n", k, s))
				}
			}
		}

		b.WriteString("\n")

		// Entries
		for _, e := range entries {
			b.WriteString(formatPlanEntry(e.content, e.status, e.priority))
		}
	} else {
		// ── Compact blockquote ─────────────────────
		//
		// > 📋 **Deploy Service** `▓▓▓▓▓▓░░░░` 3/5
		// > → **Integration tests**

		if title != "" {
			b.WriteString(fmt.Sprintf("> 📋 **%s** `%s` %d/%d", title, bar, completed, len(entries)))
		} else {
			b.WriteString(fmt.Sprintf("> 📋 `%s` %d/%d", bar, completed, len(entries)))
		}

		// Show only in-progress items
		for _, e := range entries {
			if e.status == "in_progress" {
				priorityMark := ""
				if e.priority == "high" {
					priorityMark = " ❗"
				}
				b.WriteString(fmt.Sprintf("\n> → **%s**%s", e.content, priorityMark))
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	return b.String()
}

// formatPlanEntry renders a single plan entry line.
func formatPlanEntry(content, status, priority string) string {
	priorityMark := ""
	if priority == "high" {
		priorityMark = " ❗"
	}
	switch status {
	case "completed":
		return fmt.Sprintf("  ✓ ~~%s~~\n", content)
	case "in_progress":
		return fmt.Sprintf("  → **%s**%s\n", content, priorityMark)
	default:
		return fmt.Sprintf("  ○ %s%s\n", content, priorityMark)
	}
}

// sortedMetaKeys returns metadata keys in sorted order, excluding internal keys.
func sortedMetaKeys(metadata map[string]any) []string {
	keys := make([]string, 0, len(metadata))
	for k := range metadata {
		if k == "next_update_in" {
			continue
		}
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// planProgressBar renders a text progress bar like "████░░░░" using
// Unicode block characters. Works in any markdown renderer.
func planProgressBar(completed, inProgress, total int) string {
	if total == 0 {
		return ""
	}
	const barLen = 10
	filledLen := completed * barLen / total
	// Ensure at least 1 block shown when there's any progress.
	if completed > 0 && filledLen == 0 {
		filledLen = 1
	}
	activeLen := inProgress * barLen / total
	if inProgress > 0 && activeLen == 0 {
		activeLen = 1
	}
	if filledLen+activeLen > barLen {
		activeLen = barLen - filledLen
	}
	emptyLen := barLen - filledLen - activeLen

	var b strings.Builder
	for range filledLen {
		b.WriteRune('█')
	}
	for range activeLen {
		b.WriteRune('▓')
	}
	for range emptyLen {
		b.WriteRune('░')
	}
	return b.String()
}
