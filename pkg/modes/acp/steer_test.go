package acp

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/extension"
	"github.com/kfet/fir/pkg/resources"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
)

// newSteerSession builds a session whose scripted model calls gate_tool on
// its first request and answers "ok" afterwards. gate_tool closes started,
// then blocks until release is closed. contexts records the LLM context of
// every request after the first.
func newSteerSession(t *testing.T) (sess *session.AgentSession, started, release chan struct{}, contexts func() []ai.Context) {
	t.Helper()
	model := &ai.Model{Provider: "test", ID: "test-model", Name: "Test", ContextWindow: 100000}
	var mu sync.Mutex
	calls := 0
	var seen []ai.Context
	streamFn := func(_ *ai.Model, c ai.Context, _ *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		mu.Lock()
		calls++
		first := calls == 1
		if !first {
			seen = append(seen, c)
		}
		mu.Unlock()
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			msg := &ai.AssistantMessage{
				Role: ai.RoleAssistant, Provider: model.Provider, Model: model.ID,
				StopReason: ai.StopReasonStop, Timestamp: time.Now().UnixMilli(),
			}
			if first {
				msg.StopReason = ai.StopReasonToolUse
				msg.Content = []ai.AssistantContent{{ToolCall: &ai.ToolCall{
					Type: ai.ContentTypeToolCall, ID: "tc-gate-1", Name: "gate_tool", Arguments: map[string]any{},
				}}}
			} else {
				msg.Content = []ai.AssistantContent{{Text: &ai.TextContent{Type: "text", Text: "ok"}}}
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: msg})
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Message: msg})
			stream.End(nil)
		}()
		return stream
	}

	cwd := t.TempDir()
	agentDir := t.TempDir()
	a := agent.NewAgent(agent.AgentOptions{
		InitialState: &agent.AgentState{Model: model},
		StreamFn:     streamFn,
		ConvertToLLM: store.ConvertToLLM,
	})
	sess = session.NewAgentSession(session.AgentSessionOptions{
		Agent:          a,
		SessionStore:   store.NewSessionStore(cwd, filepath.Join(agentDir, "sessions")),
		ResourceLoader: resources.NewResourceLoader(resources.ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir}),
		Cwd:            cwd,
	})
	t.Cleanup(sess.Close)
	sess.SessionStore.NewSession(nil)

	started = make(chan struct{})
	release = make(chan struct{})
	var once sync.Once
	extension.NewSessionBridge(sess).RegisterTool(extension.ToolDefinition{
		Name: "gate_tool", Description: "blocks until released",
		Parameters: map[string]any{"type": "object"},
		Execute: func(extension.ToolContext) (extension.ToolResult, error) {
			once.Do(func() { close(started) })
			<-release
			return extension.ToolResult{Content: []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: "done"}}}, nil
		},
	})
	return sess, started, release, func() []ai.Context {
		mu.Lock()
		defer mu.Unlock()
		return append([]ai.Context(nil), seen...)
	}
}

func steerParams(t *testing.T, sid, text string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(SteerSessionRequest{SessionId: acpsdk.SessionId(sid), Prompt: textPrompt(text)})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func contextHasUserText(c ai.Context, want string) bool {
	raw, _ := json.Marshal(c.Messages)
	return strings.Contains(string(raw), want)
}

// TestSteer_MidTurnReachesModelWithoutCancel: a steer sent while a tool runs
// is seen by the model on the next request of the SAME turn, and the turn
// completes normally (end_turn, not cancelled).
func TestSteer_MidTurnReachesModelWithoutCancel(t *testing.T) {
	sess, started, release, contexts := newSteerSession(t)
	pa := &firAgent{conn: newMockConn(), sessions: map[string]*firSession{
		"s1": {termState: newTerminalState(), session: sess},
	}}
	handler := rawMethodHandler(pa, newWriteNotifier(io.Discard))

	type result struct {
		resp acpsdk.PromptResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		r, err := pa.Prompt(context.Background(), acpsdk.PromptRequest{SessionId: "s1", Prompt: textPrompt("run the gate")})
		done <- result{r, err}
	}()
	select {
	case <-started:
	case <-time.After(15 * time.Second):
		t.Fatal("gate_tool never started")
	}

	if _, rerr := handler(context.Background(), SteerMethod, steerParams(t, "s1", "side answer: use plan B")); rerr != nil {
		t.Fatalf("steer: %v", rerr)
	}
	if n := sess.Agent.SteeringQueueLen(); n != 1 {
		t.Fatalf("steering queue = %d, want 1", n)
	}
	close(release)

	var r result
	select {
	case r = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("prompt never returned")
	}
	if r.err != nil {
		t.Fatalf("prompt: %v", r.err)
	}
	if r.resp.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("stop reason = %q, want end_turn", r.resp.StopReason)
	}
	cs := contexts()
	if len(cs) != 1 || !contextHasUserText(cs[0], "side answer: use plan B") {
		t.Fatalf("second request of the turn did not carry the steer; contexts = %d", len(cs))
	}
}

