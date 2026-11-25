package acp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/extension"
)

// TestInteractiveAuth_ManualRedirectPaste drives the relay's interactive
// ACP login against a real generic provider that declares a
// manual_redirect_uri: call 1 must surface URL and ShortURL with the
// manual redirect_uri, and the code#state pasted in call 2 must be
// exchanged with that same redirect_uri.
func TestInteractiveAuth_ManualRedirectPaste(t *testing.T) {
	const manual = "https://manual.example/code/callback"
	var (
		mu       sync.Mutex
		tokenReq url.Values
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		tokenReq, _ = url.ParseQuery(string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"AT","refresh_token":"RT","expires_in":3600}`)
	}))
	defer srv.Close()

	ai.ResetOAuthProviders()
	defer ai.ResetOAuthProviders()
	bridge := extension.NewBridge(nil, &extension.InitResult{
		Name: "test-ext",
		AuthProviders: []extension.AuthProviderSpec{{
			ID: "manual-prov", Name: "Manual", UsesCallbackServer: true,
			Flow: &extension.OAuthFlowSpec{
				ClientID: "cid", AuthorizeURL: srv.URL + "/authorize", TokenURL: srv.URL + "/token",
				CallbackAddr: "127.0.0.1:0", CallbackPath: "/callback",
				ManualRedirectURI:   manual,
				ShortURLBase:        "https://short.example/x",
				OpenURLInstructions: "Sign in, then copy the code shown on the page and paste it here.",
			},
		}},
	})
	bridge.RegisterAuthProviders()
	defer bridge.UnregisterAuthProviders()

	pa := &firAgent{
		sessions:     make(map[string]*firSession),
		pendingAuths: make(map[string]*pendingAuth),
		authStorage:  auth.NewInMemoryAuthStorage(nil),
		authMethods:  []ExtendedAuthMethod{{Id: "oauth-manual-prov", Name: "Manual", Type: AuthMethodTypeAgent}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	resp, err := pa.handleAuthenticate(ctx, acpsdk.AuthenticateRequest{
		MethodId: "oauth-manual-prov",
		Meta:     map[string]any{"auth": map[string]any{"interactive": true}},
	})
	if err != nil {
		t.Fatalf("call 1: %v", err)
	}
	state, id, authURL, instr := authMetaFromResponse(t, resp)
	if state != authStateNeedsRedirect {
		t.Fatalf("state = %q", state)
	}
	if !strings.Contains(instr, "copy the code shown on the page and paste it here") {
		t.Errorf("instructions = %q", instr)
	}
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("redirect_uri"); got != manual {
		t.Errorf("URL redirect_uri = %q, want manual", got)
	}
	shortURL, _ := resp.Meta["auth"].(map[string]any)["short_url"].(string)
	su, err := url.Parse(shortURL)
	if err != nil || !strings.HasPrefix(shortURL, "https://short.example/x?") {
		t.Fatalf("short_url = %q", shortURL)
	}
	if got := su.Query().Get("redirect_uri"); got != manual {
		t.Errorf("ShortURL redirect_uri = %q, want manual", got)
	}

	resp2, err := pa.handleAuthenticate(ctx, acpsdk.AuthenticateRequest{
		MethodId: "oauth-manual-prov",
		Meta: map[string]any{"auth": map[string]any{
			"interactive": true, "id": id,
			"redirect": "PASTEDCODE#" + u.Query().Get("state"),
		}},
	})
	if err != nil {
		t.Fatalf("call 2: %v", err)
	}
	if s, _, _, _ := authMetaFromResponse(t, resp2); s != authStateOK {
		t.Fatalf("call 2 state = %q", s)
	}
	mu.Lock()
	defer mu.Unlock()
	if tokenReq.Get("code") != "PASTEDCODE" || tokenReq.Get("redirect_uri") != manual {
		t.Errorf("token request = %v, want code=PASTEDCODE redirect_uri=manual", tokenReq)
	}
}
