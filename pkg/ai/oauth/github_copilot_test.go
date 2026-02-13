package oauth

import (
	"testing"

	"github.com/kfet/pi-go/pkg/ai"
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

// Verify GitHubCopilotProvider implements the Provider interface.
var _ Provider = (*GitHubCopilotProvider)(nil)
