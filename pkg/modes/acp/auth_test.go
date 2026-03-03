package acp

import (
	"context"
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/fir/pkg/core"
)

func TestBuildAuthMethods_EnvVarMethods(t *testing.T) {
	auth := core.NewInMemoryAuthStorage(nil)
	reg := core.NewModelRegistry(auth, "")

	methods := buildAuthMethods(auth, reg, acpsdk.ClientCapabilities{})

	// Should have env_var methods for providers that have models.
	envMethods := filterByType(methods, AuthMethodTypeEnvVar)
	if len(envMethods) == 0 {
		t.Fatal("expected at least one env_var auth method")
	}

	// Check that each env_var method has required fields.
	for _, m := range envMethods {
		if m.VarName == "" {
			t.Errorf("env_var method %q missing varName", m.Id)
		}
		if m.Type != AuthMethodTypeEnvVar {
			t.Errorf("method %q type = %q, want env_var", m.Id, m.Type)
		}
	}
}

func TestBuildAuthMethods_OAuthMethods(t *testing.T) {
	auth := core.NewInMemoryAuthStorage(nil)
	reg := core.NewModelRegistry(auth, "")

	methods := buildAuthMethods(auth, reg, acpsdk.ClientCapabilities{})

	// Without terminal-auth capability, all OAuth providers get type "agent".
	oauthMethods := filterByType(methods, AuthMethodTypeAgent)
	if len(oauthMethods) == 0 {
		t.Fatal("expected at least one OAuth agent method")
	}
	for _, m := range oauthMethods {
		if !strings.HasPrefix(m.Id, "oauth-") {
			continue
		}
		if m.Type != AuthMethodTypeAgent {
			t.Errorf("method %q type = %q, want agent", m.Id, m.Type)
		}
	}
}

func TestToSDKAuthMethods(t *testing.T) {
	methods := []ExtendedAuthMethod{
		{
			Id:          "env-openai",
			Name:        "OpenAI API Key",
			Description: "Set OPENAI_API_KEY",
			Type:        AuthMethodTypeEnvVar,
			VarName:     "OPENAI_API_KEY",
			Link:        "https://platform.openai.com/api-keys",
		},
		{
			Id:          "oauth-anthropic",
			Name:        "Anthropic OAuth",
			Description: "Login via OAuth",
			Type:        AuthMethodTypeAgent,
		},
	}

	sdk := toSDKAuthMethods(methods)
	if len(sdk) != 2 {
		t.Fatalf("got %d SDK methods, want 2", len(sdk))
	}

	// First method should have env_var meta.
	if sdk[0].Id != "env-openai" {
		t.Errorf("sdk[0].Id = %q, want env-openai", sdk[0].Id)
	}
	meta, ok := sdk[0].Meta.(map[string]any)
	if !ok {
		t.Fatal("sdk[0].Meta should be map[string]any")
	}
	if meta["type"] != "env_var" {
		t.Errorf("meta[type] = %v, want env_var", meta["type"])
	}
	if meta["varName"] != "OPENAI_API_KEY" {
		t.Errorf("meta[varName] = %v, want OPENAI_API_KEY", meta["varName"])
	}
}

func TestToSDKAuthMethods_Nil(t *testing.T) {
	sdk := toSDKAuthMethods(nil)
	if sdk != nil {
		t.Errorf("expected nil for empty input, got %v", sdk)
	}
}

func TestFormatProviderName(t *testing.T) {
	tests := []struct {
		pid  string
		want string
	}{
		{"openai", "OpenAI"},
		{"anthropic", "Anthropic"},
		{"github-copilot", "GitHub Copilot"},
		{"xai", "xAI"},
		{"some-new-provider", "Some New Provider"},
	}
	for _, tt := range tests {
		if got := formatProviderName(tt.pid); got != tt.want {
			t.Errorf("formatProviderName(%q) = %q, want %q", tt.pid, got, tt.want)
		}
	}
}

func TestHandleAuthenticate_UnknownMethod(t *testing.T) {
	pa := &firAgent{
		sessions:    make(map[string]*firSession),
		authMethods: []ExtendedAuthMethod{},
	}

	_, err := pa.handleAuthenticate(context.Background(), acpsdk.AuthenticateRequest{
		MethodId: "nonexistent",
	})
	if err == nil {
		t.Error("expected error for unknown method")
	}
}

func TestHandleAuthenticate_EnvVar_NotSet(t *testing.T) {
	pa := &firAgent{
		sessions: make(map[string]*firSession),
		authMethods: []ExtendedAuthMethod{
			{
				Id:      "env-openai",
				Name:    "OpenAI",
				Type:    AuthMethodTypeEnvVar,
				VarName: "OPENAI_API_KEY",
			},
		},
	}

	// Ensure the env var is not set.
	t.Setenv("OPENAI_API_KEY", "")

	_, err := pa.handleAuthenticate(context.Background(), acpsdk.AuthenticateRequest{
		MethodId: "env-openai",
	})
	if err == nil {
		t.Error("expected error when env var is not set")
	}
}

