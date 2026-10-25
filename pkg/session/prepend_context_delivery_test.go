package session

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kfet/agent"
	"github.com/kfet/ai"
)

// recordingStream answers request i with reply(i) and records every request.
// When onRequest is set it runs before request i is answered.
type recordingStream struct {
	mu        sync.Mutex
	calls     []ai.Context
	reply     func(i int) *ai.AssistantMessage
	onRequest func(i int)
}

func (r *recordingStream) fn(_ *ai.Model, c ai.Context, _ *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	r.mu.Lock()
	r.calls = append(r.calls, c)
	i := len(r.calls) - 1
	r.mu.Unlock()
	if r.onRequest != nil {
		r.onRequest(i)
	}
	msg := r.reply(i)
	stream := ai.NewAssistantMessageEventStream()
	go func() {
		stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: msg})
		stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Message: msg})
		stream.End(nil)
	}()
	return stream
}

func (r *recordingStream) requests() []ai.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ai.Context(nil), r.calls...)
}

func pcText(text string) *ai.AssistantMessage {
	return &ai.AssistantMessage{
		Role: ai.RoleAssistant, Provider: "test-provider", Model: "test-model",
		StopReason: ai.StopReasonStop, Timestamp: time.Now().UnixMilli(),
		Content: []ai.AssistantContent{{Text: &ai.TextContent{Type: "text", Text: text}}},
	}
}

func pcToolCall() *ai.AssistantMessage {
	return &ai.AssistantMessage{
		Role: ai.RoleAssistant, Provider: "test-provider", Model: "test-model",
		StopReason: ai.StopReasonToolUse, Timestamp: time.Now().UnixMilli(),
		Content: []ai.AssistantContent{{ToolCall: &ai.ToolCall{Type: "toolCall", ID: "c1", Name: "work", Arguments: map[string]any{}}}},
	}
}

// userIndex returns the index of the user message containing needle, or -1.
func userIndex(c ai.Context, needle string) int {
	for i, m := range c.Messages {
		if u := m.AsUser(); u != nil {
			if txt, ok := u.Content.(string); ok && strings.Contains(txt, needle) {
				return i
			}
		}
	}
	return -1
}

func toolResultsMention(c ai.Context, needle string) bool {
	for _, m := range c.Messages {
		if tr := m.AsToolResult(); tr != nil {
			for _, b := range tr.Content {
				if strings.Contains(b.Text, needle) {
					return true
				}
			}
		}
	}
	return false
}

func countPersistedUser(s *AgentSession, needle string) int {
	n := 0
	for _, m := range s.SessionStore.BuildSessionContext().Messages {
		if u := m.AsUser(); u != nil {
			if txt, ok := u.Content.(string); ok && strings.Contains(txt, needle) {
				n++
			}
		}
	}
	return n
}

func addWorkTool(s *AgentSession, exec func()) {
	s.Agent.UpdateTools(func(ts *agent.ToolSet) {
		ts.Add(agent.AgentTool{
			Tool: ai.Tool{Name: "work", Description: "work"},
			Execute: func(context.Context, string, map[string]any, agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
				exec()
				return agent.AgentToolResult{Content: []ai.ToolResultContent{{Type: "text", Text: "tool-output"}}}, nil
			},
		})
	})
}

// Context prepended while the run executes tools reaches the model as its own
// user message right before the next request; no tool result carries it.
func TestPrependContext_MidRun_UserMessageBeforeNextRequest(t *testing.T) {
	s := newTestAgentSessionWithModel(t, nil)
	defer s.Close()
	addWorkTool(s, func() { s.PrependContext("mid-run-notes") })
	r := &recordingStream{reply: func(i int) *ai.AssistantMessage {
		if i == 0 {
			return pcToolCall()
		}
		return pcText("ok")
	}}
	s.Agent.SetStreamFn(r.fn)

	if err := s.Prompt("task"); err != nil {
		t.Fatal(err)
	}
	s.Agent.WaitForIdle()

	calls := r.requests()
	if len(calls) != 2 {
		t.Fatalf("requests = %d, want 2", len(calls))
	}
	idx := userIndex(calls[1], "[SYS_EXT]\nmid-run-notes")
	if idx < 0 {
		t.Fatal("second request lacks the prepended context as a user message")
	}
	if prev := calls[1].Messages[idx-1]; prev.AsToolResult() == nil {
		t.Fatalf("context should follow the tool result, got role %q", prev.Role())
	}
	if toolResultsMention(calls[1], "mid-run-notes") {
		t.Fatal("tool result was mutated to carry the context")
	}
	if got := countPersistedUser(s, "mid-run-notes"); got != 1 {
		t.Fatalf("persisted %d times, want 1", got)
	}
}

