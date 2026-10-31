package acp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/resources"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
)

// newErrorSession builds a session whose model always fails with errText.
func newErrorSession(t *testing.T, errText string) *session.AgentSession {
	t.Helper()
	model := &ai.Model{Provider: "test", ID: "test-model", Name: "Test", ContextWindow: 100000}
	streamFn := func(_ *ai.Model, _ ai.Context, _ *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			msg := &ai.AssistantMessage{
				Role: ai.RoleAssistant, Provider: model.Provider, Model: model.ID,
				StopReason: ai.StopReasonError, ErrorMessage: errText, Timestamp: time.Now().UnixMilli(),
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: msg})
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Error: msg})
			stream.End(nil)
		}()
		return stream
	}
	cwd, agentDir := t.TempDir(), t.TempDir()
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
	return sess
}

func promptWithError(t *testing.T, errText string) (error, *mockConn) {
	t.Helper()
	sess := newErrorSession(t, errText)
	mc := newMockConn()
	entry := &firSession{termState: newTerminalState(), session: sess, plan: &planTracker{conn: mc, sessionID: "s1"}}
	pa := &firAgent{conn: mc, sessions: map[string]*firSession{"s1": entry}}
	unsub := sess.Subscribe(func(ev session.AgentSessionEvent) { pa.handleEvent("s1", entry, ev) })
	t.Cleanup(unsub)
	_, err := pa.Prompt(context.Background(), acpsdk.PromptRequest{SessionId: "s1", Prompt: textPrompt("hi")})
	return err, mc
}

func TestPrompt_ProviderRejection_FailsTurn(t *testing.T) {
	const errText = "402 Budget exceeded: vk_model_config * budget exceeded"
	err, mc := promptWithError(t, errText)
	var re *acpsdk.RequestError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want *RequestError", err)
	}
	if re.Code != -32603 || re.Message != errText {
		t.Errorf("got code=%d message=%q", re.Code, re.Message)
	}
	raw, _ := json.Marshal(mc.getUpdates())
	if strings.Contains(string(raw), "Budget exceeded") {
		t.Errorf("rejection leaked as answer text: %s", raw)
	}
}

func TestPrompt_ProviderRejection401_AuthRequired(t *testing.T) {
	err, _ := promptWithError(t, "401 invalid x-api-key")
	var re *acpsdk.RequestError
	if !errors.As(err, &re) || re.Code != acpsdk.NewAuthRequired(nil).Code {
		t.Fatalf("err = %#v, want AUTH_REQUIRED", err)
	}
}

func TestPrompt_OtherError_StaysAnswerText(t *testing.T) {
	err, mc := promptWithError(t, "400 bad request: malformed")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	raw, _ := json.Marshal(mc.getUpdates())
	if !strings.Contains(string(raw), "bad request") {
		t.Errorf("expected error text update, got %s", raw)
	}
}
