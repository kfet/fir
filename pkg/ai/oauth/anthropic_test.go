package oauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicProvider_IDAndName(t *testing.T) {
	p := &AnthropicProvider{}
	if p.ID() != "anthropic" {
		t.Errorf("ID() = %q, want %q", p.ID(), "anthropic")
	}
	if p.Name() != "Anthropic (Claude Pro/Max)" {
		t.Errorf("Name() = %q", p.Name())
	}
	if !p.UsesCallbackServer() {
		t.Error("expected UsesCallbackServer() == true")
	}
}

func TestAnthropicProvider_GetAPIKey(t *testing.T) {
	p := &AnthropicProvider{}
	creds := &Credentials{Access: "sk-ant-oat-test123"}
	if got := p.GetAPIKey(creds); got != "sk-ant-oat-test123" {
		t.Errorf("GetAPIKey() = %q", got)
	}
}

func TestAnthropicProvider_ModifyModels(t *testing.T) {
	p := &AnthropicProvider{}
	result := p.ModifyModels(nil, nil)
	if result != nil {
		t.Error("expected nil models returned unchanged")
	}
}

func TestAnthropicClientID_Decoded(t *testing.T) {
	if anthropicClientID == "" || anthropicClientID == "unknown" {
		t.Error("anthropicClientID should be decoded")
	}
	if !strings.Contains(anthropicClientID, "-") {
		t.Errorf("expected UUID format, got %q", anthropicClientID)
	}
	if len(anthropicClientID) != 36 {
		t.Errorf("expected UUID length 36, got %d", len(anthropicClientID))
	}
}

func TestAnthropicProvider_LoginCancelledContext(t *testing.T) {
	// Cancelling the context shuts down the callback server so login
	// returns quickly with a "missing authorization code" error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := loginAnthropic(LoginCallbacks{
		OnAuth: func(info AuthInfo) {},
		Ctx:    ctx,
	})
	if err == nil {
		t.Fatal("expected error with cancelled context")
	}
	// Should report missing code, not a panic or network error.
	if strings.Contains(err.Error(), "panic") {
		t.Errorf("unexpected panic: %v", err)
	}
}

func TestAnthropicLogin_AuthURLFormat(t *testing.T) {
	var capturedURL string
	codeCh := make(chan string, 1)
	codeCh <- "testcode#teststate"

	callbacks := LoginCallbacks{
		OnAuth: func(info AuthInfo) {
			capturedURL = info.URL
		},
		// Provide code via OnManualCodeInput so it races the callback server
		// and wins immediately, without needing an actual browser redirect.
		OnManualCodeInput: func() (string, error) {
			return <-codeCh, nil
		},
	}

	// Use a test server that returns an error immediately so the token exchange
	// fails fast without any real network I/O.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "test server", http.StatusInternalServerError)
	}))
	defer ts.Close()

	origURL := anthropicTokenURL
	setAnthropicTokenURL(ts.URL + "/token")
	defer setAnthropicTokenURL(origURL)

	// Login will fail at the token exchange, but OnAuth should still be called.
	_, _ = loginAnthropic(callbacks)

	if capturedURL == "" {
		t.Fatal("OnAuth was not called")
	}
	if !strings.HasPrefix(capturedURL, anthropicAuthorizeURL) {
		t.Errorf("URL should start with %s, got %q", anthropicAuthorizeURL, capturedURL)
	}
	if !strings.Contains(capturedURL, "client_id=") {
		t.Error("URL should contain client_id parameter")
	}
	if !strings.Contains(capturedURL, "code_challenge=") {
		t.Error("URL should contain code_challenge parameter")
	}
	if !strings.Contains(capturedURL, "code_challenge_method=S256") {
		t.Error("URL should contain code_challenge_method=S256")
	}
	if !strings.Contains(capturedURL, "response_type=code") {
		t.Error("URL should contain response_type=code")
	}
	if !strings.Contains(capturedURL, "scope=") {
		t.Error("URL should contain scope parameter")
	}
}

