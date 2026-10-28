package extension

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
)

// TestSessionBridge_SendUserMessage_Abort verifies that delivering a message
// with deliver_as="abort" cancels the in-flight turn (the ESC equivalent) and
// propagates to an in-flight, stuck tool call — rather than enqueuing anything.
//
// It wires a StreamFn that asks the agent to call a tool that blocks until its
// context is cancelled, starts a turn, then aborts via the bridge and asserts
// the tool's context fired and the agent returned to idle.
func TestSessionBridge_SendUserMessage_Abort(t *testing.T) {
	model := &ai.Model{
		Provider:      "test",
		ID:            "test-model",
		Name:          "Test",
		ContextWindow: 100000,
	}

	toolStarted := make(chan struct{})
	toolCancelled := make(chan struct{})

	var callMu sync.Mutex
	callN := 0
	streamFn := func(_ *ai.Model, _ ai.Context, _ *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		callMu.Lock()
		callN++
		first := callN == 1
		callMu.Unlock()
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			msg := &ai.AssistantMessage{
				Role:       ai.RoleAssistant,
				Provider:   model.Provider,
				Model:      model.ID,
				StopReason: ai.StopReasonStop,
				Timestamp:  time.Now().UnixMilli(),
			}
			if first {
				msg.StopReason = ai.StopReasonToolUse
				msg.Content = []ai.AssistantContent{{
					ToolCall: &ai.ToolCall{
						Type:      ai.ContentTypeToolCall,
						ID:        "tc-abort-1",
						Name:      "block_forever",
						Arguments: map[string]any{},
					},
				}}
			} else {
				msg.Content = []ai.AssistantContent{{Text: &ai.TextContent{Type: "text", Text: "done"}}}
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: msg})
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Message: msg})
			stream.End(nil)
		}()
		return stream
	}

	a := agent.NewAgent(agent.AgentOptions{
		InitialState: &agent.AgentState{Model: model},
		StreamFn:     streamFn,
	})
	sess := session.NewAgentSession(session.AgentSessionOptions{
		Agent:          a,
		SessionStore:   store.InMemorySessionStore(),
		ResourceLoader: &stubResourceLoader{},
		Cwd:            t.TempDir(),
	})
	sb := NewSessionBridge(sess)

	var startOnce, cancelOnce sync.Once
	// A tool that blocks until its context is cancelled — the stuck-tool case.
	sb.RegisterTool(ToolDefinition{
		Name:        "block_forever",
		Description: "blocks until aborted",
		Parameters:  map[string]any{"type": "object"},
		Execute: func(tc ToolContext) (ToolResult, error) {
			startOnce.Do(func() { close(toolStarted) })
			<-tc.Context.Done()
			cancelOnce.Do(func() { close(toolCancelled) })
			return ToolResult{IsError: true, Content: []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: "aborted"}}}, nil
		},
	})

	go func() { _ = sess.Prompt("call the tool") }()

	select {
	case <-toolStarted:
	case <-time.After(15 * time.Second):
		t.Fatal("tool never started")
	}

	// Deliver the abort. This must cancel the run context, propagating to the
	// in-flight tool, not enqueue anything.
	sb.SendUserMessage("", &SendUserMessageOptions{DeliverAs: "abort"})

	select {
	case <-toolCancelled:
		// The in-flight tool's context was cancelled by the abort.
	case <-time.After(15 * time.Second):
		t.Fatal("tool context was not cancelled after abort")
	}

	select {
	case <-a.IdleChan():
	case <-time.After(15 * time.Second):
		t.Fatal("agent did not return to idle after abort")
	}

	if a.HasQueuedMessages() {
		t.Fatal("abort must not enqueue any message")
	}
}