func TestHandleAuthenticate_EnvVar_Set(t *testing.T) {
	pa := &firAgent{
		sessions: make(map[string]*firSession),
		authMethods: []ExtendedAuthMethod{
			{
				Id:      "env-openai",
				Name:    "OpenAI",
				Type:    AuthMethodTypeEnvVar,
				VarName: "OPENAI_API_KEY",
			},
		},
	}

	t.Setenv("OPENAI_API_KEY", "sk-test-123")

	_, err := pa.handleAuthenticate(context.Background(), acpsdk.AuthenticateRequest{
		MethodId: "env-openai",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestHandleAuthenticate_Terminal(t *testing.T) {
	pa := &firAgent{
		sessions: make(map[string]*firSession),
		authMethods: []ExtendedAuthMethod{
			{
				Id:   "terminal-setup",
				Name: "Run in terminal",
				Type: AuthMethodTypeTerminal,
				Args: []string{"--setup"},
			},
		},
	}

	_, err := pa.handleAuthenticate(context.Background(), acpsdk.AuthenticateRequest{
		MethodId: "terminal-setup",
	})
	if err != nil {
		t.Errorf("terminal auth should succeed (no-op): %v", err)
	}
}

func TestBuildAuthMethods_TerminalAuthCapability(t *testing.T) {
	auth := core.NewInMemoryAuthStorage(nil)
	reg := core.NewModelRegistry(auth, "")

	// Simulate a client that supports terminal-auth (like Zed).
	caps := acpsdk.ClientCapabilities{
		Meta: map[string]any{"terminal-auth": true},
	}
	methods := buildAuthMethods(auth, reg, caps)

	// OAuth methods should have _meta["terminal-auth"] with command info.
	var oauthMethods []ExtendedAuthMethod
	for _, m := range methods {
		if strings.HasPrefix(m.Id, "oauth-") {
			oauthMethods = append(oauthMethods, m)
		}
	}
	if len(oauthMethods) == 0 {
		t.Fatal("expected at least one OAuth method")
	}
	for _, m := range oauthMethods {
		if m.Meta == nil {
			t.Errorf("method %q should have _meta for terminal-auth", m.Id)
			continue
		}
		ta, ok := m.Meta["terminal-auth"].(map[string]any)
		if !ok {
			t.Errorf("method %q _meta[terminal-auth] should be a map", m.Id)
			continue
		}
		if _, ok := ta["command"]; !ok {
			t.Errorf("method %q terminal-auth missing command", m.Id)
		}
		args, ok := ta["args"].([]string)
		if !ok || len(args) < 2 || args[0] != "--login" {
			t.Errorf("method %q terminal-auth args should start with --login, got %v", m.Id, ta["args"])
		}
	}
}

func TestAuthenticateOAuth_RejectsManualCodeProvider(t *testing.T) {
	// Anthropic doesn't use a callback server, so authenticateOAuth should
	// reject it immediately without making any network calls.
	auth := core.NewInMemoryAuthStorage(nil)
	pa := &firAgent{
		sessions:    make(map[string]*firSession),
		authStorage: auth,
		authMethods: []ExtendedAuthMethod{
			{
				Id:   "oauth-anthropic",
				Name: "Anthropic",
				Type: AuthMethodTypeAgent,
			},
		},
	}

	_, err := pa.handleAuthenticate(context.Background(), acpsdk.AuthenticateRequest{
		MethodId: "oauth-anthropic",
	})
	if err == nil {
		t.Fatal("expected error for provider without callback server")
	}
	if !strings.Contains(err.Error(), "interactive input") {
		t.Errorf("expected error about interactive input, got: %v", err)
	}
}

func TestAuthenticateOAuth_NilAuthStorage(t *testing.T) {
	pa := &firAgent{
		sessions:    make(map[string]*firSession),
		authStorage: nil,
		authMethods: []ExtendedAuthMethod{
			{
				Id:   "oauth-anthropic",
				Name: "Anthropic",
				Type: AuthMethodTypeAgent,
			},
		},
	}

	_, err := pa.handleAuthenticate(context.Background(), acpsdk.AuthenticateRequest{
		MethodId: "oauth-anthropic",
	})
	if err == nil {
		t.Error("expected error when authStorage is nil")
	}
}

func filterByType(methods []ExtendedAuthMethod, t AuthMethodType) []ExtendedAuthMethod {
	var result []ExtendedAuthMethod
	for _, m := range methods {
		if m.Type == t {
			result = append(result, m)
		}
	}
	return result
}
