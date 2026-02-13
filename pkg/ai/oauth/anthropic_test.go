package oauth

import (
	"encoding/json"
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
	if p.UsesCallbackServer() {
		t.Error("expected UsesCallbackServer() == false")
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

func TestAnthropicProvider_LoginRequiresOnPrompt(t *testing.T) {
	p := &AnthropicProvider{}
	_, err := p.Login(LoginCallbacks{
		OnAuth: func(info AuthInfo) {},
		// OnPrompt is nil
	})
	if err == nil {
		t.Error("expected error when OnPrompt is nil")
	}
	if !strings.Contains(err.Error(), "OnPrompt") {
		t.Errorf("expected error about OnPrompt, got: %v", err)
	}
}

func TestAnthropicLogin_AuthURLFormat(t *testing.T) {
	var capturedURL string
	callbacks := LoginCallbacks{
		OnAuth: func(info AuthInfo) {
			capturedURL = info.URL
		},
		OnPrompt: func(prompt Prompt) (string, error) {
			return "testcode#teststate", nil
		},
	}

	// Override token URL to a non-routable address so it fails fast
	origURL := anthropicTokenURL
	setAnthropicTokenURL("http://192.0.2.1:1/token") // RFC 5737 TEST-NET
	defer setAnthropicTokenURL(origURL)

	// Login will fail at the token exchange, but OnAuth should still be called
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
	tests := []struct {
		input string
		code  string
		state string
	}{
		{"abc123#mystate", "abc123", "mystate"},
		{"codeonly", "codeonly", ""},
		{"a#b#c", "a", "b#c"},
	}
	for _, tt := range tests {
		parts := strings.SplitN(tt.input, "#", 2)
		code := parts[0]
		state := ""
		if len(parts) > 1 {
			state = parts[1]
		}
		if code != tt.code {
			t.Errorf("input %q: code = %q, want %q", tt.input, code, tt.code)
		}
		if state != tt.state {
			t.Errorf("input %q: state = %q, want %q", tt.input, state, tt.state)
		}
	}
}

func TestAnthropicLogin_CodeWithoutState(t *testing.T) {
	callbacks := LoginCallbacks{
		OnAuth: func(_ AuthInfo) {},
		OnPrompt: func(prompt Prompt) (string, error) {
			return "justcode", nil // No # separator
		},
	}

	origURL := anthropicTokenURL
	setAnthropicTokenURL("http://192.0.2.1:1/token")
	defer setAnthropicTokenURL(origURL)

	// Will fail at HTTP request, but parsing should handle the code-only case
	_, err := loginAnthropic(callbacks)
	if err == nil {
		t.Skip("expected error from non-routable address")
	}
	// Error should be about the HTTP request, not about code parsing
	if strings.Contains(err.Error(), "index out of range") {
		t.Error("code parsing should not panic on code without state")
	}
}

// Verify AnthropicProvider implements the Provider interface.
var _ Provider = (*AnthropicProvider)(nil)
