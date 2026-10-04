package session

import (
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/kfet/agent"
	"github.com/kfet/ai"
)

// statusCard returns the session/status card's slug and parsed detail lines.
func statusCard(t *testing.T, s *AgentSession) (string, map[string]string) {
	t.Helper()
	for _, c := range s.Observables().List() {
		if c.Source == "session" && c.Key == "status" {
			kv := map[string]string{}
			for _, ln := range strings.Split(c.Detail, "\n") {
				k, v, ok := strings.Cut(ln, ": ")
				if ok {
					kv[k] = v
				}
			}
			return c.Slug, kv
		}
	}
	t.Fatal("no session/status card published")
	return "", nil
}

func assistantEnd(stop ai.StopReason, errMsg string) agent.AgentEvent {
	m := agent.NewAgentMessage(ai.NewAssistantMsg(ai.AssistantMessage{
		Role:         "assistant",
		StopReason:   stop,
		ErrorMessage: errMsg,
	}))
	return agent.AgentEvent{Type: agent.EventMessageEnd, Message: &m}
}

func TestStatusCard_InitialNoModel(t *testing.T) {
	s, _ := newTestAgentSession(t) // no model configured
	defer s.Close()

	slug, kv := statusCard(t, s)
	if slug != StatusNoModel || kv["status"] != StatusNoModel {
		t.Fatalf("slug=%q status=%q; want no-model", slug, kv["status"])
	}

	s.SetStatusNotice("No models available. Use /login or set an API key environment variable.")
	_, kv = statusCard(t, s)
	if !strings.Contains(kv["notice"], "No models available") {
		t.Errorf("notice = %q; want startup message surfaced", kv["notice"])
	}

	// A prompt against a model-less session is refused before the agent
	// loop runs — the refusal must still reach observers.
	if err := s.Prompt("hello"); err == nil {
		t.Fatal("expected prompt to fail without a model")
	}
	_, kv = statusCard(t, s)
	if kv["status"] != StatusNoModel || !strings.Contains(kv["error"], "no model selected") {
		t.Errorf("after refused prompt: %#v", kv)
	}
}

func TestStatusCard_Lifecycle(t *testing.T) {
	s := newTestAgentSessionWithModel(t, nil)
	defer s.Close()

	slug, kv := statusCard(t, s)
	if slug != StatusIdle || kv["model"] != "test-provider/test-model" || kv["runs"] != "0" {
		t.Fatalf("initial: slug=%q kv=%#v", slug, kv)
	}

	s.handleAgentEvent(agent.AgentEvent{Type: agent.EventAgentStart})
	if slug, _ = statusCard(t, s); slug != StatusRunning {
		t.Fatalf("after agent_start slug=%q; want running", slug)
	}

	s.handleAgentEvent(agent.AgentEvent{Type: agent.EventToolExecutionStart, ToolName: "bash"})
	slug, kv = statusCard(t, s)
	if slug != "running: bash" || kv["tool"] != "bash" || kv["status"] != StatusRunning {
		t.Fatalf("during tool: slug=%q kv=%#v", slug, kv)
	}
	s.handleAgentEvent(agent.AgentEvent{Type: agent.EventToolExecutionEnd, ToolName: "bash"})
	if _, kv = statusCard(t, s); kv["tool"] != "" {
		t.Errorf("tool not cleared: %#v", kv)
	}

	s.handleAgentEvent(assistantEnd(ai.StopReasonStop, ""))
	s.handleAgentEvent(agent.AgentEvent{Type: agent.EventAgentEnd})
	slug, kv = statusCard(t, s)
	if slug != StatusIdle || kv["runs"] != "1" || kv["error"] != "" {
		t.Fatalf("after clean run: slug=%q kv=%#v", slug, kv)
	}
}

