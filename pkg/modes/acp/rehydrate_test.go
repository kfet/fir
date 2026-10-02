package acp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/session/statetest"
	"github.com/kfet/fir/pkg/session/store"
)

// newRehydrateAgent builds a firAgent wired for re-hydration tests: an isolated
// agent dir + cwd (via env), no extensions/MCP/skills so createSession is cheap
// and deterministic, and a fixed clock.
func newRehydrateAgent(t *testing.T) (*firAgent, string) {
	t.Helper()
	agentDir := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("FIR_AGENT_DIR", agentDir)
	t.Setenv("PWD", cwd)

	base := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	pa := &firAgent{
		conn:     newMockConn(),
		sessions: make(map[string]*firSession),
		agentDir: agentDir,
		options:  Options{NoExtensions: true, NoMCP: true, NoSkills: true},
		idleTTL:  time.Hour,
		nowFn:    func() time.Time { return base },
	}
	return pa, cwd
}

func reapNow(t *testing.T, pa *firAgent, sid string, entry *firSession) {
	t.Helper()
	entry.touch(pa.now().Add(-2 * time.Hour))
	reaped := pa.reapIdle(pa.now())
	if len(reaped) != 1 || reaped[0] != sid {
		t.Fatalf("reapIdle = %v, want [%s]", reaped, sid)
	}
	if pa.lookupSession(sid) != nil {
		t.Fatal("reaped session still present in sessions map")
	}
	if _, ok := savedState(pa, sid); !ok {
		t.Fatal("reaped session state not saved")
	}
}

// savedState returns the state saved for the transcript sid is bound to.
func savedState(pa *firAgent, sid string) (session.SessionState, bool) {
	file := store.ResolveHandle(pa.agentDir, sid)
	if file == "" {
		return session.SessionState{}, false
	}
	return session.LoadState(file)
}

func promptIsNotFound(err error) bool {
	re, ok := err.(*acpsdk.RequestError)
	return ok && re.Code == SessionNotFoundError
}

// echoServer is a working client-supplied MCP server: the test binary itself
// (see TestMain in acp_mcp_e2e_test.go).
func echoServer() mcp.ServerConfig {
	return mcp.ServerConfig{Command: os.Args[0], Env: map[string]string{"MCP_TEST_SERVER": "1"}}
}

func echoMCPServer(name string) acpsdk.McpServer {
	return acpsdk.McpServer{Stdio: &acpsdk.McpServerStdio{
		Name: name, Command: os.Args[0],
		Env: []acpsdk.EnvVariable{{Name: "MCP_TEST_SERVER", Value: "1"}},
	}}
}

func openNew(t *testing.T, pa *firAgent, sid string, req *clientSetup) *firSession {
	t.Helper()
	entry, _, err := pa.openSession(context.Background(), sid, req)
	if err != nil {
		t.Fatalf("openSession: %v", err)
	}
	t.Cleanup(func() {
		if e, ok := pa.removeSession(sid); ok {
			pa.teardownSession(context.Background(), sid, e)
		}
	})
	return entry
}

func rehydrate(t *testing.T, pa *firAgent, sid string) *firSession {
	t.Helper()
	entry, err := pa.rehydrateForPrompt(context.Background(), sid)
	if err != nil {
		t.Fatalf("rehydrateForPrompt: %v", err)
	}
	if entry == nil {
		t.Fatal("session not rehydrated")
	}
	return entry
}

// TestPrompt_RehydratesReapedSession_RestoresConversation: after a reap, a
// Prompt for the same ID rehydrates it in place with its prior conversation.
func TestPrompt_RehydratesReapedSession_RestoresConversation(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	ctx := context.Background()
	const sid = "acp-sid-restore"

	entry := openNew(t, pa, sid, &clientSetup{cwd: cwd})
	entry.session.SessionStore.NewSession(nil)
	entry.session.SessionStore.AppendAgentMessage(
		agent.NewAgentMessage(ai.NewUserMsg("remembered turn", time.Now().UnixMilli())))
	sessionFile := entry.session.SessionStore.GetSessionFile()

	reapNow(t, pa, sid, entry)
	if got := store.ResolveHandle(pa.agentDir, sid); got != sessionFile {
		t.Fatalf("bound transcript = %q, want %q", got, sessionFile)
	}

	_, perr := pa.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: acpsdk.SessionId(sid),
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("hello again")},
	})
	if promptIsNotFound(perr) {
		t.Fatal("Prompt returned session-not-found after reap")
	}
	back := pa.lookupSession(sid)
	if back == nil {
		t.Fatal("session not re-hydrated into map under same ID")
	}
	if len(back.session.SessionStore.BuildSessionContext().Messages) == 0 {
		t.Error("re-hydrated session has no restored conversation history")
	}
}