// Context prepended while the model gives its final answer must not force a
// turn; it is appended as a normal message, persisted once, and seen next run.
func TestPrependContext_DuringFinalAnswer_NoExtraTurn(t *testing.T) {
	s := newTestAgentSessionWithModel(t, nil)
	defer s.Close()
	r := &recordingStream{reply: func(int) *ai.AssistantMessage { return pcText("ok") }}
	r.onRequest = func(i int) {
		if i == 0 {
			s.PrependContext("late-notes")
		}
	}
	s.Agent.SetStreamFn(r.fn)

	if err := s.Prompt("task"); err != nil {
		t.Fatal(err)
	}
	s.Agent.WaitForIdle()
	if n := len(r.requests()); n != 1 {
		t.Fatalf("requests = %d, want 1 (no forced turn)", n)
	}
	if got := countPersistedUser(s, "late-notes"); got != 1 {
		t.Fatalf("persisted %d times, want 1", got)
	}

	if err := s.Prompt("next"); err != nil {
		t.Fatal(err)
	}
	s.Agent.WaitForIdle()
	calls := r.requests()
	if len(calls) != 2 || userIndex(calls[1], "late-notes") < 0 {
		t.Fatalf("next run's request lacks the context (requests=%d)", len(calls))
	}
}

// Re-running an injector after a reload (the transcript replayed into a fresh
// agent) finds the persisted message and adds nothing.
func TestPrependContext_RerunAfterReload_AddsNothing(t *testing.T) {
	s := newTestAgentSessionWithModel(t, nil)
	defer s.Close()
	addWorkTool(s, func() { s.PrependContext("start-notes") })
	r := &recordingStream{reply: func(i int) *ai.AssistantMessage {
		if i == 0 {
			return pcToolCall()
		}
		return pcText("ok")
	}}
	s.Agent.SetStreamFn(r.fn)
	if err := s.Prompt("task"); err != nil {
		t.Fatal(err)
	}
	s.Agent.WaitForIdle()

	s.Agent.ReplaceMessages(s.SessionStore.BuildSessionContext().Messages)
	before := len(s.Agent.State().Messages)
	s.PrependContext("start-notes")
	if got := len(s.Agent.State().Messages); got != before {
		t.Fatalf("re-run added %d messages, want 0", got-before)
	}
	if got := countPersistedUser(s, "start-notes"); got != 1 {
		t.Fatalf("persisted %d times, want 1", got)
	}
}

// Duplicate PrependContexts racing the end of a run: the message lands in history and in
// the transcript exactly once, whichever side wins.
func TestPrependContext_RaceWithAgentEnd(t *testing.T) {
	for i := 0; i < 30; i++ {
		s := newTestAgentSessionWithModel(t, nil)
		r := &recordingStream{reply: func(int) *ai.AssistantMessage { return pcText("ok") }}
		s.Agent.SetStreamFn(r.fn)
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = s.Prompt("task")
		}()
		var wg sync.WaitGroup
		for j := 0; j < 3; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.PrependContext("racy-notes")
			}()
		}
		wg.Wait()
		<-done
		s.Agent.WaitForIdle()
		inState := 0
		for _, m := range s.Agent.State().Messages {
			if u := m.AsUser(); u != nil {
				if txt, _ := u.Content.(string); strings.Contains(txt, "racy-notes") {
					inState++
				}
			}
		}
		if inState != 1 {
			t.Fatalf("iteration %d: in state %d times, want 1", i, inState)
		}
		if got := countPersistedUser(s, "racy-notes"); got != 1 {
			t.Fatalf("iteration %d: persisted %d times, want 1", i, got)
		}
		s.Close()
	}
}

// A prompt queued before session_start finished (fir -p, ACP session/prompt, a
// wt brief) waits on the extension-ready gate, so context a session_start
// handler prepends is in that run's first request.
func TestPrependContext_BeforeExtReady_InFirstRequest(t *testing.T) {
	s := newTestAgentSessionWithModel(t, nil)
	defer s.Close()
	ready := make(chan struct{})
	s.extReady = ready
	r := &recordingStream{reply: func(int) *ai.AssistantMessage { return pcText("ok") }}
	s.Agent.SetStreamFn(r.fn)

	errc := make(chan error, 1)
	go func() { errc <- s.Prompt("task") }()
	s.PrependContext("start-notes")
	close(ready)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	s.Agent.WaitForIdle()
	calls := r.requests()
	if len(calls) != 1 || userIndex(calls[0], "start-notes") < 0 {
		t.Fatalf("first request lacks session_start context (requests=%d)", len(calls))
	}
}