// TestStatusCard_ProviderError is the regression for the remote-observe
// incident: an auth failure ended every turn instantly, and observers saw an
// "idle" session with an empty assistant line instead of the error.
func TestStatusCard_ProviderError(t *testing.T) {
	s := newTestAgentSessionWithModel(t, nil)
	defer s.Close()

	const authErr = `no API key for provider "anthropic": Refresh token expired. Run '/login anthropic'`
	s.handleAgentEvent(agent.AgentEvent{Type: agent.EventAgentStart})
	s.handleAgentEvent(assistantEnd(ai.StopReasonError, authErr))
	s.handleAgentEvent(agent.AgentEvent{Type: agent.EventAgentEnd})

	slug, kv := statusCard(t, s)
	if slug != StatusError || kv["error"] != authErr {
		t.Fatalf("slug=%q kv=%#v; want error with provider message", slug, kv)
	}
	if s.Status() != StatusError {
		t.Errorf("Status() = %q", s.Status())
	}

	// The next run clears the error.
	s.handleAgentEvent(agent.AgentEvent{Type: agent.EventAgentStart})
	if _, kv = statusCard(t, s); kv["error"] != "" || kv["status"] != StatusRunning {
		t.Errorf("error not cleared on new run: %#v", kv)
	}
}

func TestStatusCard_RecoveredRetryAndAbort(t *testing.T) {
	s := newTestAgentSessionWithModel(t, nil)
	defer s.Close()

	s.handleAgentEvent(agent.AgentEvent{Type: agent.EventAgentStart})
	s.handleAgentEvent(agent.AgentEvent{Type: agent.EventAutoResume, ErrorMessage: "connection reset"})
	if _, kv := statusCard(t, s); kv["status"] != StatusRunning || !strings.Contains(kv["error"], "retrying: connection reset") {
		t.Fatalf("during retry: %#v", kv)
	}
	s.handleAgentEvent(assistantEnd(ai.StopReasonStop, ""))
	s.handleAgentEvent(agent.AgentEvent{Type: agent.EventAgentEnd})
	if slug, _ := statusCard(t, s); slug != StatusIdle {
		t.Errorf("recovered run should end idle, got %q", slug)
	}

	// A user abort is not an error state.
	s.handleAgentEvent(agent.AgentEvent{Type: agent.EventAgentStart})
	s.handleAgentEvent(assistantEnd(ai.StopReasonAborted, ""))
	s.handleAgentEvent(agent.AgentEvent{Type: agent.EventAgentEnd})
	if slug, kv := statusCard(t, s); slug != StatusIdle || kv["runs"] != "2" {
		t.Errorf("after abort slug=%q kv=%#v", slug, kv)
	}

	// An abort mid-request persists stopReason=error with a (wrapped)
	// "context canceled" — observed live: `http request: Post "…": context
	// canceled`. With an abort requested that is not an error state…
	const canceled = `http request: Post "https://api.anthropic.com/v1/messages": context canceled`
	s.handleAgentEvent(agent.AgentEvent{Type: agent.EventAgentStart})
	s.Abort()
	s.handleAgentEvent(assistantEnd(ai.StopReasonError, canceled))
	s.handleAgentEvent(agent.AgentEvent{Type: agent.EventAgentEnd})
	if slug, kv := statusCard(t, s); slug != StatusIdle || kv["error"] != "" {
		t.Errorf("after mid-request abort slug=%q kv=%#v", slug, kv)
	}

	// …but the same text with no abort requested is a real failure, and
	// the abort flag does not leak into the next run.
	s.handleAgentEvent(agent.AgentEvent{Type: agent.EventAgentStart})
	s.handleAgentEvent(assistantEnd(ai.StopReasonError, canceled))
	s.handleAgentEvent(agent.AgentEvent{Type: agent.EventAgentEnd})
	if slug, kv := statusCard(t, s); slug != StatusError || kv["error"] != canceled {
		t.Errorf("unrequested cancellation should be an error: slug=%q kv=%#v", slug, kv)
	}
}

