package acp

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/mcp"
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
		conn:      newMockConn(),
		sessions:  make(map[string]*firSession),
		configDir: t.TempDir(),
		options:   Options{NoExtensions: true, NoMCP: true, NoSkills: true},
		idleTTL:   time.Hour,
		nowFn:     func() time.Time { return base },
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
	if _, ok := pa.loadSessionConfig(sid); !ok {
		t.Fatal("reaped session config not saved")
	}
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
	if cfg, _ := pa.loadSessionConfig(sid); cfg.Transcript != sessionFile {
		t.Fatalf("saved transcript = %q, want %q", cfg.Transcript, sessionFile)
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
	if _, ok := pa.loadSessionConfig(sid); ok {
		t.Error("saved config not deleted by explicit release")
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
		{"setup.cwd", back.getSetup().Cwd, cwd},
		{"meta", back.getSetup().Meta, meta},
		{"mode", back.getSetup().Mode, "architect"},
		{"client mcp", serverNames(back.clientMCP()), []string{"relay"}},
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

// fillNonZero sets every field of v (a struct pointer) to a non-zero value,
// by type only, so a field added to acpSetup later is covered with no test
// change. cwd-like strings get a real directory; MCP configs a working server.
func fillNonZero(t *testing.T, v reflect.Value, dir string) {
	t.Helper()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		switch {
		case f.Type() == reflect.TypeOf(map[string]mcp.ServerConfig{}):
			f.Set(reflect.ValueOf(map[string]mcp.ServerConfig{"srv": echoServer()}))
		case f.Kind() == reflect.String:
			f.SetString(dir)
		case f.Kind() == reflect.Bool:
			f.SetBool(true)
		case f.CanInt():
			f.SetInt(7)
		case f.Kind() == reflect.Map && f.Type().Key().Kind() == reflect.String:
			m := reflect.MakeMap(f.Type())
			elem := reflect.New(f.Type().Elem()).Elem()
			if elem.Kind() == reflect.Interface || elem.Kind() == reflect.String {
				elem.Set(reflect.ValueOf("v"))
			}
			m.SetMapIndex(reflect.ValueOf("k"), elem)
			f.Set(m)
		case f.Kind() == reflect.Slice && f.Type().Elem().Kind() == reflect.String:
			f.Set(reflect.ValueOf([]string{"v"}).Convert(f.Type()))
		default:
			t.Fatalf("fillNonZero: teach it field %s of type %s", v.Type().Field(i).Name, f.Type())
		}
		if f.IsZero() {
			t.Fatalf("fillNonZero left %s zero", v.Type().Field(i).Name)
		}
	}
}

// TestReapRehydrate_AnySetupPropertySurvives: whatever a setter puts in the
// session setup survives reap and rehydrate, with no reaper code naming it.
func TestReapRehydrate_AnySetupPropertySurvives(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	const sid = "acp-sid-generic"
	entry := openNew(t, pa, sid, &clientSetup{cwd: cwd})

	var want acpSetup
	fillNonZero(t, reflect.ValueOf(&want).Elem(), cwd)
	// A test-only setter: writes the setup the way any ACP setter would.
	entry.updateSetup(func(s *acpSetup) { *s = want })

	reapNow(t, pa, sid, entry)
	back := rehydrate(t, pa, sid)
	if got := back.getSetup(); !reflect.DeepEqual(got, want) {
		t.Fatalf("setup after rehydrate:\n got %#v\nwant %#v", got, want)
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
	entry.updateSetup(func(s *acpSetup) { s.Mode = "kept" })
	reapNow(t, pa, sid, entry)

	cwd2 := t.TempDir()
	if _, err := pa.resumeSessionLocal(ctx, ResumeSessionRequest{
		SessionId: sid, Cwd: cwd2,
		McpServers: []acpsdk.McpServer{echoMCPServer("new")},
		Meta:       map[string]any{"v": "2"},
	}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	want := acpSetup{Cwd: cwd2, McpServers: map[string]mcp.ServerConfig{"new": echoServer()},
		Meta: map[string]any{"v": "2"}, Mode: "kept"}
	if got := pa.lookupSession(sid).getSetup(); !reflect.DeepEqual(got, want) {
		t.Fatalf("live setup:\n got %#v\nwant %#v", got, want)
	}
	saved, _ := pa.loadSessionConfig(sid)
	if !reflect.DeepEqual(saved.Setup, want) {
		t.Fatalf("saved setup:\n got %#v\nwant %#v", saved.Setup, want)
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
	if _, ok := pa.loadSessionConfig(sid); !ok {
		t.Error("failed restore dropped the saved config")
	}
}

func TestSessionConfig_FileMode0600(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	const sid = "acp-sid-perm"
	openNew(t, pa, sid, &clientSetup{cwd: cwd})
	fi, err := os.Stat(pa.sessionConfigPath(sid))
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
	pa.saveSessionConfig(sid, entry)
	if _, ok := pa.loadSessionConfig(sid); ok {
		t.Fatal("save after release recreated the session config")
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

func TestPruneSessionConfigs_RemovesStale(t *testing.T) {
	pa, cwd := newRehydrateAgent(t)
	openNew(t, pa, "fresh", &clientSetup{cwd: cwd})
	openNew(t, pa, "stale", &clientSetup{cwd: cwd})
	old := time.Now().Add(-2 * sessionConfigMaxAge)
	if err := os.Chtimes(pa.sessionConfigPath("stale"), old, old); err != nil {
		t.Fatal(err)
	}
	pa.pruneSessionConfigs(time.Now().Add(-sessionConfigMaxAge))
	if _, ok := pa.loadSessionConfig("stale"); ok {
		t.Error("stale config not pruned")
	}
	if _, ok := pa.loadSessionConfig("fresh"); !ok {
		t.Error("fresh config pruned")
	}
}
