package autoreply

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
)

type fakeSubscriber struct {
	mu  sync.Mutex
	fns []func(agent.AgentEvent)
}

func (f *fakeSubscriber) Subscribe(fn func(agent.AgentEvent)) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fns = append(f.fns, fn)
	return func() {}
}

func (f *fakeSubscriber) emit(e agent.AgentEvent) {
	f.mu.Lock()
	fns := append([]func(agent.AgentEvent){}, f.fns...)
	f.mu.Unlock()
	for _, fn := range fns {
		fn(e)
	}
}

type replyLog struct {
	mu    sync.Mutex
	calls []map[string]any
}

func (r *replyLog) replyFunc(_ context.Context, args map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make(map[string]any, len(args))
	for k, v := range args {
		cp[k] = v
	}
	r.calls = append(r.calls, cp)
	return nil
}

func (r *replyLog) waitFor(n int) []map[string]any {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		if len(r.calls) >= n {
			result := append([]map[string]any{}, r.calls...)
			r.mu.Unlock()
			return result
		}
		r.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any{}, r.calls...)
}

func TestAutoReply_TextDelta(t *testing.T) {
	log := &replyLog{}
	sub := &fakeSubscriber{}
	s := New(log.replyFunc)
	s.Wire(sub)
	s.SetMessageID("m-1")

	sub.emit(agent.AgentEvent{
		Type: agent.EventMessageUpdate,
		AssistantMessageEvent: &ai.AssistantMessageEvent{
			Type:  ai.EventTextDelta,
			Delta: "hello world",
		},
	})

	calls := log.waitFor(1)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0]["text"] != "hello world" {
		t.Errorf("text: got %q", calls[0]["text"])
	}
	if calls[0]["message_id"] != "m-1" {
		t.Errorf("message_id: got %v", calls[0]["message_id"])
	}
	if calls[0]["final"] != false {
		t.Errorf("final should be false")
	}
}

func TestAutoReply_ToolExecution(t *testing.T) {
	log := &replyLog{}
	sub := &fakeSubscriber{}
	s := New(log.replyFunc)
	s.Wire(sub)
	s.SetMessageID("m-2")

	sub.emit(agent.AgentEvent{
		Type:     agent.EventToolExecutionStart,
		ToolName: "Bash",
		Args:     map[string]any{"command": "ls -la"},
	})
	sub.emit(agent.AgentEvent{
		Type:     agent.EventToolExecutionEnd,
		ToolName: "Bash",
		Result:   "file1.txt\nfile2.txt",
	})

	calls := log.waitFor(2)
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if got := calls[0]["text"].(string); got == "" {
		t.Errorf("tool start text is empty")
	}
	if got := calls[1]["text"].(string); got == "" {
		t.Errorf("tool end text is empty")
	}
}

func TestAutoReply_SkipsReplyTool(t *testing.T) {
	log := &replyLog{}
	sub := &fakeSubscriber{}
	s := New(log.replyFunc)
	s.Wire(sub)
	s.SetMessageID("m-3")

	sub.emit(agent.AgentEvent{
		Type:     agent.EventToolExecutionStart,
		ToolName: "reply",
	})
	sub.emit(agent.AgentEvent{
		Type:     agent.EventToolExecutionStart,
		ToolName: "mcp__poe__reply",
	})

	// Give time for any erroneous sends
	time.Sleep(50 * time.Millisecond)
	calls := log.waitFor(0)
	if len(calls) != 0 {
		t.Errorf("should skip reply tool events, got %d calls", len(calls))
	}
}

func TestAutoReply_Finalize(t *testing.T) {
	log := &replyLog{}
	sub := &fakeSubscriber{}
	s := New(log.replyFunc)
	s.Wire(sub)
	s.SetMessageID("m-4")

	sub.emit(agent.AgentEvent{
		Type: agent.EventMessageUpdate,
		AssistantMessageEvent: &ai.AssistantMessageEvent{
			Type:  ai.EventTextDelta,
			Delta: "hi",
		},
	})
	sub.emit(agent.AgentEvent{Type: agent.EventAgentEnd})

	calls := log.waitFor(2)
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if calls[1]["final"] != true {
		t.Errorf("last call should be final=true")
	}
}

