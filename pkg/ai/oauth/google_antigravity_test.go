package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestAntigravityProvider_IDAndName(t *testing.T) {
	p := &AntigravityProvider{}
	if p.ID() != "google-antigravity" {
		t.Errorf("ID() = %q", p.ID())
	}
	if p.Name() != "Antigravity (Gemini 3, Claude, GPT-OSS)" {
		t.Errorf("Name() = %q", p.Name())
	}
	if !p.UsesCallbackServer() {
		t.Error("expected UsesCallbackServer() == true")
	}
}

func TestAntigravityProvider_GetAPIKey(t *testing.T) {
	p := &AntigravityProvider{}
	creds := &Credentials{
		Access: "ya29.test-token",
		Extra:  map[string]any{"projectId": "my-project-123"},
	}
	key := p.GetAPIKey(creds)
	var parsed map[string]string
	if err := json.Unmarshal([]byte(key), &parsed); err != nil {
		t.Fatalf("GetAPIKey returned invalid JSON: %v", err)
	}
	if parsed["token"] != "ya29.test-token" {
		t.Errorf("token = %q", parsed["token"])
	}
	if parsed["projectId"] != "my-project-123" {
		t.Errorf("projectId = %q", parsed["projectId"])
	}
}

func TestAntigravityProvider_RefreshToken_MissingProjectID(t *testing.T) {
	p := &AntigravityProvider{}
	_, err := p.RefreshToken(&Credentials{Refresh: "rt-test"})
	if err == nil {
		t.Error("expected error when projectId is missing")
	}
}

func TestAntigravityProvider_ModifyModels(t *testing.T) {
	p := &AntigravityProvider{}
	result := p.ModifyModels(nil, nil)
	if result != nil {
		t.Error("expected nil returned")
	}
}

func TestAntigravityClientID_Decoded(t *testing.T) {
	if antigravityClientID == "" {
		t.Error("antigravityClientID should be decoded")
	}
	// Should contain ".apps.googleusercontent.com"
	if len(antigravityClientID) < 20 {
		t.Errorf("clientID suspiciously short: %q", antigravityClientID)
	}
}

func TestAntigravityClientSecret_Decoded(t *testing.T) {
	if antigravityClientSecret == "" {
		t.Error("antigravityClientSecret should be decoded")
	}
	if len(antigravityClientSecret) < 10 {
		t.Errorf("clientSecret suspiciously short: %q", antigravityClientSecret)
	}
}

func TestParseRedirectURL(t *testing.T) {
	tests := []struct {
		input     string
		wantCode  string
		wantState string
	}{
		{
			"http://localhost:51121/oauth-callback?code=4/abc&state=verifier123",
			"4/abc", "verifier123",
		},
		{
			"http://localhost:51121/oauth-callback?code=xyz",
			"xyz", "",
		},
		{"", "", ""},
		{"not-a-url", "", ""},
		{"  http://localhost:51121/oauth-callback?code=test&state=st  ", "test", "st"},
		// Shell-escaped URL (backslashes from terminal copy-paste)
		{`http://localhost:51121/oauth-callback\?code\=4/abc\&state\=verifier123`, "4/abc", "verifier123"},
	}
	for _, tt := range tests {
		code, state := parseRedirectURL(tt.input)
		if code != tt.wantCode {
			t.Errorf("parseRedirectURL(%q) code = %q, want %q", tt.input, code, tt.wantCode)
		}
		if state != tt.wantState {
			t.Errorf("parseRedirectURL(%q) state = %q, want %q", tt.input, state, tt.wantState)
		}
	}
}

func TestAntigravityScopes(t *testing.T) {
	if len(antigravityScopes) != 5 {
		t.Errorf("expected 5 scopes, got %d", len(antigravityScopes))
	}
}

// Verify AntigravityProvider implements the Provider interface.
var _ Provider = (*AntigravityProvider)(nil)

func TestManualCodeInput(t *testing.T) {
	code, err := manualCodeInput(func() (string, error) {
		return `http://localhost:51121/oauth-callback\?code\=4/abc\&state\=v123`, nil
	}, "v123")
	if err != nil {
		t.Fatal(err)
	}
	if code != "4/abc" {
		t.Errorf("got code %q, want %q", code, "4/abc")
	}
}

func TestManualCodeInput_StateMismatch(t *testing.T) {
	_, err := manualCodeInput(func() (string, error) {
		return "http://localhost:51121/oauth-callback?code=x&state=wrong", nil
	}, "expected")
	if err == nil || err.Error() != "OAuth state mismatch - possible CSRF attack" {
		t.Errorf("expected state mismatch error, got %v", err)
	}
}

func TestManualCodeInput_Error(t *testing.T) {
	_, err := manualCodeInput(func() (string, error) {
		return "", fmt.Errorf("cancelled")
	}, "v")
	if err == nil || err.Error() != "cancelled" {
		t.Errorf("expected cancelled error, got %v", err)
	}
}

func TestRaceCallbackAndManual_NilChannel(t *testing.T) {
	ctx := context.Background()
	code, err := raceCallbackAndManual(ctx, nil, func() (string, error) {
		return "http://localhost:51121/oauth-callback?code=manual_code&state=v1", nil
	}, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if code != "manual_code" {
		t.Errorf("got code %q, want %q", code, "manual_code")
	}
}
