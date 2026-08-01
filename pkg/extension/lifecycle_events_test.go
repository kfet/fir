package extension

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/store"
)

// lifecycleProbeScript is a minimal extension that appends every lifecycle
// event it receives to $PROBE_LOG, one name per line.
const lifecycleProbeScript = `#!/usr/bin/env python3
# ---
# name: lifecycle-probe
# ---
import os
import fir_ext

LOG = os.environ["PROBE_LOG"]

def record(name):
    with open(LOG, "a") as fh:
        fh.write(name + "\n")
        fh.flush()

for _event in ("agent_start", "agent_end", "turn_start", "turn_end"):
    fir_ext.on(_event)(lambda params, ctx, _n=_event: record(_n))

fir_ext.run(name="lifecycle-probe")
`

// newLifecycleProbeProject creates a temp project containing the lifecycle
// probe extension and points PROBE_LOG at a log file inside it. It returns the
// project dir and the log path.
func newLifecycleProbeProject(t *testing.T) (string, string) {
	t.Helper()
	projectDir := t.TempDir()
	extDir := filepath.Join(projectDir, ".fir", "extensions")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "lifecycle_probe.py"), []byte(lifecycleProbeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(projectDir, "events.log")
	t.Setenv("PROBE_LOG", logPath)
	return projectDir, logPath
}

// newLifecycleTestSession builds an AgentSession whose agent streams a single
// canned assistant reply, so Prompt() drives a full agent loop without any
// network access. streamGate, when non-nil, is invoked at the start of every
// stream so a test can control turn timing.
func newLifecycleTestSession(t *testing.T, cwd string, streamGate func()) *session.AgentSession {
	t.Helper()
	model := &ai.Model{Provider: "test", ID: "test-model", Name: "Test", ContextWindow: 100000}
	a := agent.NewAgent(agent.AgentOptions{
		InitialState: &agent.AgentState{Model: model},
		StreamFn: func(m *ai.Model, _ ai.Context, _ *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
			stream := ai.NewAssistantMessageEventStream()
			go func() {
				if streamGate != nil {
					streamGate()
				}
				msg := &ai.AssistantMessage{
					Role:       ai.RoleAssistant,
					Content:    []ai.AssistantContent{{Text: &ai.TextContent{Type: "text", Text: "ok"}}},
					API:        m.API,
					Provider:   m.Provider,
					Model:      m.ID,
					StopReason: ai.StopReasonStop,
				}
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventStart, Partial: msg})
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Message: msg})
				stream.End(nil)
			}()
			return stream
		},
		GetAPIKey: func(string) (string, error) { return "test-key", nil },
	})
	return session.NewAgentSession(session.AgentSessionOptions{
		Agent:          a,
		SessionStore:   store.InMemorySessionStore(),
		ResourceLoader: &stubResourceLoader{},
		Cwd:            cwd,
	})
}

// TestSetupForwardsAgentLifecycleEvents verifies that the lifecycle events
// emitted by the agent core (agent_start / agent_end and the surrounding turn
// events) reach extensions in every mode, not just the interactive TUI.
// Regression test for "agent_start never fires under --mode acp": the
// forwarding lives in Setup (mode-independent), so it must hold for any
// SetupOptions.Mode.
func TestSetupForwardsAgentLifecycleEvents(t *testing.T) {
	requirePython(t)

	for _, mode := range []string{"interactive", "acp"} {
		t.Run(mode, func(t *testing.T) {
			projectDir, logPath := newLifecycleProbeProject(t)

			asession := newLifecycleTestSession(t, projectDir, nil)
			t.Cleanup(asession.Close)

			result := setupLifecycleProbe(t, asession, projectDir, mode)
			t.Cleanup(result.EmitSessionShutdown)

			if err := asession.Prompt("hello"); err != nil {
				t.Fatalf("Prompt: %v", err)
			}

			want := map[string]int{"agent_start": 1, "turn_start": 1, "turn_end": 1, "agent_end": 1}
			if got, ok := waitForEventCounts(logPath, want); !ok {
				t.Fatalf("mode %s: want at least %v lifecycle events, got %v", mode, want, got)
			}
		})
	}
}

// TestSetupFollowUpTurnEmitsNoAgentStart pins the semantics of agent_start:
// it marks the start of an *agent loop*, not of a user prompt. A prompt that
// arrives while the agent is streaming is folded into the running loop as a
// follow-up, so it produces a second turn_start/turn_end pair but no second
// agent_start — and agent_start/agent_end stay paired. ACP clients that
// pipeline prompts hit this path, which is why it is guarded here.
func TestSetupFollowUpTurnEmitsNoAgentStart(t *testing.T) {
	requirePython(t)

	projectDir, logPath := newLifecycleProbeProject(t)

	var once sync.Once
	firstStreamStarted := make(chan struct{})
	releaseFirstStream := make(chan struct{})
	gate := func() {
		first := false
		once.Do(func() { first = true })
		if !first {
			return
		}
		close(firstStreamStarted)
		<-releaseFirstStream
	}

	asession := newLifecycleTestSession(t, projectDir, gate)
	t.Cleanup(asession.Close)

	result := setupLifecycleProbe(t, asession, projectDir, "acp")
	t.Cleanup(result.EmitSessionShutdown)

	firstDone := make(chan error, 1)
	go func() { firstDone <- asession.Prompt("first") }()

	<-firstStreamStarted

	// The agent is streaming: this prompt is queued as a follow-up and runs
	// as a second turn inside the same agent loop.
	if err := asession.Prompt("second"); err != nil {
		t.Fatalf("follow-up Prompt: %v", err)
	}
	close(releaseFirstStream)

	if err := <-firstDone; err != nil {
		t.Fatalf("first Prompt: %v", err)
	}
	asession.Agent.WaitForIdle()

	want := map[string]int{"agent_start": 1, "turn_start": 2, "turn_end": 2, "agent_end": 1}
	got, ok := waitForEventCounts(logPath, want)
	if !ok {
		t.Fatalf("want at least %v, got %v", want, got)
	}
	if got["agent_start"] != 1 || got["agent_end"] != 1 {
		t.Errorf("follow-up turn must not produce extra agent_start/agent_end: got %v", got)
	}
}

func requirePython(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
}

func setupLifecycleProbe(t *testing.T, asession *session.AgentSession, projectDir, mode string) *SetupResult {
	t.Helper()
	result, err := Setup(asession, SetupOptions{
		ProjectDir:     projectDir,
		Cwd:            projectDir,
		Mode:           mode,
		TrustStorePath: filepath.Join(t.TempDir(), "trusted.json"),
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil SetupResult")
	}
	return result
}

// waitForEventCounts polls the probe log until every event in want has been
// seen at least the requested number of times. Returns the final counts and
// whether the expectation was met.
func waitForEventCounts(logPath string, want map[string]int) (map[string]int, bool) {
	deadline := time.Now().Add(20 * time.Second)
	var counts map[string]int
	for {
		counts = countEvents(logPath)
		satisfied := true
		for name, n := range want {
			if counts[name] < n {
				satisfied = false
				break
			}
		}
		if satisfied {
			return counts, true
		}
		if time.Now().After(deadline) {
			return counts, false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func countEvents(logPath string) map[string]int {
	counts := map[string]int{}
	data, err := os.ReadFile(logPath)
	if err != nil {
		return counts
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			counts[line]++
		}
	}
	return counts
}
