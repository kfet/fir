package acp

import (
	"context"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/fir/pkg/core"
)

func TestBuildAuthMethods_EnvVarMethods(t *testing.T) {
	auth := core.NewInMemoryAuthStorage(nil)
	reg := core.NewModelRegistry(auth, "")

	methods := buildAuthMethods(auth, reg)

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

	methods := buildAuthMethods(auth, reg)

	// InMemoryAuthStorage registers default OAuth providers.
	oauthMethods := filterByType(methods, AuthMethodTypeAgent)
	if len(oauthMethods) == 0 {
		t.Log("no OAuth methods (may depend on registered providers)")
	}
	for _, m := range oauthMethods {
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

func filterByType(methods []ExtendedAuthMethod, t AuthMethodType) []ExtendedAuthMethod {
	var result []ExtendedAuthMethod
	for _, m := range methods {
		if m.Type == t {
			result = append(result, m)
		}
	}
	return result
}