// TestSteer_NoActiveTurn: with no running turn the method fails with
// NoActiveTurnError and queues nothing.
func TestSteer_NoActiveTurn(t *testing.T) {
	sess, _, release, _ := newSteerSession(t)
	close(release)
	pa := &firAgent{conn: newMockConn(), sessions: map[string]*firSession{
		"s1": {termState: newTerminalState(), session: sess},
	}}
	handler := rawMethodHandler(pa, newWriteNotifier(io.Discard))
	_, rerr := handler(context.Background(), SteerMethod, steerParams(t, "s1", "hello"))
	if rerr == nil || rerr.Code != NoActiveTurnError {
		t.Fatalf("err = %v, want code %d", rerr, NoActiveTurnError)
	}
	if n := sess.Agent.SteeringQueueLen(); n != 0 {
		t.Fatalf("steering queue = %d, want 0", n)
	}
}

func TestSteer_Errors(t *testing.T) {
	sess, _, release, _ := newSteerSession(t)
	close(release)
	pa := &firAgent{conn: newMockConn(), sessions: map[string]*firSession{
		"s1": {termState: newTerminalState(), session: sess},
	}}
	handler := rawMethodHandler(pa, newWriteNotifier(io.Discard))

	if _, rerr := handler(context.Background(), SteerMethod, steerParams(t, "nope", "x")); rerr == nil || rerr.Code != SessionNotFoundError {
		t.Fatalf("unknown session err = %v", rerr)
	}
	if _, rerr := handler(context.Background(), SteerMethod, steerParams(t, "s1", "")); rerr == nil || rerr.Code != -32602 {
		t.Fatalf("empty prompt err = %v", rerr)
	}
	if _, rerr := handler(context.Background(), SteerMethod, json.RawMessage(`{"sessionId":`)); rerr == nil || rerr.Code != -32602 {
		t.Fatalf("bad params err = %v", rerr)
	}
}

