package acp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/mcp"
	"github.com/kfet/fir/pkg/slashcmd/builtin"
	"github.com/kfet/pinoauth"
)

const mcpTestSession = "sess-mcp"

// newAgentForMCPAuthTest returns a firAgent with one session whose MCP
// manager knows a stdio server "local" and a remote server "remote". login
// replaces the real MCP login when non-nil.
func newAgentForMCPAuthTest(t *testing.T, login mcpLoginFunc) *firAgent {
	t.Helper()
	mgr := mcp.NewManager(map[string]mcp.ServerConfig{
		"local":  {Command: "true"},
		"remote": {Transport: "streamable", URL: "https://mcp.example.invalid/mcp"},
	}, false)
	t.Cleanup(func() { _ = mgr.Close() })
	pa := &firAgent{
		sessions:     map[string]*firSession{mcpTestSession: {mcpManager: mgr}},
		pendingAuths: make(map[string]*pendingAuth),
		mcpLogin:     login,
	}
	t.Cleanup(func() {
		pa.mu.Lock()
		ids := make([]string, 0, len(pa.pendingAuths))
		for id := range pa.pendingAuths {
			ids = append(ids, id)
		}
		pa.mu.Unlock()
		for _, id := range ids {
			pa.cancelPendingAuth(id)
		}
	})
	return pa
}

func mcpAuthReq(method string, auth map[string]any) acpsdk.AuthenticateRequest {
	return acpsdk.AuthenticateRequest{MethodId: method, Meta: map[string]any{"auth": auth}}
}

func mcpCall1(sessionID string) map[string]any {
	a := map[string]any{"interactive": true}
	if sessionID != "" {
		a["sessionId"] = sessionID
	}
	return a
}