func TestStatusCard_ModelSelectedClearsNoModel(t *testing.T) {
	s, _ := newTestAgentSession(t)
	defer s.Close()
	s.statusPromptRefused(StatusNoModel, errors.New("no model selected"))

	if err := s.SetModel(&ai.Model{ID: "m", Provider: "p"}); err != nil {
		t.Fatal(err)
	}
	slug, kv := statusCard(t, s)
	if slug != StatusIdle || kv["model"] != "p/m" || kv["error"] != "" {
		t.Fatalf("after SetModel: slug=%q kv=%#v", slug, kv)
	}
}

func TestStatusCard_NewSessionRepublishes(t *testing.T) {
	s, _ := newTestAgentSession(t)
	defer s.Close()
	before := s.Observables()
	if _, err := s.NewSessionCmd(); err != nil {
		t.Fatal(err)
	}
	if s.Observables() == before {
		t.Fatal("expected /new to bind a fresh cards store")
	}
	if slug, _ := statusCard(t, s); slug != StatusNoModel {
		t.Errorf("status card missing after /new: %q", slug)
	}
}

// TestStatusCard_PromptAcceptedIsBusyBeforeAgentStart: the first prompt can
// block for seconds on extension startup; observers must see "running"
// immediately, not a stale "idle".
func TestStatusCard_PromptAcceptedIsBusyBeforeAgentStart(t *testing.T) {
	s := newTestAgentSessionWithModel(t, nil)
	defer s.Close()
	ready := make(chan struct{})
	s.extReady = ready

	done := make(chan error, 1)
	go func() { done <- s.Prompt("hi") }()

	// Prompt publishes "running" before it blocks on extReady; observe it
	// through the card store without sleeping.
	for {
		if slug, _ := statusCard(t, s); slug == StatusRunning {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("Prompt returned before extensions were ready: %v", err)
		default:
		}
		runtime.Gosched()
	}
	close(ready)
	<-done // the run fails fast (no stream fn); status settles again
	if slug, _ := statusCard(t, s); slug == StatusRunning {
		t.Errorf("status stuck at running after the run ended")
	}
}

func TestStatusCard_SkipsIdenticalRewrites(t *testing.T) {
	s := newTestAgentSessionWithModel(t, nil)
	defer s.Close()
	s.handleAgentEvent(agent.AgentEvent{Type: agent.EventAgentStart})
	first := s.Observables().List()[0].Ts
	// Successful assistant messages change nothing visible.
	s.handleAgentEvent(assistantEnd(ai.StopReasonToolUse, ""))
	s.handleAgentEvent(assistantEnd(ai.StopReasonStop, ""))
	if got := s.Observables().List()[0].Ts; !got.Equal(first) {
		t.Errorf("card rewritten without a change: %v -> %v", first, got)
	}
}

func TestStatusCard_ModelChangeClearsStartupNotice(t *testing.T) {
	s := newTestAgentSessionWithModel(t, nil)
	defer s.Close()
	s.SetStatusNotice("Could not restore model a/b. Using test-provider/test-model")
	if _, kv := statusCard(t, s); kv["notice"] == "" {
		t.Fatal("notice not published")
	}
	if err := s.SetModel(&ai.Model{ID: "m2", Provider: "p"}); err != nil {
		t.Fatal(err)
	}
	if _, kv := statusCard(t, s); kv["notice"] != "" || kv["model"] != "p/m2" {
		t.Errorf("after explicit model change: %#v", kv)
	}
}

// TestStatusCard_RepeatedRefusalRestamps: the second identical refusal must
// still advance the card timestamp, or `fir send --wait` never notices it.
func TestStatusCard_RepeatedRefusalRestamps(t *testing.T) {
	s, _ := newTestAgentSession(t)
	defer s.Close()
	_ = s.Prompt("one")
	first := s.Observables().List()[0].Ts
	_ = s.Prompt("two")
	if second := s.Observables().List()[0].Ts; !second.After(first) {
		t.Errorf("second refusal did not restamp the card: %v then %v", first, second)
	}
}
