package mcp

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/kfet/agent"
	"github.com/kfet/pinoauth"
	"github.com/stretchr/testify/require"
)

// remoteBrowserCallbacks plays a browser that is NOT on the agent host: it
// walks the authorization URL but never follows the final redirect to the
// loopback listener. The landing URL is handed back through
// OnManualCodeInput — what a chat relay does with the URL the user pastes.
// mangle, when non-nil, edits the landing URL before it is "pasted".
func remoteBrowserCallbacks(t *testing.T, mangle func(*url.URL)) pinoauth.LoginCallbacks {
	t.Helper()
	landing := make(chan string, 1)
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	return pinoauth.LoginCallbacks{
		OnAuth: func(info pinoauth.AuthInfo) {
			resp, err := noFollow.Get(info.URL) //nolint:noctx // loopback test server
			if err != nil {
				t.Errorf("remote browser fetch: %v", err)
				close(landing)
				return
			}
			defer resp.Body.Close() //nolint:errcheck
			loc, err := url.Parse(resp.Header.Get("Location"))
			if resp.StatusCode != http.StatusFound || err != nil {
				t.Errorf("authorize ended with HTTP %d, Location err %v", resp.StatusCode, err)
				close(landing)
				return
			}
			if mangle != nil {
				mangle(loc)
			}
			landing <- loc.String()
		},
		OnManualCodeInput: func() (string, error) {
			v, ok := <-landing
			if !ok {
				return "", context.Canceled
			}
			return v, nil
		},
	}
}

func TestMCPOAuth_PastedLandingURL_ReconnectsRunningServer(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{})
	mgr, _ := newTestManager(t, srv.config(nil))

	// Only non-empty lists count: the failed initial connect publishes none.
	toolsPublished := make(chan struct{}, 1)
	mgr.SetOnToolsChanged(func(tools []agent.AgentTool) {
		if len(tools) > 0 {
			select {
			case toolsPublished <- struct{}{}:
			default:
			}
		}
	})

	// The running manager's first connect fails: no credential yet.
	var authErr *AuthRequiredError
	require.ErrorAs(t, connectAndWait(t, mgr), &authErr)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	require.NoError(t, mgr.LoginServer(ctx, "srv", remoteBrowserCallbacks(t, nil)))

	select {
	case <-toolsPublished:
	case <-ctx.Done():
		t.Fatal("reconnect after login never published the server's tools")
	}
	requireToolsWork(t, mgr)
}

func TestMCPOAuth_PastedLandingURL_StateMismatchRejected(t *testing.T) {
	as := newFakeAuthServer(t, asOptions{})
	srv := newFakeMCPServer(t, as, mcpOptions{})
	mgr, _ := newTestManager(t, srv.config(nil))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := mgr.LoginServer(ctx, "srv", remoteBrowserCallbacks(t, func(u *url.URL) {
		q := u.Query()
		q.Set("state", "forged")
		u.RawQuery = q.Encode()
	}))
	require.ErrorContains(t, err, "state mismatch")
}
