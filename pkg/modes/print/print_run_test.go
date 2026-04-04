package print_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/config"
	"github.com/kfet/fir/pkg/models"
	printmode "github.com/kfet/fir/pkg/modes/print"
	"github.com/kfet/fir/pkg/resources"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
)

// newPrintTestSession creates a minimal AgentSession with no model attached,
// suitable for testing the print.Run function paths that don't call Prompt.
func newPrintTestSession(t *testing.T) *session.AgentSession {
	t.Helper()
	cwd := t.TempDir()
	agentDir := t.TempDir()

	sm := store.NewSessionStore(cwd, filepath.Join(agentDir, "sessions"))
	settingsManager := config.NewSettingsManager(cwd, agentDir)

	rl := resources.NewResourceLoader(resources.ResourceLoaderOptions{
		Cwd:      cwd,
		AgentDir: agentDir,
	})
	rl.Reload()

	modelRegistry := models.NewModelRegistry(auth.NewAuthStorage(filepath.Join(agentDir, "auth.json")), "")

	a := agent.NewAgent(agent.AgentOptions{
		InitialState: &agent.AgentState{
			SystemPrompt:  "test",
			ThinkingLevel: "off",
		},
		ConvertToLLM: func(msgs []agent.AgentMessage) ([]ai.Message, error) {
			return nil, nil
		},
	})

	sess := session.NewAgentSession(session.AgentSessionOptions{
		Agent:           a,
		SessionStore:    sm,
		SettingsManager: settingsManager,
		ResourceLoader:  rl,
		ModelRegistry:   modelRegistry,
		Cwd:             cwd,
	})
	t.Cleanup(sess.Close)
	return sess
}

// captureStdout redirects os.Stdout to a pipe, calls f(), and returns what was written.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w
	f()
	w.Close()
	os.Stdout = old
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	r.Close()
	return string(data)
}

// captureStderr redirects os.Stderr to a pipe, calls f(), and returns what was written.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = w
	f()
	w.Close()
	os.Stderr = old
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	r.Close()
	return string(data)
}

// TestRun_TextMode_EmptyMessages verifies Run returns nil when there are no messages.
func TestRun_TextMode_EmptyMessages(t *testing.T) {
	sess := newPrintTestSession(t)
	err := printmode.Run(sess, printmode.Options{Mode: printmode.ModeText})
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}

// TestRun_TextMode_LastAssistantMessage verifies that Run outputs the final
// text block from the last assistant message to stdout.
func TestRun_TextMode_LastAssistantMessage(t *testing.T) {
	sess := newPrintTestSession(t)

	sess.Agent.ReplaceMessages([]agent.AgentMessage{
		agent.NewAgentMessage(ai.NewAssistantMsg(ai.AssistantMessage{
			StopReason: ai.StopReasonStop,
			Content: []ai.AssistantContent{
				{Text: &ai.TextContent{Text: "hello from agent"}},
			},
		})),
	})

	var runErr error
	out := captureStdout(t, func() {
		runErr = printmode.Run(sess, printmode.Options{Mode: printmode.ModeText})
	})
	if runErr != nil {
		t.Fatalf("expected nil error, got: %v", runErr)
	}
	if !strings.Contains(out, "hello from agent") {
		t.Errorf("expected stdout to contain %q, got %q", "hello from agent", out)
	}
}

// TestRun_TextMode_AbortedStopReason verifies that Run writes the error to
// stderr and returns ErrAgentAborted when the stop reason is "aborted".
func TestRun_TextMode_AbortedStopReason(t *testing.T) {
	sess := newPrintTestSession(t)

	sess.Agent.ReplaceMessages([]agent.AgentMessage{
		agent.NewAgentMessage(ai.NewAssistantMsg(ai.AssistantMessage{
			StopReason:   ai.StopReasonAborted,
			ErrorMessage: "something went wrong",
		})),
	})

	var runErr error
	errOut := captureStderr(t, func() {
		runErr = printmode.Run(sess, printmode.Options{Mode: printmode.ModeText})
	})

	if !errors.Is(runErr, printmode.ErrAgentAborted) {
		t.Errorf("expected ErrAgentAborted, got: %v", runErr)
	}
	if !strings.Contains(errOut, "something went wrong") {
		t.Errorf("expected stderr to contain error message, got %q", errOut)
	}
}

// TestRun_TextMode_ErrorStopReason_DefaultMessage verifies that Run uses the
// fallback error message when ErrorMessage is empty.
func TestRun_TextMode_ErrorStopReason_DefaultMessage(t *testing.T) {
	sess := newPrintTestSession(t)

	sess.Agent.ReplaceMessages([]agent.AgentMessage{
		agent.NewAgentMessage(ai.NewAssistantMsg(ai.AssistantMessage{
			StopReason:   ai.StopReasonError,
			ErrorMessage: "", // empty — should produce default message
		})),
	})

	var runErr error
	errOut := captureStderr(t, func() {
		runErr = printmode.Run(sess, printmode.Options{Mode: printmode.ModeText})
	})

	if !errors.Is(runErr, printmode.ErrAgentAborted) {
		t.Errorf("expected ErrAgentAborted, got: %v", runErr)
	}
	if !strings.Contains(errOut, "error") {
		t.Errorf("expected stderr to mention stop reason, got %q", errOut)
	}
}

// TestRun_JSONMode_SubscribesAndReceivesEvents verifies that JSON mode runs
// successfully with no messages (subscription set up, no Prompt called).
func TestRun_JSONMode_SubscribesAndReceivesEvents(t *testing.T) {
	sess := newPrintTestSession(t)

	var runErr error
	out := captureStdout(t, func() {
		runErr = printmode.Run(sess, printmode.Options{Mode: printmode.ModeJSON})
	})

	if runErr != nil {
		t.Fatalf("expected nil error, got: %v", runErr)
	}
	// No events fired → no JSON lines expected.
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected empty stdout in JSON mode with no events, got %q", out)
	}
}

// TestRun_JSONMode_EventsEmittedDuringRun verifies that the AgentSession's
// Subscribe/PublishEvent mechanism works correctly — a prerequisite for the
// JSON-mode subscriber in Run receiving events.
func TestRun_JSONMode_EventsEmittedDuringRun(t *testing.T) {
	sess := newPrintTestSession(t)

	subCount := 0
	unsub := sess.Subscribe(func(_ session.AgentSessionEvent) { subCount++ })
	defer unsub()

	sess.PublishEvent(session.AgentSessionEvent{})
	if subCount != 1 {
		t.Fatalf("subscription not working: got count %d, want 1", subCount)
	}

	// JSON mode with no messages returns nil.
	var runErr error
	out := captureStdout(t, func() {
		runErr = printmode.Run(sess, printmode.Options{Mode: printmode.ModeJSON})
	})
	if runErr != nil {
		t.Fatalf("unexpected error: %v", runErr)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected empty stdout in JSON mode with no events, got %q", out)
	}
}
