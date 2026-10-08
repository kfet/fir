package session

import (
	"testing"
	"time"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
)

func userMsg(text string) agent.AgentMessage {
	return agent.NewAgentMessage(ai.NewUserMsg(text, time.Now().UnixMilli()))
}

// replyOK makes every LLM call answer "ok" immediately.
func replyOK(_ *ai.Model, _ ai.Context, _ *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	stream := ai.NewAssistantMessageEventStream()
	go func() {
		msg := &ai.AssistantMessage{
			Role: ai.RoleAssistant, Provider: "test-provider", Model: "test-model",
			StopReason: ai.StopReasonStop, Timestamp: time.Now().UnixMilli(),
			Content: []ai.AssistantContent{{Text: &ai.TextContent{Type: "text", Text: "ok"}}},
		}
		stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: msg})
		stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Message: msg})
		stream.End(nil)
	}()
	return stream
}

// userTurns lists the plain-text user messages in the agent history.
func userTurns(s *AgentSession) []string {
	var out []string
	for _, m := range s.State().Messages {
		if u := m.Message.AsUser(); u != nil {
			if t, ok := u.Content.(string); ok {
				out = append(out, t)
			}
		}
	}
	return out
}

func assertTurns(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("user turns = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("user turns = %q, want %q", got, want)
		}
	}
}

func TestTakeQueuedMessages_SteerFirstThenFollowUp(t *testing.T) {
	s, _ := newTestAgentSession(t)
	defer s.Close()
	if got := s.TakeQueuedMessages(); len(got) != 0 {
		t.Fatalf("empty queues: got %d", len(got))
	}
	// Follow-ups only: returned as-is.
	s.Agent.FollowUp(userMsg("f1"))
	if got := s.TakeQueuedMessages(); len(got) != 1 {
		t.Fatalf("follow-up only: got %d", len(got))
	}
	s.Agent.FollowUp(userMsg("f1"))
	s.Agent.Steer(userMsg("s1"))
	s.Agent.FollowUp(userMsg("f2"))
	got := s.TakeQueuedMessages()
	var texts []string
	for _, m := range got {
		texts = append(texts, m.Message.AsUser().Content.(string))
	}
	assertTurns(t, texts, "s1", "f1", "f2")
	if s.Agent.HasQueuedMessages() {
		t.Fatal("queues must be empty after TakeQueuedMessages")
	}
}

func TestPromptWithCarried_PromptThenCarriedInOrder(t *testing.T) {
	s := newTestAgentSessionWithModel(t, nil)
	defer s.Close()
	s.Agent.SetStreamFn(replyOK)
	if err := s.PromptWithCarried("handoff", []agent.AgentMessage{userMsg("q1"), userMsg("q2")}); err != nil {
		t.Fatal(err)
	}
	s.Agent.WaitForIdle()
	assertTurns(t, userTurns(s), "handoff", "q1", "q2")
}

func TestPromptWithCarried_EmptyPromptStartsWithCarried(t *testing.T) {
	s := newTestAgentSessionWithModel(t, nil)
	defer s.Close()
	s.Agent.SetStreamFn(replyOK)
	if err := s.PromptWithCarried("", nil); err != nil {
		t.Fatal(err)
	}
	assertTurns(t, userTurns(s))
	if err := s.PromptWithCarried("", []agent.AgentMessage{userMsg("q1"), userMsg("q2")}); err != nil {
		t.Fatal(err)
	}
	s.Agent.WaitForIdle()
	assertTurns(t, userTurns(s), "q1", "q2")
}

// With nothing able to run (no model), the carried messages stay queued in
// their original order rather than being dropped.
func TestPromptWithCarried_FailureKeepsQueue(t *testing.T) {
	s, _ := newTestAgentSession(t) // no model
	defer s.Close()
	s.Agent.Steer(userMsg("s0"))
	s.Agent.FollowUp(userMsg("f0"))
	if err := s.PromptWithCarried("", []agent.AgentMessage{userMsg("q1"), userMsg("q2")}); err == nil {
		t.Fatal("expected an error without a model")
	}
	var texts []string
	for _, m := range s.TakeQueuedMessages() {
		texts = append(texts, m.Message.AsUser().Content.(string))
	}
	assertTurns(t, texts, "s0", "f0", "q1", "q2")
}

func TestQueueNotices(t *testing.T) {
	if got := CarriedQueueNotice(1); got != "1 queued message carried into the new session" {
		t.Fatal(got)
	}
	if got := CarriedQueueNotice(3); got != "3 queued messages carried into the new session" {
		t.Fatal(got)
	}
	if got := DiscardedQueueNotice(2); got != "Discarded 2 queued messages" {
		t.Fatal(got)
	}
}