// TestSessionBridge_RestartSession_TakesQueues: RestartSession must take the
// queued messages off the agent BEFORE aborting (so the aborted run cannot
// drain them into the discarded history) and hand them to the consumer,
// steer first. A superseded, unconsumed restart's carried messages are kept
// ahead of newer ones.
func TestSessionBridge_RestartSession_TakesQueues(t *testing.T) {
	a := agent.NewAgent(agent.AgentOptions{})
	sess := session.NewAgentSession(session.AgentSessionOptions{
		Agent:          a,
		SessionStore:   store.InMemorySessionStore(),
		ResourceLoader: &stubResourceLoader{},
		Cwd:            t.TempDir(),
	})
	t.Cleanup(sess.Close)
	sb := NewSessionBridge(sess)
	sb.SetRestartFn(func() {})

	msg := func(s string) agent.AgentMessage { return agent.NewAgentMessage(ai.NewUserMsg(s, 0)) }
	text := func(ms []agent.AgentMessage) []string {
		var out []string
		for _, m := range ms {
			out = append(out, m.Message.AsUser().Content.(string))
		}
		return out
	}

	a.FollowUp(msg("f1"))
	a.Steer(msg("s1"))
	if err := sb.RestartSession("p1", ""); err != nil {
		t.Fatal(err)
	}
	if a.HasQueuedMessages() {
		t.Fatal("RestartSession must take the queues off the agent")
	}
	a.FollowUp(msg("f2"))
	if err := sb.RestartSession("p2", "b2"); err != nil {
		t.Fatal(err)
	}
	req, ok := sb.TakePendingRestart()
	if !ok || req.Prompt != "p2" || req.PrependContext != "b2" {
		t.Fatalf("TakePendingRestart = %+v, %v", req, ok)
	}
	if got := strings.Join(text(req.Carried), ","); got != "s1,f1,f2" {
		t.Fatalf("carried = %s, want s1,f1,f2", got)
	}
}

// TestSessionBridge_PendingSteers counts only steering messages, not
// follow-ups, so wait/pipe yield to a steer but not to a queued follow-up.
func TestSessionBridge_PendingSteers(t *testing.T) {
	a := agent.NewAgent(agent.AgentOptions{})
	sess := session.NewAgentSession(session.AgentSessionOptions{
		Agent:          a,
		SessionStore:   store.InMemorySessionStore(),
		ResourceLoader: &stubResourceLoader{},
		Cwd:            t.TempDir(),
	})
	t.Cleanup(sess.Close)
	sb := NewSessionBridge(sess)
	if n := sb.PendingSteers(); n != 0 {
		t.Fatalf("PendingSteers = %d, want 0", n)
	}
	a.FollowUp(agent.NewAgentMessage(ai.NewUserMsg("f", 0)))
	if n := sb.PendingSteers(); n != 0 {
		t.Fatalf("follow-up counted as steer: %d", n)
	}
	a.Steer(agent.NewAgentMessage(ai.NewUserMsg("s", 0)))
	if n := sb.PendingSteers(); n != 1 {
		t.Fatalf("PendingSteers = %d, want 1", n)
	}
	if n := (&SessionBridge{}).PendingSteers(); n != 0 {
		t.Fatalf("nil session PendingSteers = %d", n)
	}
}

