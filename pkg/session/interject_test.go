package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

func TestParseInterject(t *testing.T) {
	cases := []struct {
		in         string
		text, note string
		act        InterjectAction
		conf       int
	}{
		{"It is 4.\n1 9", "It is 4.", "", InterjectNone, 9},
		{"Sure.\nNOTE: use tabs\n**2 8**", "Sure.", "use tabs", InterjectInform, 8},
		{"Stop.\n3 7", "Stop.", "", InterjectAbort, 7},
		{"no verdict here", "no verdict here", "", InterjectNone, -1},
		{"bad\n4 9", "bad\n4 9", "", InterjectNone, -1},
	}
	for _, c := range cases {
		r := ParseInterject(c.in)
		if r.Text != c.text || r.Note != c.note || r.Action != c.act || r.Conf != c.conf {
			t.Errorf("ParseInterject(%q) = %+v", c.in, r)
		}
	}
}

func interjectSession(t *testing.T, reply string) (*AgentSession, *string) {
	t.Helper()
	s, _ := newTestAgentSession(t)
	t.Cleanup(s.Close)
	s.Agent.SetModel(&ai.Model{ID: "test-model", Provider: "anthropic", API: "anthropic"})
	var asked string
	s.Agent.SetStreamFn(func(m *ai.Model, llmCtx ai.Context, opts *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		if n := len(llmCtx.Messages); n > 0 {
			if u := llmCtx.Messages[n-1].AsUser(); u != nil {
				b, _ := json.Marshal(u.Content)
				asked = string(b)
			}
		}
		st := ai.NewAssistantMessageEventStream()
		go func() {
			st.Push(ai.AssistantMessageEvent{Type: ai.EventTextDelta, Delta: reply})
			st.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Message: &ai.AssistantMessage{
				Content: []ai.AssistantContent{ai.NewTextContent(reply)},
			}})
			st.End(nil)
		}()
		return st
	})
	return s, &asked
}

func TestInterject_NoneRecordsSideQuery(t *testing.T) {
	s, asked := interjectSession(t, "It is 4.\n1 9")
	r, err := s.Interject(context.Background(), "what is 2+2?")
	if err != nil {
		t.Fatal(err)
	}
	if r.Action != InterjectNone || r.Text != "It is 4." {
		t.Fatalf("reply = %+v", r)
	}
	if !strings.Contains(*asked, "what is 2+2?") || !strings.Contains(*asked, "ACTION CONF") {
		t.Errorf("branch prompt = %q", *asked)
	}
	var found bool
	for _, e := range s.SessionStore.GetEntries() {
		if e.CustomType == SideQueryCustomType {
			found = true
		}
	}
	if !found {
		t.Error("no side_query entry recorded")
	}
	if n := len(s.Agent.State().Messages); n != 0 {
		t.Errorf("main context grew to %d messages", n)
	}
}

func TestInterject_LowConfidenceIsNone(t *testing.T) {
	s, _ := interjectSession(t, "Maybe.\nNOTE: x\n3 2")
	r, err := s.Interject(context.Background(), "stop?")
	if err != nil {
		t.Fatal(err)
	}
	if r.Action != InterjectNone || r.Note != "" {
		t.Fatalf("reply = %+v", r)
	}
}

func TestInterject_InformWithoutTurnIsNotSteered(t *testing.T) {
	s, _ := interjectSession(t, "Will do.\n2 9")
	r, err := s.Interject(context.Background(), "use tabs")
	if err != nil {
		t.Fatal(err)
	}
	if r.Action != InterjectInform || r.Steered {
		t.Fatalf("reply = %+v", r)
	}
}

func TestInterject_EmptyIsError(t *testing.T) {
	s, _ := interjectSession(t, "")
	if _, err := s.Interject(context.Background(), "hi"); err == nil {
		t.Fatal("want error")
	}
}
