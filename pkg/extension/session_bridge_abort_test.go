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
			if s := carriedTexts(req); s != tc.want {
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
	a, sb := newRestartBridge(t)
	user := func(s string) agent.AgentMessage { return agent.NewAgentMessage(ai.NewUserMsg(s, 0)) }

	a.ReplaceMessages([]agent.AgentMessage{user("q")})
	if err := sb.RestartSession("HANDOFF", ""); err != nil {
		t.Fatal(err)
	}
	first, _ := sb.TakePendingRestart()
	if len(first.Carried) != 1 {
		t.Fatalf("first handoff carried %d, want 1", len(first.Carried))
	}
	a.ReplaceMessages([]agent.AgentMessage{user("[SYS_EXT]\nb\n[/SYS_EXT]"), user("HANDOFF"), first.Carried[0], user("q2")})
	if err := sb.RestartSession("HANDOFF", ""); err != nil {
		t.Fatal(err)
	}
	req, _ := sb.TakePendingRestart()
	// q was carried once already; carrying it again would loop forever when
	// q itself asks for a handoff. Only the new q2 is carried.
	if carriedTexts(req) != "q2" {
		t.Fatalf("chained handoff carried %+v, want only q2", req.Carried)
	}
}

func newRestartBridge(t *testing.T) (*agent.Agent, *SessionBridge) {
	t.Helper()
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
	return a, sb
}

func carriedTexts(req RestartRequest) string {
	var got []string
	for _, m := range req.Carried {
		got = append(got, strings.TrimPrefix(m.Message.AsUser().Content.(string), CarriedNote+"\n\n"))
	}
	return strings.Join(got, ",")
}

// TestSessionBridge_RestartSession_RepeatedTextIsNewMessage: carry-at-most-once
// is per message, not per text. A later message that happens to repeat an
// earlier carried one ("continue") is a new message and must be carried.
func TestSessionBridge_RestartSession_RepeatedTextIsNewMessage(t *testing.T) {
	a, sb := newRestartBridge(t)
	user := func(s string, ts int64) agent.AgentMessage { return agent.NewAgentMessage(ai.NewUserMsg(s, ts)) }
	answer := agent.NewAgentMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Content: []ai.AssistantContent{{Text: &ai.TextContent{Type: "text", Text: "ok"}}}}))

	a.ReplaceMessages([]agent.AgentMessage{user("continue", 1)})
	if err := sb.RestartSession("HANDOFF", ""); err != nil {
		t.Fatal(err)
	}
	if req, _ := sb.TakePendingRestart(); carriedTexts(req) != "continue" {
		t.Fatalf("first handoff carried %q", carriedTexts(req))
	}
	a.ReplaceMessages([]agent.AgentMessage{user("HANDOFF", 2), user("continue", 1), answer, user("continue", 5)})
	if err := sb.RestartSession("HANDOFF", ""); err != nil {
		t.Fatal(err)
	}
	if req, _ := sb.TakePendingRestart(); carriedTexts(req) != "continue" {
		t.Fatalf("second handoff carried %q, want the new continue", carriedTexts(req))
	}
}

// TestSessionBridge_RestartSession_LateMessageAfterQueued: a user message that
// arrived after the restart was requested (drained into history while the
// aborted turn unwound) goes after the messages queued before it.
func TestSessionBridge_RestartSession_LateMessageAfterQueued(t *testing.T) {
	a, sb := newRestartBridge(t)
	user := func(s string, ts int64) agent.AgentMessage { return agent.NewAgentMessage(ai.NewUserMsg(s, ts)) }
	a.ReplaceMessages([]agent.AgentMessage{user("u1", 1)})
	a.FollowUp(user("f1", 2))
	if err := sb.RestartSession("p", ""); err != nil {
		t.Fatal(err)
	}
	a.ReplaceMessages([]agent.AgentMessage{user("u1", 1), user("late", time.Now().Add(time.Hour).UnixMilli())})
	if req, _ := sb.TakePendingRestart(); carriedTexts(req) != "u1,f1,late" {
		t.Fatalf("carried = %q, want u1,f1,late", carriedTexts(req))
	}
}

// TestSessionBridge_RestartSession_InFlightCarriedIsNoted: the message that
// started the handing-off turn is carried with a note that the handoff is
// done, so the new session answers it instead of handing off again. Queued
// messages never reached the model and are carried as-is.
func TestSessionBridge_RestartSession_InFlightCarriedIsNoted(t *testing.T) {
	a, sb := newRestartBridge(t)
	a.ReplaceMessages([]agent.AgentMessage{agent.NewAgentMessage(ai.NewUserMsg("q", 7))})
	a.FollowUp(agent.NewAgentMessage(ai.NewUserMsg("f", 8)))
	if err := sb.RestartSession("p", ""); err != nil {
		t.Fatal(err)
	}
	req, _ := sb.TakePendingRestart()
	if len(req.Carried) != 2 {
		t.Fatalf("carried %d, want 2", len(req.Carried))
	}
	q := req.Carried[0].AsUser()
	if s, _ := q.Content.(string); !strings.HasPrefix(s, CarriedNote) || !strings.HasSuffix(s, "\n\nq") || q.Timestamp != 7 {
		t.Fatalf("in-flight message = %+v, want noted q with ts 7", q)
	}
	if f := req.Carried[1].AsUser(); f.Content != "f" {
		t.Fatalf("queued message = %+v, want f as-is", f)
	}
	// The noted copy is what the new session holds; it is not re-carried.
	a.ReplaceMessages([]agent.AgentMessage{agent.NewAgentMessage(ai.NewUserMsg("p", 9)), req.Carried[0], req.Carried[1]})
	if err := sb.RestartSession("p", ""); err != nil {
		t.Fatal(err)
	}
	if req, _ := sb.TakePendingRestart(); len(req.Carried) != 0 {
		t.Fatalf("re-carried %q", carriedTexts(req))
	}
}

// TestWithCarriedNote: the note goes first for both content shapes, keeps the
// timestamp and other blocks, and is never applied twice.
func TestWithCarriedNote(t *testing.T) {
	note := CarriedNote + "\n\n"
	s := withCarriedNote(agent.NewAgentMessage(ai.NewUserMsg("q", 3)))
	if u := s.AsUser(); u.Content != note+"q" || u.Timestamp != 3 {
		t.Fatalf("string: %+v", u)
	}
	if again := withCarriedNote(s); again.AsUser().Content != note+"q" {
		t.Fatalf("string noted twice: %q", again.AsUser().Content)
	}
	img := ai.ImageContent{Type: "image", Data: "x", MimeType: "image/png"}
	b := withCarriedNote(agent.NewAgentMessage(ai.NewUserMsg([]any{img}, 4)))
	c := b.AsUser().Content.([]any)
	if len(c) != 2 || c[0] != any(ai.TextContent{Type: "text", Text: note}) || c[1] != any(img) || b.AsUser().Timestamp != 4 {
		t.Fatalf("blocks: %+v", b.AsUser())
	}
	if again := withCarriedNote(b); len(again.AsUser().Content.([]any)) != 2 {
		t.Fatal("blocks noted twice")
	}
}
