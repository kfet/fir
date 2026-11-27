package session

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
)

// Quick replies: a message that arrives while a turn runs is asked of a
// branch of the session (a side query over a snapshot of the context),
// which answers at once and judges what the running turn needs:
//
//	InterjectNone   — the answer was enough; the main turn never sees it.
//	InterjectInform — the exchange is steered into the running turn.
//	InterjectAbort  — the running turn is stopped.
//
// Modelled on acp-kit's interject package, which zulip-acp uses over
// ACP (session/fork + _fir/session/steer). Here the branch is local, so
// it has no tools.

// InterjectAction is the branch's verdict on the running turn.
type InterjectAction int

// Verdicts, numbered as the branch reports them.
const (
	InterjectNone InterjectAction = iota + 1
	InterjectInform
	InterjectAbort
)

// String returns the verdict's tag word.
func (a InterjectAction) String() string {
	switch a {
	case InterjectInform:
		return "told main"
	case InterjectAbort:
		return "stopping"
	default:
		return "answered"
	}
}

// InterjectMinConf is the confidence a verdict needs to act; below it the
// verdict becomes InterjectNone.
const InterjectMinConf = 7

// SideQueryCustomType is the customType of the transcript entry that records
// a side exchange the LLM never sees.
const SideQueryCustomType = "side_query"

// SideQueryData is the payload of a side_query entry.
type SideQueryData struct {
	Question      string `json:"question"`
	Answer        string `json:"answer"`
	ForkSessionId string `json:"forkSessionId,omitempty"`
}

// interjectPrefix heads side Q&A handed to the main turn.
const interjectPrefix = "[while you were working, the user asked and a side branch answered]\n"

// IsInterjectSteer reports whether a user message is a quick reply steered
// into the running turn by Interject.
func IsInterjectSteer(text string) bool { return strings.HasPrefix(text, interjectPrefix) }

// interjectInstruction is appended to every branch prompt. The branch has no
// tools, so it must not claim actions.
const interjectInstruction = `[side question while your main task keeps running in another branch]
Answer the message above briefly from what you already know. You have no tools here; do not claim to have done anything. Leave all actions to the main task.
In your answer, say what the main task will do at its next step if relevant.
Optionally, just before the last line, add one line "NOTE: <text>" — a note to your main task, sent with action 2.
End with a final line of exactly two numbers separated by one space, for example "2 8": ACTION CONF
ACTION: 1 = just answer, the main task never sees this; 2 = also tell the main task now (it keeps running); 3 = stop the main task.
CONF: your confidence 0-9.`

// InterjectReply is the branch's answer and what was done with it.
type InterjectReply struct {
	Text   string          // the answer, NOTE and verdict lines stripped
	Note   string          // note for the main turn; Inform only
	Action InterjectAction // after the confidence gate
	Conf   int             // -1 when the verdict line did not parse
	// Steered: the exchange went into the running turn. False on an Inform
	// whose turn ended first — the caller then prompts with the message.
	Steered bool
}

// interjectState keeps side Q&A of the current turn, for later branches.
type interjectState struct {
	mu sync.Mutex // serialises Interject calls
	qa []string
}

// ParseInterject splits branch output into answer, optional NOTE line and
// the "ACTION CONF" verdict. An unparseable last line yields the whole text,
// InterjectNone and conf -1.
func ParseInterject(out string) InterjectReply {
	out = strings.TrimSpace(out)
	idx := strings.LastIndexByte(out, '\n')
	f := strings.Fields(strings.Trim(out[idx+1:], "*` \t"))
	bad := InterjectReply{Text: out, Action: InterjectNone, Conf: -1}
	if len(f) != 2 {
		return bad
	}
	a, err1 := strconv.Atoi(f[0])
	c, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil || a < 1 || a > 3 || c < 0 || c > 9 {
		return bad
	}
	r := InterjectReply{Text: strings.TrimSpace(out[:idx+1]), Action: InterjectAction(a), Conf: c}
	j := strings.LastIndexByte(r.Text, '\n')
	if n, ok := strings.CutPrefix(strings.TrimSpace(r.Text[j+1:]), "NOTE:"); ok {
		r.Note = strings.TrimSpace(n)
		r.Text = strings.TrimSpace(r.Text[:j+1])
	}
	return r
}

// Interject asks a branch of the session text and acts on its verdict:
// Inform steers the exchange into the running turn, Abort stops it, and
// None and Abort record a side_query entry. Calls are serialised. Bound ctx.
// On error nothing was done; the caller falls back to prompting with text.
func (s *AgentSession) Interject(ctx context.Context, text string) (InterjectReply, error) {
	st := &s.interject
	st.mu.Lock()
	defer st.mu.Unlock()

	var b strings.Builder
	if len(st.qa) > 0 {
		b.WriteString("[earlier side Q&A this turn]\n")
		b.WriteString(strings.Join(st.qa, "\n\n"))
		b.WriteString("\n\n")
	}
	b.WriteString(text)
	b.WriteString("\n\n")
	b.WriteString(interjectInstruction)

	out, err := s.SideQuery(ctx, b.String(), nil)
	if err != nil {
		return InterjectReply{}, err
	}
	r := ParseInterject(out)
	if r.Text == "" && r.Conf < 0 {
		return InterjectReply{}, errors.New("interject: branch returned no answer")
	}
	if r.Conf < InterjectMinConf {
		r.Action = InterjectNone
	}
	if r.Action != InterjectInform {
		r.Note = ""
	}
	qa := "Q: " + text + "\nA: " + r.Text
	if r.Note != "" {
		qa += "\nNote from the side branch: " + r.Note
	}

	if r.Action == InterjectInform {
		r.Steered = s.Steer(interjectPrefix+qa, nil) == nil
	} else {
		if s.SessionStore != nil {
			data, _ := json.Marshal(SideQueryData{Question: text, Answer: r.Text}) // plain strings: cannot fail
			s.SessionStore.AppendCustomEntry(SideQueryCustomType, data)
		}
		if r.Action == InterjectAbort && s.IsStreaming() {
			s.Abort()
		}
	}
	if s.IsStreaming() {
		st.qa = append(st.qa, qa)
	} else {
		st.qa = nil
	}
	return r, nil
}

// resetInterject drops the side Q&A at turn end. TryLock: a running
// Interject resets it itself once it sees the turn is over.
func (s *AgentSession) resetInterject() {
	if s.interject.mu.TryLock() {
		s.interject.qa = nil
		s.interject.mu.Unlock()
	}
}
