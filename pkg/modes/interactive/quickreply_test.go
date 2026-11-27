package interactive

import (
	"strings"
	"testing"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/session"
)

func fakeReply(tm *testMode, reply string) {
	a := tm.mode.session.Agent
	a.SetModel(&ai.Model{ID: "test-model", Provider: "anthropic", API: "anthropic"})
	a.SetStreamFn(func(*ai.Model, ai.Context, *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		st := ai.NewAssistantMessageEventStream()
		go func() {
			st.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Message: &ai.AssistantMessage{
				Content: []ai.AssistantContent{ai.NewTextContent(reply)},
			}})
			st.End(nil)
		}()
		return st
	})
}

func TestQuickReply_ShowsAnswerAndTag(t *testing.T) {
	tm := newTestModeWithSession(t)
	fakeReply(tm, "It is four.\n1 9")
	tm.mode.quickReply("what is 2+2?")
	tm.waitRender()
	out := tm.renderedOutput()
	if !strings.Contains(out, "It is four.") || !strings.Contains(out, "quick reply · answered") {
		t.Fatalf("output missing quick reply:\n%s", out)
	}
	if n := len(tm.mode.session.Agent.State().Messages); n != 0 {
		t.Errorf("main context has %d messages, want 0", n)
	}
	if n := tm.mode.pendingPreemptiveUserMsgs.Load(); n != 0 {
		t.Errorf("pendingPreemptiveUserMsgs = %d, want 0", n)
	}
}

func TestQuickReply_SteerNotRenderedTwice(t *testing.T) {
	tm := newTestModeWithSession(t)
	steer := "[while you were working, the user asked and a side branch answered]\nQ: SECRETQ\nA: x"
	if !session.IsInterjectSteer(steer) {
		t.Fatal("prefix drifted from session.interjectPrefix")
	}
	msg := agent.NewAgentMessage(ai.NewUserMsg(steer, 0))
	tm.mode.onMessageStart(&agent.AgentEvent{Type: agent.EventMessageStart, Message: &msg})
	tm.waitRender()
	if strings.Contains(tm.renderedOutput(), "SECRETQ") {
		t.Fatal("steered quick reply rendered as a user message")
	}
}