// TestSessionBridge_RestartSession_CarriesUnanswered: a user message that
// started the in-flight turn lives in history, which the restart erases, so
// TakePendingRestart must carry it — ahead of queued messages. Messages already
// answered with text are not carried; tool-call-only turns are not answers.
func TestSessionBridge_RestartSession_CarriesUnanswered(t *testing.T) {
	user := func(s string) agent.AgentMessage { return agent.NewAgentMessage(ai.NewUserMsg(s, 0)) }
	asst := func(c ai.AssistantContent) agent.AgentMessage {
		return agent.NewAgentMessage(ai.NewAssistantMsg(ai.AssistantMessage{Content: []ai.AssistantContent{c}}))
	}
	text := func(s string) agent.AgentMessage {
		return asst(ai.AssistantContent{Text: &ai.TextContent{Type: "text", Text: s}})
	}
	toolCall := asst(ai.AssistantContent{ToolCall: &ai.ToolCall{Type: ai.ContentTypeToolCall, ID: "tc", Name: "self_handoff"}})

	for _, tc := range []struct {
		name    string
		history []agent.AgentMessage
		want    string
	}{
		{"unanswered", []agent.AgentMessage{user("u1"), text("a1"), user("u2"), user("u3"), toolCall}, "u2,u3,s1,f1"},
		{"answered", []agent.AgentMessage{user("u1"), toolCall, text("a1")}, "s1,f1"},
		{"aborted text is no answer", []agent.AgentMessage{user("u1"), text("a1"), user("u2"), toolCall,
			agent.NewAgentMessage(ai.NewAssistantMsg(ai.AssistantMessage{StopReason: ai.StopReasonAborted,
				Content: []ai.AssistantContent{{Text: &ai.TextContent{Type: "text", Text: "part"}}}}))}, "u2,s1,f1"},
		{"injected context is not carried", []agent.AgentMessage{user("[SYS_EXT]\nbrief\n[/SYS_EXT]"), user("u1"), toolCall}, "u1,s1,f1"},
		{"no answer yet", []agent.AgentMessage{user("u1"), toolCall}, "u1,s1,f1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := agent.NewAgent(agent.AgentOptions{})
			sess := session.NewAgentSession(session.AgentSessionOptions{
				Agent:          a,
				SessionStore:   store.InMemorySessionStore(),
				ResourceLoader: &stubResourceLoader{},
				Cwd:            t.TempDir(),
			})
			t.Cleanup(sess.Close)
			sb := NewSessionBridge(sess)
			sb.SetRestartFn(func() {})
			a.ReplaceMessages(tc.history)
			a.FollowUp(user("f1"))
			a.Steer(user("s1"))
			if err := sb.RestartSession("p", ""); err != nil {
				t.Fatal(err)
			}
			req, ok := sb.TakePendingRestart()
			if !ok || req.Prompt != "p" {
				t.Fatalf("TakePendingRestart = %+v, %v", req, ok)
			}
			var got []string
			for _, m := range req.Carried {
				got = append(got, m.Message.AsUser().Content.(string))
			}
			if s := strings.Join(got, ","); s != tc.want {
				t.Fatalf("carried = %s, want %s", s, tc.want)
			}
		})
	}
}

// TestSessionBridge_RestartSession_ChainedSkipsHandoffPrompt: when the new
// session hands off again before answering, its own handoff prompt is not
// replayed as an unanswered user message, but the still-unanswered original
// is.
func TestSessionBridge_RestartSession_ChainedSkipsHandoffPrompt(t *testing.T) {
	a := agent.NewAgent(agent.AgentOptions{})
	sess := session.NewAgentSession(session.AgentSessionOptions{
		Agent:          a,
		SessionStore:   store.InMemorySessionStore(),
		ResourceLoader: &stubResourceLoader{},
		Cwd:            t.TempDir(),
	})
	t.Cleanup(sess.Close)
	sb := NewSessionBridge(sess)
	sb.SetRestartFn(func() {})
	user := func(s string) agent.AgentMessage { return agent.NewAgentMessage(ai.NewUserMsg(s, 0)) }

	a.ReplaceMessages([]agent.AgentMessage{user("q")})
	if err := sb.RestartSession("HANDOFF", ""); err != nil {
		t.Fatal(err)
	}
	if req, _ := sb.TakePendingRestart(); len(req.Carried) != 1 {
		t.Fatalf("first handoff carried %d, want 1", len(req.Carried))
	}
	a.ReplaceMessages([]agent.AgentMessage{user("[SYS_EXT]\nb\n[/SYS_EXT]"), user("HANDOFF"), user("q")})
	if err := sb.RestartSession("HANDOFF", ""); err != nil {
		t.Fatal(err)
	}
	req, _ := sb.TakePendingRestart()
	if len(req.Carried) != 1 || req.Carried[0].AsUser().Content != "q" {
		t.Fatalf("chained handoff carried %+v, want only q", req.Carried)
	}
}
