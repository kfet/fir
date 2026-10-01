package session

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/fir/pkg/session/statetest"
	"github.com/kfet/fir/pkg/session/store"
)

// openForTest opens (or creates, when file is "") a persisted session through
// the core Setup path every mode uses, and closes it at test end.
func openForTest(t *testing.T, cwd, agentDir, file string, override func(*SessionState)) *AgentSession {
	t.Helper()
	sessionDir := store.DefaultSessionDir(agentDir, cwd)
	ss := store.NewSessionStore(cwd, sessionDir)
	if file != "" {
		ss, _ = store.OpenSessionStore(file, sessionDir)
	}
	res, err := Setup(context.Background(), SetupOptions{
		Cwd: cwd, AgentDir: agentDir, SessionStore: ss, StateOverride: override,
		Tools: DefaultCodingTools(cwd),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		res.Session.Close()
		if res.MCPManager != nil {
			_ = res.MCPManager.Close()
		}
	})
	return res.Session
}

// Every field of SessionState — including any added later — survives a save
// and a reopen, with no code naming it.
func TestSessionState_AnyFieldSurvivesReopen(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	// A server that never starts is fine: only its config must survive.
	srv := map[string]mcp.ServerConfig{"srv": {Command: "/nonexistent/mcp", Env: map[string]string{"TOKEN": "secret"}}}
	var want SessionState
	statetest.FillNonZero(t, &want, cwd, srv)

	s := openForTest(t, cwd, agentDir, "", nil)
	s.UpdateSessionState(func(st *SessionState) { *st = want })
	file := s.SessionStore.GetSessionFile()
	s.Close()

	// The handle's owner reclaims it on open; everything else comes back alone.
	back := openForTest(t, cwd, agentDir, file, func(st *SessionState) { st.Runtime.Handle = want.Runtime.Handle })
	if got := back.SessionState(); !reflect.DeepEqual(got, want) {
		t.Fatalf("state after reopen:\n got %#v\nwant %#v", got, want)
	}
}

func TestSessionState_RestoresModelThinkingNameAndMCP(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	s := openForTest(t, cwd, agentDir, "", func(st *SessionState) {
		st.Runtime.McpServers = map[string]mcp.ServerConfig{"client": {Command: "/nonexistent/mcp"}}
	})
	m := s.modelRegistry.Find("anthropic", "claude-sonnet-4-5")
	if m == nil {
		t.Skip("model not in catalog")
	}
	if err := s.SetModel(m); err != nil {
		t.Fatal(err)
	}
	s.SetThinkingLevel("high")
	s.SetSessionName("named")
	file := s.SessionStore.GetSessionFile()
	s.Close()

	back := openForTest(t, cwd, agentDir, file, nil)
	if got := back.Model(); got == nil || got.Provider+"/"+got.ID != "anthropic/claude-sonnet-4-5" {
		t.Errorf("model = %v", got)
	}
	if got := back.ThinkingLevel(); got != "high" {
		t.Errorf("thinking = %q", got)
	}
	if got := back.GetSessionName(); got != "named" {
		t.Errorf("name = %q", got)
	}
	if got := ServerNames(back.SessionMCPServers()); !reflect.DeepEqual(got, []string{"client"}) {
		t.Errorf("session MCP = %v", got)
	}
}

// Switching the running session to another transcript keeps its runtime
// settings and adopts the target transcript's conversation settings.
func TestSessionState_SwitchKeepsRuntime(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	other := openForTest(t, cwd, agentDir, "", nil)
	other.SetSessionName("other")
	otherFile := other.SessionStore.GetSessionFile()
	other.UpdateSessionState(func(st *SessionState) { st.Runtime.Mode = "other-mode" })
	other.Close()

	s := openForTest(t, cwd, agentDir, "", func(st *SessionState) { st.Runtime.Mode = "mine" })
	if _, err := s.SwitchSession(otherFile); err != nil {
		t.Fatal(err)
	}
	st := s.SessionState()
	if st.Runtime.Mode != "mine" {
		t.Errorf("runtime mode = %q, want mine", st.Runtime.Mode)
	}
	if st.Conversation.Name != "other" {
		t.Errorf("conversation name = %q, want other", st.Conversation.Name)
	}
	saved, _ := LoadState(otherFile)
	if saved.Runtime.Mode != "mine" {
		t.Errorf("saved runtime mode = %q, want mine", saved.Runtime.Mode)
	}
}

