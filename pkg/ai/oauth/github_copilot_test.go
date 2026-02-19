package oauth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kfet/tau/pkg/ai"
)

func TestGitHubCopilotProvider_IDAndName(t *testing.T) {
	p := &GitHubCopilotProvider{}
	if p.ID() != "github-copilot" {
		t.Errorf("ID() = %q", p.ID())
	}
	if p.Name() != "GitHub Copilot" {
		t.Errorf("Name() = %q", p.Name())
	}
	if p.UsesCallbackServer() {
		t.Error("expected UsesCallbackServer() == false")
	}
}

func TestGitHubCopilotProvider_GetAPIKey(t *testing.T) {
	p := &GitHubCopilotProvider{}
	creds := &Credentials{Access: "ghu_test_token"}
	if got := p.GetAPIKey(creds); got != "ghu_test_token" {
		t.Errorf("GetAPIKey() = %q", got)
	}
}

func TestGitHubCopilotProvider_ModifyModels(t *testing.T) {
	p := &GitHubCopilotProvider{}
	models := []*ai.Model{
		{ID: "gpt-4o", Provider: "github-copilot", BaseURL: "https://old.example.com"},
		{ID: "claude-3.5", Provider: "anthropic", BaseURL: "https://api.anthropic.com"},
	}
	token := "tid=123;exp=999;proxy-ep=proxy.individual.githubcopilot.com;st=ok"
	creds := &Credentials{Access: token}

	result := p.ModifyModels(models, creds)
	if len(result) != 2 {
		t.Fatalf("expected 2 models, got %d", len(result))
	}
	// Copilot model should have updated baseURL
	if result[0].BaseURL != "https://api.individual.githubcopilot.com" {
		t.Errorf("copilot model baseURL = %q", result[0].BaseURL)
	}
	// Non-copilot model should be unchanged
	if result[1].BaseURL != "https://api.anthropic.com" {
		t.Errorf("anthropic model baseURL = %q", result[1].BaseURL)
	}
}