func TestAutoReply_NoMessageID_Ignored(t *testing.T) {
	log := &replyLog{}
	sub := &fakeSubscriber{}
	s := New(log.replyFunc)
	s.Wire(sub)

	sub.emit(agent.AgentEvent{
		Type: agent.EventMessageUpdate,
		AssistantMessageEvent: &ai.AssistantMessageEvent{
			Type:  ai.EventTextDelta,
			Delta: "ignored",
		},
	})

	time.Sleep(50 * time.Millisecond)
	if len(log.waitFor(0)) != 0 {
		t.Errorf("should ignore events without message_id")
	}
}

func TestAutoReply_DoubleFinalize(t *testing.T) {
	log := &replyLog{}
	sub := &fakeSubscriber{}
	s := New(log.replyFunc)
	s.Wire(sub)
	s.SetMessageID("m-5")

	sub.emit(agent.AgentEvent{
		Type: agent.EventMessageUpdate,
		AssistantMessageEvent: &ai.AssistantMessageEvent{
			Type:  ai.EventTextDelta,
			Delta: "x",
		},
	})
	sub.emit(agent.AgentEvent{Type: agent.EventMessageEnd})
	sub.emit(agent.AgentEvent{Type: agent.EventAgentEnd})

	calls := log.waitFor(2)
	// Allow a moment for potential third (erroneous) call
	time.Sleep(50 * time.Millisecond)
	calls = log.waitFor(2)
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls (no double-final), got %d", len(calls))
	}
}

func TestFormatToolArgs(t *testing.T) {
	if got := formatToolArgs("bash", map[string]any{"command": "echo hi"}); got != "\n$ echo hi" {
		t.Errorf("bash: got %q", got)
	}
	if got := formatToolArgs("read", map[string]any{"path": "/tmp/f"}); got != " /tmp/f" {
		t.Errorf("read: got %q", got)
	}
	if got := formatToolArgs("unknown", nil); got != "" {
		t.Errorf("nil: got %q", got)
	}
}

func TestFormatPlanMarkdown_FirstUpdate(t *testing.T) {
	args := map[string]any{
		"title": "Deploy service",
		"entries": []any{
			map[string]any{"content": "Build binary", "status": "completed", "priority": "high"},
			map[string]any{"content": "Run tests", "status": "in_progress", "priority": "medium"},
			map[string]any{"content": "Push to prod", "status": "pending", "priority": "medium"},
		},
	}
	got := formatPlanMarkdown(args, 1, false)

	// First update should NOT be a blockquote
	if strings.HasPrefix(strings.TrimSpace(got), ">") {
		t.Error("first update should not be a blockquote")
	}
	if !strings.Contains(got, "📋") {
		t.Error("should have plan emoji")
	}
	if !strings.Contains(got, "Deploy service") {
		t.Error("should contain title")
	}
	// Completed entries use strikethrough
	if !strings.Contains(got, "~~Build binary~~") {
		t.Error("completed entry should use strikethrough")
	}
	if !strings.Contains(got, "✓") {
		t.Error("completed entry should have checkmark")
	}
	// In-progress entry is bold with arrow
	if !strings.Contains(got, "→ **Run tests**") {
		t.Error("in-progress entry should be bold with arrow")
	}
	// Pending entry uses circle
	if !strings.Contains(got, "○ Push to prod") {
		t.Error("pending entry should use circle")
	}
	// High priority on completed items doesn't show (it's struck through)
	// Progress bar in backticks
	if !strings.Contains(got, "`") {
		t.Error("should have progress bar in code span")
	}
	if !strings.Contains(got, "1/3") {
		t.Error("should show completion count")
	}
}

func TestFormatPlanMarkdown_SubsequentUpdate_Compact(t *testing.T) {
	args := map[string]any{
		"title": "Deploy service",
		"entries": []any{
			map[string]any{"content": "Build binary", "status": "completed", "priority": "medium"},
			map[string]any{"content": "Run tests", "status": "completed", "priority": "medium"},
			map[string]any{"content": "Push to prod", "status": "in_progress", "priority": "medium"},
		},
	}
	got := formatPlanMarkdown(args, 2, false)

	// Should be a blockquote (compact)
	if !strings.Contains(got, "> ") {
		t.Error("subsequent update should be a blockquote")
	}
	// Should show active item bold with arrow
	if !strings.Contains(got, "→ **Push to prod**") {
		t.Errorf("should show in-progress item with arrow, got: %s", got)
	}
	// Should show completion count
	if !strings.Contains(got, "2/3") {
		t.Error("should show completion count")
	}
	// Should NOT show completed items (compact mode)
	if strings.Contains(got, "Build binary") {
		t.Error("compact mode should not show completed items")
	}
}

