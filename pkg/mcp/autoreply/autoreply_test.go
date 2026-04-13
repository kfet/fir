package autoreply

import (
	"context"
	"sync"
	"testing"

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

func (r *replyLog) get() []map[string]any {
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

	calls := log.get()
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

	calls := log.get()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if got := calls[0]["text"].(string); got == "" || got == "hello" {
		t.Errorf("tool start text unexpected: %q", got)
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

	if len(log.get()) != 0 {
		t.Errorf("should skip reply tool events, got %d calls", len(log.get()))
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
	sub.emit(agent.AgentEvent{Type: agent.EventMessageEnd})

	calls := log.get()
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
	// Don't set message ID

	sub.emit(agent.AgentEvent{
		Type: agent.EventMessageUpdate,
		AssistantMessageEvent: &ai.AssistantMessageEvent{
			Type:  ai.EventTextDelta,
			Delta: "ignored",
		},
	})

	if len(log.get()) != 0 {
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

	calls := log.get()
	// Should be 2: text + final. AgentEnd should not send another final.
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls (no double-final), got %d", len(calls))
	}
}

func TestFormatToolArgs(t *testing.T) {
	if got := formatToolArgs("bash", map[string]any{"command": "echo hi"}); got != ": echo hi" {
		t.Errorf("bash: got %q", got)
	}
	if got := formatToolArgs("read", map[string]any{"path": "/tmp/f"}); got != ": /tmp/f" {
		t.Errorf("read: got %q", got)
	}
	if got := formatToolArgs("unknown", nil); got != "" {
		t.Errorf("nil: got %q", got)
	}
}