// A reaped session that never wrote a transcript comes back fresh, same ID.
func TestPrompt_RehydratesReapedSession_NoTranscript_FreshSameID(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	const sid = "acp-sid-fresh"
	entry := openNew(t, pa, sid, &clientSetup{cwd: cwd})
	reapNow(t, pa, sid, entry)
	_, perr := pa.Prompt(context.Background(), acpsdk.PromptRequest{
		SessionId: acpsdk.SessionId(sid),
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("first real prompt")},
	})
	if promptIsNotFound(perr) {
		t.Fatal("Prompt returned session-not-found; expected fresh same-ID re-hydration")
	}
	if pa.lookupSession(sid) == nil {
		t.Fatal("session not re-created under same ID")
	}
}

func TestPrompt_UnknownSession_StillTypedError(t *testing.T) {
	pa, _ := newRehydrateAgent(t)
	_, err := pa.Prompt(context.Background(), acpsdk.PromptRequest{
		SessionId: "00000000-dead-beef-0000-unknownsession",
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("hi")},
	})
	if !promptIsNotFound(err) {
		t.Fatalf("expected session-not-found (-32001) for unknown id, got %v", err)
	}
}

// An explicit release of a reaped session forgets its saved config.
func TestRelease_ReapedSession_ForgetsAndBlocksRehydration(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	ctx := context.Background()
	const sid = "acp-sid-release-reaped"
	entry := openNew(t, pa, sid, &clientSetup{cwd: cwd})
	reapNow(t, pa, sid, entry)

	if _, err := pa.ReleaseSession(ctx, ReleaseSessionRequest{SessionId: sid}); err != nil {
		t.Fatalf("ReleaseSession on reaped id: %v", err)
	}
	if _, ok := savedState(pa, sid); ok {
		t.Error("binding not deleted by explicit release")
	}
	_, perr := pa.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: acpsdk.SessionId(sid),
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("anyone home?")},
	})
	if !promptIsNotFound(perr) {
		t.Fatalf("expected session-not-found after release, got %v", perr)
	}
}

func mcpConnected(e *firSession, name string) bool {
	for _, st := range e.mcpManager.Status() {
		if st.Name == name {
			return st.Connected
		}
	}
	return false
}

// TestReapRehydrate_RestoresSessionState: every piece of client-supplied or
// ACP-set state survives a reap and a rehydrate.
func TestReapRehydrate_RestoresSessionState(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	ctx := context.Background()
	const sid = "acp-sid-state"
	meta := map[string]any{"relay": "zulip", "conv": "c1"}

	entry := openNew(t, pa, sid, &clientSetup{
		cwd: cwd, mcpServers: []acpsdk.McpServer{echoMCPServer("relay")}, meta: meta,
	})
	if _, err := pa.SetSessionModel(ctx, SetSessionModelRequest{
		SessionId: acpsdk.SessionId(sid), ModelId: "anthropic/claude-sonnet-4-5"}); err != nil {
		t.Fatalf("SetSessionModel: %v", err)
	}
	if _, err := pa.setSessionConfigOptionLocal(ctx, SetSessionConfigOptionRequest{
		SessionId: sid, ConfigId: thinkingConfigID, Value: "high"}); err != nil {
		t.Fatalf("set thinking: %v", err)
	}
	if _, err := pa.SetSessionMode(ctx, acpsdk.SetSessionModeRequest{
		SessionId: acpsdk.SessionId(sid), ModeId: "architect"}); err != nil {
		t.Fatalf("SetSessionMode: %v", err)
	}
	wantThinking := entry.session.ThinkingLevel()

	reapNow(t, pa, sid, entry)
	back := rehydrate(t, pa, sid)

	tests := []struct {
		name      string
		got, want any
	}{
		{"cwd", back.cwd, cwd},
		{"state cwd", back.session.SessionState().Runtime.Cwd, cwd},
		{"meta", back.session.SessionState().Runtime.Meta, meta},
		{"mode", back.session.SessionState().Runtime.Mode, "architect"},
		{"client mcp", session.ServerNames(back.session.SessionMCPServers()), []string{"relay"}},
		{"mcp connected", mcpConnected(back, "relay"), true},
		{"model", back.session.Model().Provider + "/" + back.session.Model().ID, "anthropic/claude-sonnet-4-5"},
		{"thinking", back.session.ThinkingLevel(), wantThinking},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !reflect.DeepEqual(tc.got, tc.want) {
				t.Errorf("got %#v, want %#v", tc.got, tc.want)
			}
		})
	}
}