// pasteLogin emits url, waits for the pasted redirect and reports it.
func pasteLogin(url string, gotServer, pasted chan<- string) mcpLoginFunc {
	return func(_ context.Context, _ *mcp.Manager, server string, cb pinoauth.LoginCallbacks) error {
		gotServer <- server
		cb.OnAuth(pinoauth.AuthInfo{URL: url, Instructions: "authorize"})
		v, err := cb.OnManualCodeInput()
		if err != nil {
			return err
		}
		pasted <- v
		return nil
	}
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestMCPAuth_Call1Call2(t *testing.T) {
	const wantURL = "https://as.example/authorize?state=s1"
	const wantPaste = "http://127.0.0.1:4242/callback?code=c&state=s1"
	gotServer := make(chan string, 1)
	pasted := make(chan string, 1)
	pa := newAgentForMCPAuthTest(t, pasteLogin(wantURL, gotServer, pasted))
	ctx := testCtx(t)

	resp, err := pa.handleAuthenticate(ctx, mcpAuthReq("mcp:remote", mcpCall1(mcpTestSession)))
	if err != nil {
		t.Fatalf("call 1: %v", err)
	}
	state, id, url, instr := authMetaFromResponse(t, resp)
	if state != authStateNeedsRedirect || url != wantURL || id == "" || instr != "authorize" {
		t.Fatalf("call 1 = state %q id %q url %q instr %q", state, id, url, instr)
	}
	if s := <-gotServer; s != "remote" {
		t.Fatalf("login ran for server %q, want remote", s)
	}

	resp, err = pa.handleAuthenticate(ctx, mcpAuthReq("mcp:remote", map[string]any{
		"interactive": true, "id": id, "redirect": wantPaste,
	}))
	if err != nil {
		t.Fatalf("call 2: %v", err)
	}
	if state, _, _, _ := authMetaFromResponse(t, resp); state != authStateOK {
		t.Fatalf("call 2 state = %q, want ok", state)
	}
	if got := <-pasted; got != wantPaste {
		t.Errorf("login got paste %q, want %q", got, wantPaste)
	}
	if pa.lookupPendingAuth(id) != nil {
		t.Error("pending login not cleaned up")
	}
}

func TestMCPAuth_LoopbackWinsBeforePaste(t *testing.T) {
	// The loopback listener completes the login before the relay pastes;
	// a late call 2 still reports ok, and the manual-input wait is released.
	released := make(chan error, 1)
	pa := newAgentForMCPAuthTest(t, func(_ context.Context, _ *mcp.Manager, _ string, cb pinoauth.LoginCallbacks) error {
		cb.OnAuth(pinoauth.AuthInfo{URL: "https://as.example/a"})
		go func() { _, err := cb.OnManualCodeInput(); released <- err }()
		return nil
	})
	ctx := testCtx(t)

	resp, err := pa.handleAuthenticate(ctx, mcpAuthReq("mcp:remote", mcpCall1(mcpTestSession)))
	if err != nil {
		t.Fatalf("call 1: %v", err)
	}
	_, id, _, _ := authMetaFromResponse(t, resp)
	select {
	case err := <-released:
		if err == nil {
			t.Error("manual input returned a value, want ctx error")
		}
	case <-ctx.Done():
		t.Fatal("manual-input wait never released after loopback win")
	}
	resp, err = pa.handleAuthenticate(ctx, mcpAuthReq("mcp:remote", map[string]any{
		"interactive": true, "id": id, "redirect": "http://127.0.0.1/cb?code=x",
	}))
	if err != nil {
		t.Fatalf("call 2: %v", err)
	}
	if state, _, _, _ := authMetaFromResponse(t, resp); state != authStateOK {
		t.Fatalf("call 2 state = %q, want ok", state)
	}
}

func TestMCPAuth_Cancel(t *testing.T) {
	loginErr := make(chan error, 1)
	pa := newAgentForMCPAuthTest(t, func(_ context.Context, _ *mcp.Manager, _ string, cb pinoauth.LoginCallbacks) error {
		cb.OnAuth(pinoauth.AuthInfo{URL: "https://as.example/a"})
		_, err := cb.OnManualCodeInput()
		loginErr <- err
		return err
	})
	ctx := testCtx(t)

	resp, err := pa.handleAuthenticate(ctx, mcpAuthReq("mcp:remote", mcpCall1(mcpTestSession)))
	if err != nil {
		t.Fatalf("call 1: %v", err)
	}
	_, id, _, _ := authMetaFromResponse(t, resp)

	// A cancel for another method must not touch this flow.
	if _, err := pa.handleAuthenticate(ctx, mcpAuthReq("mcp:other", map[string]any{"interactive": true, "id": id, "cancel": true})); err != nil {
		t.Fatalf("foreign cancel: %v", err)
	}
	if pa.lookupPendingAuth(id) == nil {
		t.Fatal("cancel for mcp:other removed the mcp:remote flow")
	}

	resp, err = pa.handleAuthenticate(ctx, mcpAuthReq("mcp:remote", map[string]any{"interactive": true, "id": id, "cancel": true}))
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if state, gotID, _, _ := authMetaFromResponse(t, resp); state != authStateCancelled || gotID != id {
		t.Fatalf("cancel = state %q id %q", state, gotID)
	}
	if err := <-loginErr; !errors.Is(err, context.Canceled) {
		t.Errorf("login ended with %v, want context.Canceled", err)
	}
	if pa.lookupPendingAuth(id) != nil {
		t.Error("pending login survived cancel")
	}
	_, err = pa.handleAuthenticate(ctx, mcpAuthReq("mcp:remote", map[string]any{"interactive": true, "id": id, "redirect": "x"}))
	if !errors.Is(err, errNoPendingAuth) {
		t.Errorf("call 2 after cancel = %v, want errNoPendingAuth", err)
	}
}

func TestMCPAuth_Call2WrongMethod(t *testing.T) {
	gotServer := make(chan string, 1)
	pasted := make(chan string, 1)
	pa := newAgentForMCPAuthTest(t, pasteLogin("https://as.example/a", gotServer, pasted))
	ctx := testCtx(t)

	resp, err := pa.handleAuthenticate(ctx, mcpAuthReq("mcp:remote", mcpCall1(mcpTestSession)))
	if err != nil {
		t.Fatalf("call 1: %v", err)
	}
	_, id, _, _ := authMetaFromResponse(t, resp)
	_, err = pa.handleAuthenticate(ctx, mcpAuthReq("mcp:other", map[string]any{"interactive": true, "id": id, "redirect": "x"}))
	if !errors.Is(err, errNoPendingAuth) {
		t.Fatalf("call 2 for another method = %v, want errNoPendingAuth", err)
	}
	if pa.lookupPendingAuth(id) == nil {
		t.Error("mismatched call 2 consumed the pending flow")
	}
}

func TestMCPAuth_Call1Errors(t *testing.T) {
	scripted := func(_ context.Context, _ *mcp.Manager, _ string, _ pinoauth.LoginCallbacks) error {
		return errors.New("discovery exploded")
	}
	cases := []struct {
		name    string
		login   mcpLoginFunc // nil = real mcp.Manager.LoginServer
		method  string
		auth    map[string]any
		noMgr   bool
		wantErr string
	}{
		{"missing sessionId", nil, "mcp:remote", mcpCall1(""), false, "requires _meta.auth.sessionId"},
		{"unknown session", nil, "mcp:remote", mcpCall1("nope"), false, "session not found: nope"},
		{"not interactive", nil, "mcp:remote", map[string]any{"sessionId": mcpTestSession}, false, "requires _meta.auth.interactive"},
		{"no meta at all", nil, "mcp:remote", nil, false, "requires _meta.auth.interactive"},
		{"empty server", nil, "mcp:", mcpCall1(mcpTestSession), false, "names no MCP server"},
		{"unknown server", nil, "mcp:slack", mcpCall1(mcpTestSession), false, `MCP server "slack" not configured`},
		{"stdio server", nil, "mcp:local", mcpCall1(mcpTestSession), false, "OAuth applies to sse and streamable servers only"},
		{"no mcp manager", nil, "mcp:remote", mcpCall1(mcpTestSession), true, "no MCP servers configured"},
		{"login fails before url", scripted, "mcp:remote", mcpCall1(mcpTestSession), false, "discovery exploded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pa := newAgentForMCPAuthTest(t, tc.login)
			if tc.noMgr {
				pa.sessions[mcpTestSession].mcpManager = nil
			}
			req := acpsdk.AuthenticateRequest{MethodId: tc.method}
			if tc.auth != nil {
				req.Meta = map[string]any{"auth": tc.auth}
			}
			_, err := pa.handleAuthenticate(testCtx(t), req)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
			}
			pa.mu.Lock()
			n := len(pa.pendingAuths)
			pa.mu.Unlock()
			if n != 0 {
				t.Errorf("%d pending logins left after a failed call 1", n)
			}
		})
	}
}

