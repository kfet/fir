package acp

import (
	"context"
	"path/filepath"
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

// newCancelSession builds a session whose scripted model calls slow_tool on
// its first request and answers "ok" afterwards. slow_tool signals started,
// waits for the turn to be aborted, then holds the aborted run until unwind
// is closed — the window in which a relay that gave up on the cancelled
// prompt sends its next one.
func newCancelSession(t *testing.T) (*session.AgentSession, chan struct{}, chan struct{}) {
	t.Helper()
	model := &ai.Model{Provider: "test", ID: "test-model", Name: "Test", ContextWindow: 100000}
	var mu sync.Mutex
	calls := 0
	aborted := false
	streamFn := func(_ *ai.Model, _ ai.Context, _ *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		mu.Lock()
		calls++
		first := calls == 1
		afterAbort := aborted
		aborted = false
		mu.Unlock()
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			msg := &ai.AssistantMessage{
				Role: ai.RoleAssistant, Provider: model.Provider, Model: model.ID,
				StopReason: ai.StopReasonStop, Timestamp: time.Now().UnixMilli(),
			}
			switch {
			case first:
				msg.StopReason = ai.StopReasonToolUse
				msg.Content = []ai.AssistantContent{{ToolCall: &ai.ToolCall{
					Type: ai.ContentTypeToolCall, ID: "tc-slow-1", Name: "slow_tool", Arguments: map[string]any{},
				}}}
			case afterAbort:
				msg.StopReason = ai.StopReasonAborted
				msg.ErrorMessage = "aborted"
			default:
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
	sess := session.NewAgentSession(session.AgentSessionOptions{
		Agent:          a,
		SessionStore:   store.NewSessionStore(cwd, filepath.Join(agentDir, "sessions")),
		ResourceLoader: resources.NewResourceLoader(resources.ResourceLoaderOptions{Cwd: cwd, AgentDir: agentDir}),
		Cwd:            cwd,
	})
	t.Cleanup(sess.Close)
	sess.SessionStore.NewSession(nil)

	bridge := extension.NewSessionBridge(sess)
	started := make(chan struct{})
	unwind := make(chan struct{})
	var once sync.Once
	bridge.RegisterTool(extension.ToolDefinition{
		Name: "slow_tool", Description: "blocks until aborted",
		Parameters: map[string]any{"type": "object"},
		Execute: func(tc extension.ToolContext) (extension.ToolResult, error) {
			once.Do(func() { close(started) })
			<-tc.Context.Done()
			mu.Lock()
			aborted = true
			mu.Unlock()
			<-unwind
			return extension.ToolResult{IsError: true, Content: []ai.ToolResultContent{{Type: ai.ContentTypeText, Text: "context canceled"}}}, nil
		},
	})
	return sess, started, unwind
}

func textPrompt(s string) []acpsdk.ContentBlock {
	return []acpsdk.ContentBlock{acpsdk.TextBlock(s)}
}

// TestPrompt_AfterCancel_WaitsAndAnswers is the regression test for a
// mid-turn message lost on a cancel: a session/prompt that arrives while
// the cancelled run is still unwinding must not be queued as a follow-up
// and answered with an empty end_turn. It must wait for the run to unwind
// and then get a real turn of its own.
func TestPrompt_AfterCancel_WaitsAndAnswers(t *testing.T) {
	sess, started, unwind := newCancelSession(t)
	pa := &firAgent{conn: newMockConn(), sessions: map[string]*firSession{
		"s1": {termState: newTerminalState(), session: sess},
	}}
	ctx := context.Background()

	first := make(chan struct{})
	go func() {
		defer close(first)
		_, _ = pa.Prompt(ctx, acpsdk.PromptRequest{SessionId: "s1", Prompt: textPrompt("start the slow tool")})
	}()
	select {
	case <-started:
	case <-time.After(15 * time.Second):
		t.Fatal("slow_tool never started")
	}
	if err := pa.Cancel(ctx, acpsdk.CancelNotification{SessionId: "s1"}); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	second := make(chan error, 1)
	go func() {
		_, err := pa.Prompt(ctx, acpsdk.PromptRequest{SessionId: "s1", Prompt: textPrompt("the steer")})
		second <- err
	}()
	select {
	case err := <-second:
		t.Fatalf("second prompt returned (err=%v) while the cancelled run was still unwinding", err)
	case <-time.After(300 * time.Millisecond):
	}
	if n := sess.Agent.FollowUpQueueLen(); n != 0 {
		t.Fatalf("second prompt was queued as a follow-up of the aborted run (%d queued)", n)
	}
	close(unwind)
	<-first
	select {
	case err := <-second:
		if err != nil {
			t.Fatalf("second prompt: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("second prompt never returned")
	}

	msgs := sess.Agent.State().Messages
	last := msgs[len(msgs)-1].AsAssistant()
	if last == nil || last.StopReason != ai.StopReasonStop || len(last.Content) == 0 || last.Content[0].Text == nil || last.Content[0].Text.Text != "ok" {
		t.Fatalf("second prompt did not get a real answer; last message = %#v", last)
	}
}

// TestPrompt_WaitForIdle_ContextDone covers the client giving up while the
// previous run is still unwinding: the prompt returns the context error and
// starts no turn.
func TestPrompt_WaitForIdle_ContextDone(t *testing.T) {
	sess, started, unwind := newCancelSession(t)
	defer close(unwind)
	pa := &firAgent{conn: newMockConn(), sessions: map[string]*firSession{
		"s1": {termState: newTerminalState(), session: sess},
	}}
	go func() {
		_, _ = pa.Prompt(context.Background(), acpsdk.PromptRequest{SessionId: "s1", Prompt: textPrompt("start the slow tool")})
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := pa.Prompt(ctx, acpsdk.PromptRequest{SessionId: "s1", Prompt: textPrompt("late")}); err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	_ = pa.Cancel(context.Background(), acpsdk.CancelNotification{SessionId: "s1"})
}