// TestReapRehydrate_AnyRuntimePropertySurvives: whatever a setter puts in the
// session's runtime state survives reap and rehydrate (the ACP handle is
// re-pinned to the sessionId), with no ACP code naming the field.
func TestReapRehydrate_AnyRuntimePropertySurvives(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	const sid = "acp-sid-generic"
	entry := openNew(t, pa, sid, &clientSetup{cwd: cwd})

	var want session.RuntimeState
	statetest.FillNonZero(t, &want, cwd, map[string]mcp.ServerConfig{"srv": echoServer()})
	want.Handle = sid
	entry.session.UpdateSessionState(func(st *session.SessionState) { st.Runtime = want })

	reapNow(t, pa, sid, entry)
	back := rehydrate(t, pa, sid)
	if got := back.session.SessionState().Runtime; !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime after rehydrate:\n got %#v\nwant %#v", got, want)
	}
}

// TestLoad_ClientOverridesSaved: a client session/load supplies fresh values;
// they replace the saved ones and are saved in turn.
func TestLoad_ClientOverridesSaved(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	ctx := context.Background()
	const sid = "acp-sid-override"
	entry := openNew(t, pa, sid, &clientSetup{
		cwd: cwd, mcpServers: []acpsdk.McpServer{echoMCPServer("old")}, meta: map[string]any{"v": "1"},
	})
	entry.session.UpdateSessionState(func(st *session.SessionState) { st.Runtime.Mode = "kept" })
	reapNow(t, pa, sid, entry)

	cwd2 := t.TempDir()
	if _, err := pa.resumeSessionLocal(ctx, ResumeSessionRequest{
		SessionId: sid, Cwd: cwd2,
		McpServers: []acpsdk.McpServer{echoMCPServer("new")},
		Meta:       map[string]any{"v": "2"},
	}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	want := session.RuntimeState{Cwd: cwd2, McpServers: map[string]mcp.ServerConfig{"new": echoServer()},
		Meta: map[string]any{"v": "2"}, Mode: "kept", Handle: sid}
	if got := pa.lookupSession(sid).session.SessionState().Runtime; !reflect.DeepEqual(got, want) {
		t.Fatalf("live runtime:\n got %#v\nwant %#v", got, want)
	}
	saved, _ := savedState(pa, sid)
	if !reflect.DeepEqual(saved.Runtime, want) {
		t.Fatalf("saved runtime:\n got %#v\nwant %#v", saved.Runtime, want)
	}
}

// TestRehydrate_FailedClientMCP_NeedsReload: a restored client MCP server that
// cannot start yields a typed error telling the client to session/load.
func TestRehydrate_FailedClientMCP_NeedsReload(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	const sid = "acp-sid-badmcp"
	bad := acpsdk.McpServer{Stdio: &acpsdk.McpServerStdio{
		Name: "relay", Command: "/nonexistent/zulip-acp", Args: []string{"mcp-serve"}, Env: []acpsdk.EnvVariable{}}}
	entry := openNew(t, pa, sid, &clientSetup{cwd: cwd, mcpServers: []acpsdk.McpServer{bad}})
	reapNow(t, pa, sid, entry)

	_, err := pa.Prompt(context.Background(), acpsdk.PromptRequest{
		SessionId: acpsdk.SessionId(sid),
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("hi")},
	})
	re, ok := err.(*acpsdk.RequestError)
	if !ok || re.Code != SessionNeedsReloadError {
		t.Fatalf("err = %v, want code %d", err, SessionNeedsReloadError)
	}
	data, _ := re.Data.(map[string]any)
	if data["reason"] != "session_needs_reload" {
		t.Errorf("data = %#v, want reason session_needs_reload", re.Data)
	}
	if pa.lookupSession(sid) != nil {
		t.Error("failed restore left a session in the map")
	}
	if _, ok := savedState(pa, sid); !ok {
		t.Error("failed restore dropped the saved state")
	}
}