func TestNormalizeDomain(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"github.com", "github.com"},
		{"https://github.com", "github.com"},
		{"https://github.com/path", "github.com"},
		{"company.ghe.com", "company.ghe.com"},
		{"https://company.ghe.com", "company.ghe.com"},
		{"  github.com  ", "github.com"},
		{"", ""},
		{"   ", ""},
	}
	for _, tt := range tests {
		got := NormalizeDomain(tt.input)
		if got != tt.want {
			t.Errorf("NormalizeDomain(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestGetBaseURLFromToken(t *testing.T) {
	tests := []struct {
		token string
		want  string
	}{
		{"tid=123;exp=999;proxy-ep=proxy.individual.githubcopilot.com;st=ok", "https://api.individual.githubcopilot.com"},
		{"tid=456;exp=999;proxy-ep=proxy.business.githubcopilot.com;st=ok", "https://api.business.githubcopilot.com"},
		{"tid=789;exp=999;st=ok", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := GetBaseURLFromToken(tt.token)
		if got != tt.want {
			t.Errorf("GetBaseURLFromToken(%q) = %q, want %q", tt.token, got, tt.want)
		}
	}
}

func TestGetGitHubCopilotBaseURL(t *testing.T) {
	// With token containing proxy-ep
	got := GetGitHubCopilotBaseURL("tid=1;proxy-ep=proxy.test.com;st=ok", "")
	if got != "https://api.test.com" {
		t.Errorf("with token = %q", got)
	}

	// Without token, with enterprise domain
	got = GetGitHubCopilotBaseURL("", "company.ghe.com")
	if got != "https://copilot-api.company.ghe.com" {
		t.Errorf("with enterprise = %q", got)
	}

	// Neither
	got = GetGitHubCopilotBaseURL("", "")
	if got != "https://api.individual.githubcopilot.com" {
		t.Errorf("default = %q", got)
	}
}

func TestGitHubCopilotProvider_LoginRequiresOnPrompt(t *testing.T) {
	p := &GitHubCopilotProvider{}
	_, err := p.Login(LoginCallbacks{
		OnAuth: func(info AuthInfo) {},
	})
	if err == nil {
		t.Error("expected error when OnPrompt is nil")
	}
}

func TestGitHubClientID_Decoded(t *testing.T) {
	if githubClientID == "" || githubClientID == "unknown" {
		t.Error("githubClientID should be decoded")
	}
}

func TestGitHubURLs(t *testing.T) {
	dc, at, ct := githubURLs("github.com")
	if dc != "https://github.com/login/device/code" {
		t.Errorf("deviceCodeURL = %q", dc)
	}
	if at != "https://github.com/login/oauth/access_token" {
		t.Errorf("accessTokenURL = %q", at)
	}
	if ct != "https://api.github.com/copilot_internal/v2/token" {
		t.Errorf("copilotTokenURL = %q", ct)
	}

	// Enterprise
	dc2, _, ct2 := githubURLs("company.ghe.com")
	if dc2 != "https://company.ghe.com/login/device/code" {
		t.Errorf("enterprise deviceCodeURL = %q", dc2)
	}
	if ct2 != "https://api.company.ghe.com/copilot_internal/v2/token" {
		t.Errorf("enterprise copilotTokenURL = %q", ct2)
	}
}

// ---------------------------------------------------------------------------
// pollForGitHubAccessToken tests
// ---------------------------------------------------------------------------

// TestPollForGitHubAccessToken_Success verifies the happy path: server returns
// an access_token on the first poll.
func TestPollForGitHubAccessToken_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"ghu_test123"}`)
	}))
	defer ts.Close()

	origURL := githubAccessTokenURLOverride
	origUnit := pollIntervalUnit
	githubAccessTokenURLOverride = ts.URL + "/login/oauth/access_token"
	pollIntervalUnit = time.Millisecond
	defer func() {
		githubAccessTokenURLOverride = origURL
		pollIntervalUnit = origUnit
	}()

	token, err := pollForGitHubAccessToken(context.Background(), "github.com", "device123", 5, 60)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "ghu_test123" {
		t.Errorf("token = %q, want %q", token, "ghu_test123")
	}
}

// TestPollForGitHubAccessToken_AuthorizationPending verifies that the poller
// retries on authorization_pending and succeeds when the server eventually
// returns an access_token.
func TestPollForGitHubAccessToken_AuthorizationPending(t *testing.T) {
	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount < 3 {
			fmt.Fprint(w, `{"error":"authorization_pending"}`)
		} else {
			fmt.Fprint(w, `{"access_token":"ghu_pending_ok"}`)
		}
	}))
	defer ts.Close()

	origURL := githubAccessTokenURLOverride
	origUnit := pollIntervalUnit
	githubAccessTokenURLOverride = ts.URL + "/token"
	pollIntervalUnit = time.Millisecond
	defer func() {
		githubAccessTokenURLOverride = origURL
		pollIntervalUnit = origUnit
	}()

	token, err := pollForGitHubAccessToken(context.Background(), "github.com", "dev456", 1, 60)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "ghu_pending_ok" {
		t.Errorf("token = %q, want %q", token, "ghu_pending_ok")
	}
	if callCount < 3 {
		t.Errorf("expected at least 3 calls, got %d", callCount)
	}
}

// TestPollForGitHubAccessToken_SlowDown verifies that slow_down increases the
// polling interval and the poller eventually succeeds.
func TestPollForGitHubAccessToken_SlowDown(t *testing.T) {
	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			fmt.Fprint(w, `{"error":"slow_down"}`)
		} else {
			fmt.Fprint(w, `{"access_token":"ghu_slow_ok"}`)
		}
	}))
	defer ts.Close()

	origURL := githubAccessTokenURLOverride
	origUnit := pollIntervalUnit
	githubAccessTokenURLOverride = ts.URL + "/token"
	pollIntervalUnit = time.Millisecond
	defer func() {
		githubAccessTokenURLOverride = origURL
		pollIntervalUnit = origUnit
	}()

	token, err := pollForGitHubAccessToken(context.Background(), "github.com", "dev789", 1, 60)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "ghu_slow_ok" {
		t.Errorf("token = %q, want %q", token, "ghu_slow_ok")
	}
	if callCount != 2 {
		t.Errorf("expected 2 server calls (slow_down + success), got %d", callCount)
	}
}

// TestPollForGitHubAccessToken_Timeout verifies that the poller returns an
// error when expiresIn has already passed.
func TestPollForGitHubAccessToken_Timeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"error":"authorization_pending"}`)
	}))
	defer ts.Close()

	origURL := githubAccessTokenURLOverride
	origUnit := pollIntervalUnit
	githubAccessTokenURLOverride = ts.URL + "/token"
	pollIntervalUnit = time.Millisecond
	defer func() {
		githubAccessTokenURLOverride = origURL
		pollIntervalUnit = origUnit
	}()

	_, err := pollForGitHubAccessToken(context.Background(), "github.com", "devXXX", 1, 0)
	if err == nil {
		t.Error("expected timeout error, got nil")
	}
}

