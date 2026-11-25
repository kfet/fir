package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/pinoauth"
	"github.com/stretchr/testify/require"
)

// newDropManager builds a Manager for several named servers.
func init() { dropPollRetryDelay = 20 * time.Millisecond }

func newDropManager(t *testing.T, cfgs map[string]ServerConfig) *Manager {
	t.Helper()
	storage := auth.NewAuthStorage(filepath.Join(t.TempDir(), "auth.json"))
	mgr := NewManager(cfgs, false)
	mgr.SetAuth(storage)
	t.Cleanup(func() { _ = mgr.Close() })
	return mgr
}

func login(t *testing.T, mgr *Manager, name string, ui pinoauth.LoginCallbacks) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return mgr.LoginServer(ctx, name, ui)
}

// recordInstructions wraps ui so the login instructions are captured.
func recordInstructions(ui pinoauth.LoginCallbacks, got *string) pinoauth.LoginCallbacks {
	inner := ui.OnAuth
	ui.OnAuth = func(info pinoauth.AuthInfo) {
		*got = info.Instructions
		inner(info)
	}
	return ui
}

func TestMCPOAuthDrop_CaseB_LoginToDropHostItself(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{drop: true})
	srv := newFakeMCPServer(t, as, mcpOptions{})
	mgr, _ := newTestManager(t, srv.config(nil))

	var instr string
	require.NoError(t, login(t, mgr, "srv", recordInstructions(browserCallbacks(t, nil), &instr)))
	require.True(t, strings.HasPrefix(as.lastRedirectURI(), as.URL()+fakeDropPrefix), as.lastRedirectURI())
	require.Contains(t, instr, "automatically")
	require.NotContains(t, instr, "paste")
	opens, polls := as.dropCounters()
	require.Zero(t, opens, "Case B must not open a slot")
	require.Positive(t, polls)
	require.NoError(t, connectAndWait(t, mgr))
	requireToolsWork(t, mgr)
}

func TestMCPOAuthDrop_CaseB_PasteStillWorks(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{drop: true})
	srv := newFakeMCPServer(t, as, mcpOptions{})
	mgr, _ := newTestManager(t, srv.config(nil))
	// The browser never reaches the drop-box; the user pastes the URL.
	require.NoError(t, login(t, mgr, "srv", remoteBrowserCallbacks(t, nil)))
	require.NoError(t, connectAndWait(t, mgr))
}

// dropPair is a drop-box host plus a third-party DCR server.
func dropPair(t *testing.T, other asOptions) (box, third *fakeAuthServer, mgr *Manager) {
	t.Helper()
	box = newFakeAuthServer(t, asOptions{drop: true, dropOpenStatus: other.dropOpenStatus})
	boxSrv := newFakeMCPServer(t, box, mcpOptions{})
	other.dropOpenStatus = 0
	third = newFakeAuthServer(t, other)
	thirdSrv := newFakeMCPServer(t, third, mcpOptions{})
	mgr = newDropManager(t, map[string]ServerConfig{
		"agentbox": boxSrv.config(nil),
		"srv":      thirdSrv.config(nil),
	})
	require.NoError(t, login(t, mgr, "agentbox", browserCallbacks(t, nil)))
	return box, third, mgr
}

func TestMCPOAuthDrop_CaseA_ThirdPartyUsesDropHostSlot(t *testing.T) {
	box, third, mgr := dropPair(t, asOptions{})
	var instr string
	require.NoError(t, login(t, mgr, "srv", recordInstructions(browserCallbacks(t, nil), &instr)))
	require.True(t, strings.HasPrefix(third.lastRedirectURI(), box.URL()+fakeDropPrefix), third.lastRedirectURI())
	require.NotContains(t, instr, "paste")
	opens, _ := box.dropCounters()
	require.Equal(t, 1, opens)
	require.NoError(t, connectAndWait(t, mgr))
}

func TestMCPOAuthDrop_CaseA_SlotRefusedFallsBackToLoopback(t *testing.T) {
	_, third, mgr := dropPair(t, asOptions{dropOpenStatus: 503})
	require.NoError(t, login(t, mgr, "srv", browserCallbacks(t, nil)))
	require.True(t, strings.HasPrefix(third.lastRedirectURI(), "http://127.0.0.1:"), third.lastRedirectURI())
}

func TestMCPOAuthDrop_CaseA_NeedsDCR(t *testing.T) {
	box := newFakeAuthServer(t, asOptions{drop: true})
	boxSrv := newFakeMCPServer(t, box, mcpOptions{})
	third := newFakeAuthServer(t, asOptions{noDCR: true, preRegistered: "pre"})
	thirdSrv := newFakeMCPServer(t, third, mcpOptions{})
	mgr := newDropManager(t, map[string]ServerConfig{
		"agentbox": boxSrv.config(nil),
		"srv":      thirdSrv.config(&AuthConfig{ClientID: "pre"}),
	})
	require.NoError(t, login(t, mgr, "agentbox", browserCallbacks(t, nil)))
	require.NoError(t, login(t, mgr, "srv", browserCallbacks(t, nil)))
	require.True(t, strings.HasPrefix(third.lastRedirectURI(), "http://127.0.0.1:"), third.lastRedirectURI())
	opens, _ := box.dropCounters()
	require.Zero(t, opens)
}

func TestMCPOAuthDrop_NoDropHostKeepsLoopback(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{})
	mgr, _ := newTestManager(t, srv.config(nil))
	var instr string
	require.NoError(t, login(t, mgr, "srv", recordInstructions(browserCallbacks(t, nil), &instr)))
	require.True(t, strings.HasPrefix(as.lastRedirectURI(), "http://127.0.0.1:"))
	require.Contains(t, instr, "paste")
}