func TestSessionState_FileMode0600AndHandle(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	s := openForTest(t, cwd, agentDir, "", func(st *SessionState) { st.Runtime.Handle = "h1" })
	file := s.SessionStore.GetSessionFile()
	fi, err := os.Stat(store.StatePath(file))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	if got := store.ResolveHandle(agentDir, "h1"); got != file {
		t.Errorf("handle -> %q, want %q", got, file)
	}
	// /new moves the handle with the running session.
	if _, err := s.NewSessionCmd(); err != nil {
		t.Fatal(err)
	}
	if got := store.ResolveHandle(agentDir, "h1"); got != s.SessionStore.GetSessionFile() {
		t.Errorf("handle after /new -> %q, want %q", got, s.SessionStore.GetSessionFile())
	}
	// A closed session saves nothing more.
	s.Close()
	store.ForgetHandle(agentDir, "h1")
	s.SaveState()
	if store.ResolveHandle(agentDir, "h1") != "" {
		t.Error("save after Close rebound the handle")
	}
}

func TestSessionState_InMemorySavesNothing(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	res, err := Setup(context.Background(), SetupOptions{
		Cwd: cwd, AgentDir: agentDir, SessionStore: store.InMemorySessionStore(cwd),
		Tools: DefaultCodingTools(cwd),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Session.Close()
	res.Session.UpdateSessionState(func(st *SessionState) { st.Runtime.Mode = "x" })
	if res.Session.SessionState().Runtime.Mode != "x" {
		t.Error("in-memory state not kept")
	}
}

// Opening another owner's transcript (e.g. `fir --session` on an ACP one)
// must not take over its handle, even across /new.
func TestSessionState_OpenDoesNotHijackHandle(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	owner := openForTest(t, cwd, agentDir, "", func(st *SessionState) { st.Runtime.Handle = "acp-sid" })
	file := owner.SessionStore.GetSessionFile()
	owner.Close()

	other := openForTest(t, cwd, agentDir, file, nil)
	if h := other.SessionState().Runtime.Handle; h != "" {
		t.Fatalf("opener inherited handle %q", h)
	}
	if _, err := other.NewSessionCmd(); err != nil {
		t.Fatal(err)
	}
	if got := store.ResolveHandle(agentDir, "acp-sid"); got != file {
		t.Fatalf("handle -> %q, want owner's %q", got, file)
	}
}

// Switching onto a transcript another owner holds by handle forks it, so the
// owner's saved runtime (e.g. ACP client MCP) is never overwritten.
func TestSessionState_SwitchForksForeignOwnedTranscript(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	owner := openForTest(t, cwd, agentDir, "", func(st *SessionState) {
		st.Runtime.Handle = "acp-sid"
		st.Runtime.Mode = "owner"
	})
	file := owner.SessionStore.GetSessionFile()
	owner.Close()

	s := openForTest(t, cwd, agentDir, "", func(st *SessionState) { st.Runtime.Mode = "mine" })
	forked, err := s.SwitchSession(file)
	if err != nil {
		t.Fatal(err)
	}
	if !forked || s.SessionStore.GetSessionFile() == file {
		t.Fatalf("forked=%v file=%q, want a fork of %q", forked, s.SessionStore.GetSessionFile(), file)
	}
	saved, _ := LoadState(file)
	if saved.Runtime.Mode != "owner" || saved.Runtime.Handle != "acp-sid" {
		t.Fatalf("owner state overwritten: %#v", saved.Runtime)
	}
	if got := store.ResolveHandle(agentDir, "acp-sid"); got != file {
		t.Fatalf("handle -> %q, want %q", got, file)
	}
	if s.SessionState().Runtime.Mode != "mine" {
		t.Error("switcher lost its runtime")
	}
}

// A handle that no longer points at the transcript (released, pruned) does
// not force a fork.
func TestSessionState_SwitchIgnoresStaleHandle(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	owner := openForTest(t, cwd, agentDir, "", func(st *SessionState) { st.Runtime.Handle = "gone" })
	file := owner.SessionStore.GetSessionFile()
	owner.Close()
	store.ForgetHandle(agentDir, "gone")

	s := openForTest(t, cwd, agentDir, "", nil)
	forked, err := s.SwitchSession(file)
	if err != nil {
		t.Fatal(err)
	}
	if forked || s.SessionStore.GetSessionFile() != file {
		t.Fatalf("forked=%v file=%q, want %q", forked, s.SessionStore.GetSessionFile(), file)
	}
}