func TestInitialize_AdvertisesSteer(t *testing.T) {
	pa := &firAgent{sessions: make(map[string]*firSession)}
	handler := rawMethodHandler(pa, newWriteNotifier(io.Discard))
	params, _ := json.Marshal(acpsdk.InitializeRequest{ProtocolVersion: 1})
	resp, rerr := handler(context.Background(), "initialize", params)
	if rerr != nil {
		t.Fatal(rerr)
	}
	raw, _ := json.Marshal(resp)
	var got struct {
		AgentCapabilities struct {
			Meta map[string]struct {
				Method string `json:"method"`
			} `json:"_meta"`
		} `json:"agentCapabilities"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.AgentCapabilities.Meta[steerExtKey].Method != SteerMethod {
		t.Fatalf("capability = %s", raw)
	}
}

func recordParams(t *testing.T, sid, mode, q, a, fork string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(SteerSessionRequest{SessionId: acpsdk.SessionId(sid), Mode: mode, Question: q, Answer: a, ForkSessionId: fork})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func checkSideQueryEntry(t *testing.T, sess *session.AgentSession, resp any, want SideQueryData) {
	t.Helper()
	id := resp.(SteerSessionResponse).EntryId
	e := sess.SessionStore.GetEntry(id)
	if e == nil || e.Type != "custom" || e.CustomType != SideQueryCustomType {
		t.Fatalf("entry %q = %#v", id, e)
	}
	var got SideQueryData
	if err := json.Unmarshal(e.Data, &got); err != nil || got != want {
		t.Fatalf("data = %s (err %v), want %+v", e.Data, err, want)
	}
}

// TestSteer_RecordMidTurn: record mode during a running turn appends a
// side_query entry, queues nothing, and the model never sees it.
func TestSteer_RecordMidTurn(t *testing.T) {
	sess, started, release, contexts := newSteerSession(t)
	pa := &firAgent{conn: newMockConn(), sessions: map[string]*firSession{
		"s1": {termState: newTerminalState(), session: sess},
	}}
	handler := rawMethodHandler(pa, newWriteNotifier(io.Discard))
	done := make(chan error, 1)
	go func() {
		_, err := pa.Prompt(context.Background(), acpsdk.PromptRequest{SessionId: "s1", Prompt: textPrompt("run the gate")})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(15 * time.Second):
		t.Fatal("gate_tool never started")
	}
	resp, rerr := handler(context.Background(), SteerMethod, recordParams(t, "s1", SteerModeRecord, "what is X?", "X is secret-42", "fork-1"))
	if rerr != nil {
		t.Fatalf("record: %v", rerr)
	}
	if n := sess.Agent.SteeringQueueLen(); n != 0 {
		t.Fatalf("steering queue = %d, want 0", n)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("prompt: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("prompt never returned")
	}
	checkSideQueryEntry(t, sess, resp, SideQueryData{Question: "what is X?", Answer: "X is secret-42", ForkSessionId: "fork-1"})
	for _, c := range contexts() {
		if contextHasUserText(c, "secret-42") {
			t.Fatal("record-mode entry reached the LLM")
		}
	}
	// A transcript rebuild (resume) must not surface it either, and the
	// leaf stays forkable.
	raw, _ := json.Marshal(sess.SessionStore.BuildSessionContext())
	if strings.Contains(string(raw), "secret-42") {
		t.Fatal("rebuilt session context contains the side_query")
	}
	if sess.SessionStore.LeafIDIfForkable() == "" {
		t.Fatal("leaf not forkable after record")
	}
}

// TestSteer_RecordIdle: record mode works with no running turn.
func TestSteer_RecordIdle(t *testing.T) {
	sess, _, release, _ := newSteerSession(t)
	close(release)
	pa := &firAgent{conn: newMockConn(), sessions: map[string]*firSession{
		"s1": {termState: newTerminalState(), session: sess},
	}}
	handler := rawMethodHandler(pa, newWriteNotifier(io.Discard))
	resp, rerr := handler(context.Background(), SteerMethod, recordParams(t, "s1", SteerModeRecord, "q", "a", ""))
	if rerr != nil {
		t.Fatalf("record: %v", rerr)
	}
	checkSideQueryEntry(t, sess, resp, SideQueryData{Question: "q", Answer: "a"})
	if sess.SessionStore.LeafIDIfForkable() != resp.(SteerSessionResponse).EntryId {
		t.Fatal("side_query leaf not forkable")
	}

	if _, rerr := handler(context.Background(), SteerMethod, recordParams(t, "s1", SteerModeRecord, "q", "", "")); rerr == nil || rerr.Code != -32602 {
		t.Fatalf("missing answer err = %v", rerr)
	}
	if _, rerr := handler(context.Background(), SteerMethod, recordParams(t, "s1", "bogus", "q", "a", "")); rerr == nil || rerr.Code != -32602 {
		t.Fatalf("unknown mode err = %v", rerr)
	}
	// Explicit "steer" mode behaves as the default.
	if _, rerr := handler(context.Background(), SteerMethod, recordParams(t, "s1", SteerModeSteer, "", "", "")); rerr == nil || rerr.Code != -32602 {
		t.Fatalf("explicit steer with empty prompt err = %v", rerr)
	}
}

// TestSteer_DuringCancelUnwind: once session/cancel was sent, the run is
// still unwinding (IsStreaming) but a steer would be drained into the
// aborted run and lost. It must be refused with NoActiveTurnError.
func TestSteer_DuringCancelUnwind(t *testing.T) {
	sess, started, unwind := newCancelSession(t)
	pa := &firAgent{conn: newMockConn(), sessions: map[string]*firSession{
		"s1": {termState: newTerminalState(), session: sess},
	}}
	handler := rawMethodHandler(pa, newWriteNotifier(io.Discard))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = pa.Prompt(context.Background(), acpsdk.PromptRequest{SessionId: "s1", Prompt: textPrompt("start")})
	}()
	<-started
	if err := pa.Cancel(context.Background(), acpsdk.CancelNotification{SessionId: "s1"}); err != nil {
		t.Fatal(err)
	}
	_, rerr := handler(context.Background(), SteerMethod, steerParams(t, "s1", "too late"))
	close(unwind)
	<-done
	if rerr == nil || rerr.Code != NoActiveTurnError {
		t.Fatalf("err = %v, want code %d", rerr, NoActiveTurnError)
	}
}