func TestSessionState_FileMode0600(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	const sid = "acp-sid-perm"
	entry := openNew(t, pa, sid, &clientSetup{cwd: cwd, mcpServers: []acpsdk.McpServer{echoMCPServer("relay")}})
	fi, err := os.Stat(store.StatePath(entry.session.SessionStore.GetSessionFile()))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

// A save racing session/release (e.g. Prompt's deferred save) must not
// resurrect the released session.
func TestSaveAfterRelease_DoesNotResurrect(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	const sid = "acp-sid-race"
	entry := openNew(t, pa, sid, &clientSetup{cwd: cwd})
	if _, err := pa.ReleaseSession(context.Background(), ReleaseSessionRequest{SessionId: sid}); err != nil {
		t.Fatal(err)
	}
	entry.session.SaveState()
	if pa.canRehydrate(sid) {
		t.Fatal("save after release recreated the session binding")
	}
}

// Concurrent rehydrates of one reaped session yield one live session.
func TestRehydrate_ConcurrentSameSession(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	const sid = "acp-sid-concurrent"
	entry := openNew(t, pa, sid, &clientSetup{cwd: cwd})
	reapNow(t, pa, sid, entry)
	results := make(chan *firSession, 4)
	for i := 0; i < 4; i++ {
		go func() {
			e, _ := pa.rehydrateForPrompt(context.Background(), sid)
			results <- e
		}()
	}
	first := <-results
	for i := 0; i < 3; i++ {
		if e := <-results; e != first {
			t.Fatal("concurrent rehydrates produced different sessions")
		}
	}
	if pa.lookupSession(sid) != first {
		t.Fatal("live session is not the rehydrated one")
	}
}

func TestPruneHandles_RemovesStale(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	openNew(t, pa, "fresh", &clientSetup{cwd: cwd})
	stale := openNew(t, pa, "stale", &clientSetup{cwd: cwd})
	// Stop the stale session first so no late save re-touches its binding.
	if e, ok := pa.removeSession("stale"); ok {
		pa.teardownSession(context.Background(), "stale", e)
	}
	old := time.Now().Add(-2 * handleMaxAge)
	matches, _ := filepath.Glob(filepath.Join(pa.agentDir, "session-handles", "*"))
	for _, m := range matches {
		data, _ := os.ReadFile(m)
		if string(data) == stale.session.SessionStore.GetSessionFile() {
			if err := os.Chtimes(m, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	pa.pruneHandles(time.Now().Add(-handleMaxAge))
	if pa.canRehydrate("stale") {
		t.Error("stale binding not pruned")
	}
	if !pa.canRehydrate("fresh") {
		t.Error("fresh binding pruned")
	}
}

// A v1.24.0 acp-sessions config is migrated on first use: the session comes
// back with its client setup, and the legacy file is gone.
func TestLegacyACPConfig_Migrated(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	const sid = "acp-sid-legacy"
	entry := openNew(t, pa, sid, &clientSetup{cwd: cwd})
	transcript := entry.session.SessionStore.GetSessionFile()
	if e, ok := pa.removeSession(sid); ok {
		pa.teardownSession(context.Background(), sid, e)
	}
	// Simulate a pre-upgrade install: no binding, no state, only the old file.
	pa.forgetSession(sid)
	_ = os.Remove(store.StatePath(transcript))
	legacy := `{"setup":{"cwd":"` + cwd + `","meta":{"relay":"zulip"},"mode":"m"},"transcript":"` + transcript + `","thinking":"high"}`
	sum := sha256.Sum256([]byte(sid))
	path := filepath.Join(pa.legacyConfigDir(), hex.EncodeToString(sum[:16])+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	back := rehydrate(t, pa, sid)
	rt := back.session.SessionState().Runtime
	if rt.Mode != "m" || rt.Meta["relay"] != "zulip" || rt.Cwd != cwd || rt.Handle != sid {
		t.Fatalf("runtime after migration = %#v", rt)
	}
	if back.session.SessionStore.GetSessionFile() != transcript {
		t.Errorf("transcript = %q, want %q", back.session.SessionStore.GetSessionFile(), transcript)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("legacy config not removed after migration")
	}
	t.Cleanup(func() {
		if e, ok := pa.removeSession(sid); ok {
			pa.teardownSession(context.Background(), sid, e)
		}
	})
}

// TestRehydrate_ConfigMCPOutlivesPromptCtx: a session re-hydrated by a
// session/prompt must keep its config-file MCP servers (mcp.json) after the
// prompt's request ctx is cancelled. Regression: Setup inherited that ctx, so
// every server still connecting died with "context canceled" — only the
// client-supplied servers, which restore waits for, survived.
func TestRehydrate_ConfigMCPOutlivesPromptCtx(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	pa.options.NoMCP = false
	srv := echoServer()
	cfg, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"proj": map[string]any{"command": srv.Command, "env": srv.Env}}})
	if err := os.MkdirAll(filepath.Join(cwd, ".fir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".fir", "mcp.json"), cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	const sid = "acp-sid-cfgmcp"
	entry := openNew(t, pa, sid, &clientSetup{cwd: cwd})
	reapNow(t, pa, sid, entry)

	ctx, cancel := context.WithCancel(context.Background())
	back, err := pa.rehydrateForPrompt(ctx, sid)
	cancel() // the prompt returns
	if err != nil || back == nil {
		t.Fatalf("rehydrateForPrompt: %v %v", back, err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for !mcpConnected(back, "proj") {
		if time.Now().After(deadline) {
			t.Fatalf("config MCP server not connected after prompt ctx cancel: %+v", back.mcpManager.Status())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