// TestPollForGitHubAccessToken_Cancellation verifies that cancelling the context
// stops the poller.
func TestPollForGitHubAccessToken_Cancellation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"error":"authorization_pending"}`)
	}))
	defer ts.Close()

	origURL := githubAccessTokenURLOverride
	origUnit := pollIntervalUnit
	githubAccessTokenURLOverride = ts.URL + "/token"
	pollIntervalUnit = time.Millisecond
	defer func() {
		githubAccessTokenURLOverride = origURL
		pollIntervalUnit = origUnit
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := pollForGitHubAccessToken(ctx, "github.com", "devCTX", 1, 60)
	if err == nil {
		t.Error("expected cancellation error, got nil")
	}
}

// TestPollForGitHubAccessToken_FatalError verifies that an unrecognized error
// code (e.g. "expired_token") stops the poller immediately.
func TestPollForGitHubAccessToken_FatalError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"error":"expired_token"}`)
	}))
	defer ts.Close()

	origURL := githubAccessTokenURLOverride
	origUnit := pollIntervalUnit
	githubAccessTokenURLOverride = ts.URL + "/token"
	pollIntervalUnit = time.Millisecond
	defer func() {
		githubAccessTokenURLOverride = origURL
		pollIntervalUnit = origUnit
	}()

	_, err := pollForGitHubAccessToken(context.Background(), "github.com", "devFATAL", 1, 60)
	if err == nil {
		t.Error("expected error for expired_token, got nil")
	}
	if !strings.Contains(err.Error(), "expired_token") {
		t.Errorf("error should mention expired_token, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// refreshGitHubCopilotToken tests
// ---------------------------------------------------------------------------

func TestRefreshGitHubCopilotToken_Success(t *testing.T) {
	expiresAt := time.Now().Add(time.Hour).Unix()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ghu_refresh_token" {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"token":"copilot_api_token","expires_at":%d}`, expiresAt)
	}))
	defer ts.Close()

	origURL := githubCopilotTokenURLOverride
	githubCopilotTokenURLOverride = ts.URL + "/copilot/token"
	defer func() { githubCopilotTokenURLOverride = origURL }()

	creds, err := refreshGitHubCopilotToken("ghu_refresh_token", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.Access != "copilot_api_token" {
		t.Errorf("Access = %q, want %q", creds.Access, "copilot_api_token")
	}
	if creds.Refresh != "ghu_refresh_token" {
		t.Errorf("Refresh = %q, want %q", creds.Refresh, "ghu_refresh_token")
	}
}

func TestRefreshGitHubCopilotToken_Error(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer ts.Close()

	origURL := githubCopilotTokenURLOverride
	githubCopilotTokenURLOverride = ts.URL + "/copilot/token"
	defer func() { githubCopilotTokenURLOverride = origURL }()

	_, err := refreshGitHubCopilotToken("bad_token", "")
	if err == nil {
		t.Error("expected error for HTTP 401, got nil")
	}
}

func TestRefreshGitHubCopilotToken_MissingToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"expires_at":9999}`) // no "token" field
	}))
	defer ts.Close()

	origURL := githubCopilotTokenURLOverride
	githubCopilotTokenURLOverride = ts.URL + "/copilot/token"
	defer func() { githubCopilotTokenURLOverride = origURL }()

	_, err := refreshGitHubCopilotToken("ghu_token", "")
	if err == nil {
		t.Error("expected error when token field missing, got nil")
	}
}

// ---------------------------------------------------------------------------
// enableCopilotModel / enableAllCopilotModels tests
// ---------------------------------------------------------------------------

func TestEnableCopilotModel_Success(t *testing.T) {
	var receivedPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	origBase := githubCopilotBaseURLOverride
	githubCopilotBaseURLOverride = ts.URL
	defer func() { githubCopilotBaseURLOverride = origBase }()

	ok := enableCopilotModel("api_token", "gpt-4o", "")
	if !ok {
		t.Error("expected true for 200 OK")
	}
	if receivedPath != "/models/gpt-4o/policy" {
		t.Errorf("path = %q, want %q", receivedPath, "/models/gpt-4o/policy")
	}
}

func TestEnableCopilotModel_Failure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer ts.Close()

	origBase := githubCopilotBaseURLOverride
	githubCopilotBaseURLOverride = ts.URL
	defer func() { githubCopilotBaseURLOverride = origBase }()

	ok := enableCopilotModel("api_token", "gpt-4o", "")
	if ok {
		t.Error("expected false for HTTP 403")
	}
}

func TestEnableAllCopilotModels_DoesNotPanic(t *testing.T) {
	// enableAllCopilotModels iterates over known models and fires best-effort
	// requests; it should never panic even when all requests fail.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	defer ts.Close()

	origBase := githubCopilotBaseURLOverride
	githubCopilotBaseURLOverride = ts.URL
	defer func() { githubCopilotBaseURLOverride = origBase }()

	// Should complete without panicking.
	enableAllCopilotModels("test_token", "")
}

// Verify GitHubCopilotProvider implements the Provider interface.
var _ Provider = (*GitHubCopilotProvider)(nil)
