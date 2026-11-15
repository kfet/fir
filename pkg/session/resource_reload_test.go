package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kfet/agent"
)

func writeSkill(t *testing.T, cwd, name string) {
	t.Helper()
	dir := filepath.Join(cwd, ".fir", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: test skill " + name + "\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func lastUserText(s *AgentSession) string {
	msgs := s.Agent.State().Messages
	for i := len(msgs) - 1; i >= 0; i-- {
		if u := msgs[i].AsUser(); u != nil {
			txt, _ := u.Content.(string)
			return txt
		}
	}
	return ""
}

func TestRequestResourceReload_IdleRunsNowAndReports(t *testing.T) {
	s, cwd := newTestAgentSession(t)
	defer s.Close()

	writeSkill(t, cwd, "freshskill")
	s.RequestResourceReload()

	if !strings.Contains(s.GetSystemPrompt(), "freshskill") {
		t.Fatal("system prompt should list the new skill")
	}
	msg := lastUserText(s)
	if !strings.Contains(msg, "[SYS_EXT]") || !strings.Contains(msg, "Skills added: freshskill") {
		t.Fatalf("expected change summary, got %q", msg)
	}
	if s.resourceReloadPending.Load() {
		t.Fatal("pending flag must be consumed")
	}

	// Nothing changed: prompt kept as-is, summary says so.
	before := s.baseSystemPrompt
	s.RequestResourceReload()
	if s.baseSystemPrompt != before {
		t.Fatal("unchanged prompt must be kept")
	}
	if msg := lastUserText(s); !strings.Contains(msg, "No skill or model changes") {
		t.Fatalf("expected no-change summary, got %q", msg)
	}

	// Removal is reported too.
	if err := os.RemoveAll(filepath.Join(cwd, ".fir", "skills", "freshskill")); err != nil {
		t.Fatal(err)
	}
	s.RequestResourceReload()
	if msg := lastUserText(s); !strings.Contains(msg, "Skills removed: freshskill") {
		t.Fatalf("expected removal summary, got %q", msg)
	}
}

func TestRequestResourceReload_DeferredRunsOnAgentEnd(t *testing.T) {
	s, cwd := newTestAgentSession(t)
	defer s.Close()

	writeSkill(t, cwd, "laterskill")
	// Simulate a request made mid-turn: flag set, reload not yet run.
	s.resourceReloadPending.Store(true)
	if strings.Contains(s.GetSystemPrompt(), "laterskill") {
		t.Fatal("reload must not run before the turn ends")
	}
	s.handleAgentEvent(agent.AgentEvent{Type: agent.EventAgentEnd})
	if !strings.Contains(s.GetSystemPrompt(), "laterskill") {
		t.Fatal("reload should run at agent_end")
	}
	if s.resourceReloadPending.Load() {
		t.Fatal("pending flag must be consumed")
	}
	// A second agent_end without a request does nothing.
	n := len(s.Agent.State().Messages)
	s.handleAgentEvent(agent.AgentEvent{Type: agent.EventAgentEnd})
	if len(s.Agent.State().Messages) != n {
		t.Fatal("no reload without a request")
	}
}

func TestDiffSets(t *testing.T) {
	a := map[string]struct{}{"x": {}, "y": {}}
	b := map[string]struct{}{"y": {}, "z": {}}
	add, del := diffSets(a, b)
	if strings.Join(add, ",") != "z" || strings.Join(del, ",") != "x" {
		t.Fatalf("got add=%v del=%v", add, del)
	}
}

func TestRequestResourceReload_ReportsOnlyNewDiagnostics(t *testing.T) {
	s, cwd := newTestAgentSession(t)
	defer s.Close()

	dir := filepath.Join(cwd, ".fir", "skills", "broken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: broken\n---\nno description\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.RequestResourceReload()
	if msg := lastUserText(s); !strings.Contains(msg, "Errors:") {
		t.Fatalf("expected the new diagnostic, got %q", msg)
	}
	s.RequestResourceReload()
	if msg := lastUserText(s); strings.Contains(msg, "Errors:") {
		t.Fatalf("standing diagnostic must not repeat, got %q", msg)
	}
}