func TestMCPOAuthDrop_LoginRefreshesFirst(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{drop: true})
	srv := newFakeMCPServer(t, as, mcpOptions{})
	mgr, _ := newTestManager(t, srv.config(nil))
	var visits atomic.Int32
	require.NoError(t, login(t, mgr, "srv", browserCallbacks(t, &visits)))
	require.NoError(t, login(t, mgr, "srv", browserCallbacks(t, &visits)))
	require.EqualValues(t, 1, visits.Load(), "second login must be satisfied by refresh")
	_, refreshes, _ := as.counters()
	require.Equal(t, 1, refreshes)

	// A revoked refresh token falls through to a browser login.
	as.revokeAll()
	require.NoError(t, login(t, mgr, "srv", browserCallbacks(t, &visits)))
	require.EqualValues(t, 2, visits.Load())
}

func TestMCPOAuthDrop_DeniedSurfacesError(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{drop: true})
	srv := newFakeMCPServer(t, as, mcpOptions{})
	mgr, _ := newTestManager(t, srv.config(nil))
	ui := pinoauth.LoginCallbacks{OnAuth: func(info pinoauth.AuthInfo) {
		// Deliver an error response straight to the drop-box slot.
		u, err := urlParse(info.URL)
		require.NoError(t, err)
		redirect := u.Query().Get("redirect_uri")
		as.mu.Lock()
		id := strings.TrimPrefix(redirect, as.URL()+fakeDropPrefix)
		as.slots[id] = &fakeDropSlot{ready: make(chan struct{})}
		as.mu.Unlock()
		resp, err := httpGet(redirect + "?error=access_denied&state=" + u.Query().Get("state"))
		require.NoError(t, err)
		_ = resp.Body.Close()
	}}
	err := login(t, mgr, "srv", ui)
	require.ErrorContains(t, err, "access_denied")
}

func TestValidDropEndpoint(t *testing.T) {
	doc := "https://box.test/.well-known/oauth-authorization-server"
	for raw, want := range map[string]bool{
		"https://box.test/.agentbox/oauth-drop/":     true,
		"https://evil.test/.agentbox/oauth-drop/":    false,
		"https://box.test/.agentbox/oauth-drop":      false,
		"http://box.test/.agentbox/oauth-drop/":      false,
		"https://box.test/.agentbox/oauth-drop/?x=1": false,
		"": false,
	} {
		got := validDropEndpoint(raw, doc) != ""
		require.Equal(t, want, got, raw)
	}
	require.NotEmpty(t, validDropEndpoint("http://127.0.0.1:9/d/", "http://127.0.0.1:9/m"))
}

func TestNewDropSlotIDIsSecretHash(t *testing.T) {
	s, err := newDropSlot("https://box.test/d/", "", "st")
	require.NoError(t, err)
	require.Len(t, s.id, 64)
	require.Equal(t, "https://box.test/d/"+s.id, s.redirectURI())
	s2, _ := newDropSlot("https://box.test/d/", "", "st")
	require.NotEqual(t, s.secret, s2.secret)
}

var (
	urlParse = url.Parse
	httpGet  = http.Get //nolint:gosec,noctx // loopback test server
)

func TestMCPOAuthDrop_CaseA_RefreshesDropHostTokenFirst(t *testing.T) {
	box := newFakeAuthServer(t, asOptions{drop: true, accessTokenTTL: 1})
	boxSrv := newFakeMCPServer(t, box, mcpOptions{})
	third := newFakeAuthServer(t, asOptions{})
	thirdSrv := newFakeMCPServer(t, third, mcpOptions{})
	mgr := newDropManager(t, map[string]ServerConfig{
		"agentbox": boxSrv.config(nil),
		"srv":      thirdSrv.config(nil),
	})
	require.NoError(t, login(t, mgr, "agentbox", browserCallbacks(t, nil)))
	require.NoError(t, login(t, mgr, "srv", browserCallbacks(t, nil)))
	_, refreshes, _ := box.counters()
	require.Equal(t, 1, refreshes, "a near-expiry drop host token is refreshed before opening the slot")
	require.True(t, strings.HasPrefix(third.lastRedirectURI(), box.URL()+fakeDropPrefix))
}

func TestMCPOAuthDrop_CaseA_DeadDropHostTokenFallsBack(t *testing.T) {
	box := newFakeAuthServer(t, asOptions{drop: true, accessTokenTTL: 1})
	boxSrv := newFakeMCPServer(t, box, mcpOptions{})
	third := newFakeAuthServer(t, asOptions{})
	thirdSrv := newFakeMCPServer(t, third, mcpOptions{})
	mgr := newDropManager(t, map[string]ServerConfig{
		"agentbox": boxSrv.config(nil),
		"srv":      thirdSrv.config(nil),
	})
	require.NoError(t, login(t, mgr, "agentbox", browserCallbacks(t, nil)))
	box.revokeAll()
	require.NoError(t, login(t, mgr, "srv", browserCallbacks(t, nil)))
	require.True(t, strings.HasPrefix(third.lastRedirectURI(), "http://127.0.0.1:"), third.lastRedirectURI())
	opens, _ := box.dropCounters()
	require.Zero(t, opens)
}

func TestDropSlotRejectsWrongState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"query": "code=c&state=evil"})
	}))
	t.Cleanup(srv.Close)
	s, err := newDropSlot(srv.URL+"/d/", "", "good")
	require.NoError(t, err)
	_, done, err := s.pollOnce(context.Background(), srv.Client())
	require.True(t, done)
	require.ErrorContains(t, err, "wrong state")
}