func TestFormatPlanMarkdown_EmptyEntries(t *testing.T) {
	args := map[string]any{
		"entries": []any{},
	}
	got := formatPlanMarkdown(args, 1, false)
	if !strings.Contains(got, "Plan cleared") {
		t.Errorf("empty entries should show cleared, got %q", got)
	}
}

func TestFormatPlanMarkdown_Error(t *testing.T) {
	got := formatPlanMarkdown(map[string]any{}, 1, true)
	if !strings.Contains(got, "failed") {
		t.Errorf("error should show failure, got %q", got)
	}
}

func TestFormatPlanMarkdown_Metadata(t *testing.T) {
	args := map[string]any{
		"title": "Test",
		"metadata": map[string]any{
			"worktree":       "/tmp/wt",
			"next_update_in": "3", // should be hidden
		},
		"entries": []any{
			map[string]any{"content": "Step 1", "status": "pending", "priority": "medium"},
		},
	}
	got := formatPlanMarkdown(args, 1, false)
	if !strings.Contains(got, "worktree") {
		t.Error("should show worktree metadata")
	}
	if strings.Contains(got, "next_update_in") {
		t.Error("should hide next_update_in metadata")
	}
	// Metadata should be italic
	if !strings.Contains(got, "*worktree:") {
		t.Error("metadata should be italic")
	}
}

func TestAutoReply_PlanTool_RichRendering(t *testing.T) {
	log := &replyLog{}
	sub := &fakeSubscriber{}
	s := New(log.replyFunc)
	s.Wire(sub)
	s.SetMessageID("m-plan")

	planArgs := map[string]any{
		"title": "My Plan",
		"entries": []any{
			map[string]any{"content": "Step A", "status": "completed", "priority": "medium"},
			map[string]any{"content": "Step B", "status": "in_progress", "priority": "high"},
		},
	}

	// Plan tool start — should NOT produce a code block
	sub.emit(agent.AgentEvent{
		Type:     agent.EventToolExecutionStart,
		ToolName: "plan",
		Args:     planArgs,
	})

	// Plan tool end
	sub.emit(agent.AgentEvent{
		Type:     agent.EventToolExecutionEnd,
		ToolName: "plan",
		Result:   "Plan updated (2 entries).",
	})

	calls := log.waitFor(1)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call (plan render only), got %d", len(calls))
	}

	text := calls[0]["text"].(string)
	// Should render rich markdown, not the generic tool block
	if strings.Contains(text, "```") {
		t.Error("plan should not render as code block")
	}
	// Completed: strikethrough with checkmark
	if !strings.Contains(text, "~~Step A~~") {
		t.Errorf("should contain completed step with strikethrough, got: %s", text)
	}
	// In-progress: bold with arrow
	if !strings.Contains(text, "→ **Step B**") {
		t.Errorf("should contain in_progress step bold with arrow, got: %s", text)
	}
	// First update — should NOT be a blockquote
	if strings.HasPrefix(strings.TrimSpace(text), ">") {
		t.Error("first plan update should not be a blockquote")
	}
}

func TestAutoReply_PlanTool_SecondUpdate_Collapsible(t *testing.T) {
	log := &replyLog{}
	sub := &fakeSubscriber{}
	s := New(log.replyFunc)
	s.Wire(sub)
	s.SetMessageID("m-plan2")

	planArgs := map[string]any{
		"title": "Plan",
		"entries": []any{
			map[string]any{"content": "Step", "status": "pending", "priority": "medium"},
		},
	}

	// First update
	sub.emit(agent.AgentEvent{Type: agent.EventToolExecutionStart, ToolName: "plan", Args: planArgs})
	sub.emit(agent.AgentEvent{Type: agent.EventToolExecutionEnd, ToolName: "plan", Result: "ok"})

	// Second update
	sub.emit(agent.AgentEvent{Type: agent.EventToolExecutionStart, ToolName: "plan", Args: planArgs})
	sub.emit(agent.AgentEvent{Type: agent.EventToolExecutionEnd, ToolName: "plan", Result: "ok"})

	calls := log.waitFor(2)
	if len(calls) < 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}

	// Second call should be compact (blockquote, not collapsible)
	text := calls[1]["text"].(string)
	if strings.Contains(text, "<details>") {
		t.Error("should not use <details>")
	}
	if !strings.Contains(text, "> ") {
		t.Error("second plan update should be a compact blockquote")
	}
}