func TestMCPAuth_NotAdvertised(t *testing.T) {
	// mcp:* ids are dispatched even though authMethods lists none of them.
	pa := newAgentForMCPAuthTest(t, nil)
	if len(pa.authMethods) != 0 {
		t.Fatal("test agent unexpectedly advertises auth methods")
	}
	_, err := pa.handleAuthenticate(testCtx(t), mcpAuthReq("mcp:slack", mcpCall1(mcpTestSession)))
	if err == nil || strings.Contains(err.Error(), "unknown auth method") {
		t.Fatalf("mcp:* treated as an unadvertised method: %v", err)
	}
}

func TestACPMCPLogin_LoopbackNote(t *testing.T) {
	var got pinoauth.AuthInfo
	inner := builtin.FuncMCP{LoginFn: func(_ context.Context, _ string, cb pinoauth.LoginCallbacks) error {
		cb.OnAuth(pinoauth.AuthInfo{URL: "https://as.example/a", Instructions: "Authorize fir."})
		return nil
	}}
	err := acpMCP{inner}.Login(context.Background(), "linear", pinoauth.LoginCallbacks{
		OnAuth: func(info pinoauth.AuthInfo) { got = info },
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Authorize fir.", "loopback address on the agent host", "`!mcp login linear`", "paste the landing URL back"} {
		if !strings.Contains(got.Instructions, want) {
			t.Errorf("instructions %q missing %q", got.Instructions, want)
		}
	}
	if got.URL != "https://as.example/a" {
		t.Errorf("url = %q", got.URL)
	}
}

func TestMCPAuth_ProviderMethodCannotTouchMCPFlow(t *testing.T) {
	pa, cleanup := newAgentForAuthTest(t, "scripted-cross", func(pinoauth.LoginCallbacks) (*ai.OAuthCredentials, error) {
		return nil, errors.New("unused")
	})
	defer cleanup()
	gotServer := make(chan string, 1)
	pasted := make(chan string, 1)
	mcpAgent := newAgentForMCPAuthTest(t, pasteLogin("https://as.example/a", gotServer, pasted))
	pa.sessions = mcpAgent.sessions
	pa.mcpLogin = mcpAgent.mcpLogin
	ctx := testCtx(t)

	resp, err := pa.handleAuthenticate(ctx, mcpAuthReq("mcp:remote", mcpCall1(mcpTestSession)))
	if err != nil {
		t.Fatalf("call 1: %v", err)
	}
	_, id, _, _ := authMetaFromResponse(t, resp)
	t.Cleanup(func() { pa.cancelPendingAuth(id) })

	const provider = "oauth-scripted-cross"
	if _, err := pa.handleAuthenticate(ctx, mcpAuthReq(provider, map[string]any{"interactive": true, "id": id, "cancel": true})); err != nil {
		t.Fatalf("provider cancel: %v", err)
	}
	if pa.lookupPendingAuth(id) == nil {
		t.Fatal("provider cancel removed the mcp flow")
	}
	_, err = pa.handleAuthenticate(ctx, mcpAuthReq(provider, map[string]any{"interactive": true, "id": id, "redirect": "x"}))
	if !errors.Is(err, errNoPendingAuth) {
		t.Fatalf("provider call 2 with mcp id = %v, want errNoPendingAuth", err)
	}
}