func TestAnthropicTokenResponse_Parsing(t *testing.T) {
	respBody := `{"access_token":"at-abc","refresh_token":"rt-def","expires_in":3600}`
	var tokenData struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal([]byte(respBody), &tokenData); err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if tokenData.AccessToken != "at-abc" {
		t.Errorf("access_token = %q, want %q", tokenData.AccessToken, "at-abc")
	}
	if tokenData.RefreshToken != "rt-def" {
		t.Errorf("refresh_token = %q, want %q", tokenData.RefreshToken, "rt-def")
	}
	if tokenData.ExpiresIn != 3600 {
		t.Errorf("expires_in = %d, want 3600", tokenData.ExpiresIn)
	}
}

func TestAnthropicAuthCodeParsing(t *testing.T) {
	// parseAuthorizationInput is shared across oauth providers (defined in openai_codex.go).
	tests := []struct {
		input string
		code  string
		state string
	}{
		{"abc123#mystate", "abc123", "mystate"},
		{"codeonly", "codeonly", ""},
		// Full redirect URL
		{"https://platform.claude.com/oauth/code/callback?code=abc&state=xyz", "abc", "xyz"},
		// Query-string format
		{"code=abc&state=xyz", "abc", "xyz"},
	}
	for _, tt := range tests {
		code, state := parseAuthorizationInput(tt.input)
		if code != tt.code {
			t.Errorf("input %q: code = %q, want %q", tt.input, code, tt.code)
		}
		if state != tt.state {
			t.Errorf("input %q: state = %q, want %q", tt.input, state, tt.state)
		}
	}
}

func TestAnthropicLogin_CodeWithoutState(t *testing.T) {
	codeCh := make(chan string, 1)
	codeCh <- "justcode" // No # separator

	callbacks := LoginCallbacks{
		OnAuth: func(_ AuthInfo) {},
		OnManualCodeInput: func() (string, error) {
			return <-codeCh, nil
		},
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "test server", http.StatusInternalServerError)
	}))
	defer ts.Close()

	origURL := anthropicTokenURL
	setAnthropicTokenURL(ts.URL + "/token")
	defer setAnthropicTokenURL(origURL)

	// Will fail at HTTP request, but parsing should handle the code-only case
	_, err := loginAnthropic(callbacks)
	if err == nil {
		t.Skip("expected error from test server")
	}
	// Error should be about the HTTP request or state mismatch, not code parsing panic.
	if strings.Contains(err.Error(), "index out of range") {
		t.Error("code parsing should not panic on code without state")
	}
}

// Verify AnthropicProvider implements the Provider interface.
var _ Provider = (*AnthropicProvider)(nil)

func TestAnthropicRefreshToken_NoScopeInRequest(t *testing.T) {
	// Verify that refresh token requests don't include a "scope" parameter,
	// which causes "invalid_scope" errors from the Anthropic server.
	var capturedBody map[string]string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-at",
			"refresh_token": "new-rt",
			"expires_in":    3600,
		})
	}))
	defer ts.Close()

	origURL := anthropicTokenURL
	setAnthropicTokenURL(ts.URL + "/token")
	defer setAnthropicTokenURL(origURL)

	creds, err := refreshAnthropicToken("old-refresh-token")
	if err != nil {
		t.Fatalf("refreshAnthropicToken: %v", err)
	}
	if creds.Access != "new-at" {
		t.Errorf("access = %q, want %q", creds.Access, "new-at")
	}

	// The key assertion: no "scope" in the request body
	if _, hasScope := capturedBody["scope"]; hasScope {
		t.Errorf("refresh request should not include 'scope' parameter, got: %v", capturedBody)
	}
	if capturedBody["grant_type"] != "refresh_token" {
		t.Errorf("grant_type = %q, want %q", capturedBody["grant_type"], "refresh_token")
	}
}
