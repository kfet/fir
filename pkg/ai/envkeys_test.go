// Ported from: packages/ai/src/env-api-keys.ts
// Upstream hash: 1caadb2e
package ai

import (
	"os"
	"testing"
)

func TestGetEnvApiKey_OpenAI(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test-123")
	if got := GetEnvApiKey("openai"); got != "sk-test-123" {
		t.Errorf("expected 'sk-test-123', got %q", got)
	}
}

func TestGetEnvApiKey_Anthropic_APIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	if got := GetEnvApiKey("anthropic"); got != "sk-ant-test" {
		t.Errorf("expected 'sk-ant-test', got %q", got)
	}
}

func TestGetEnvApiKey_Anthropic_OAuthTakesPrecedence(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("ANTHROPIC_OAUTH_TOKEN", "oauth-token")
	if got := GetEnvApiKey("anthropic"); got != "oauth-token" {
		t.Errorf("expected 'oauth-token', got %q", got)
	}
}

func TestGetEnvApiKey_GitHubCopilot(t *testing.T) {
	t.Setenv("COPILOT_GITHUB_TOKEN", "ghu_test")
	if got := GetEnvApiKey("github-copilot"); got != "ghu_test" {
		t.Errorf("expected 'ghu_test', got %q", got)
	}
}

func TestGetEnvApiKey_GitHubCopilot_Fallback(t *testing.T) {
	os.Unsetenv("COPILOT_GITHUB_TOKEN")
	t.Setenv("GH_TOKEN", "gh-test")
	if got := GetEnvApiKey("github-copilot"); got != "gh-test" {
		t.Errorf("expected 'gh-test', got %q", got)
	}
}

func TestGetEnvApiKey_Bedrock(t *testing.T) {
	t.Setenv("AWS_PROFILE", "test-profile")
	if got := GetEnvApiKey("amazon-bedrock"); got != "<authenticated>" {
		t.Errorf("expected '<authenticated>', got %q", got)
	}
}

func TestGetEnvApiKey_Bedrock_AccessKey(t *testing.T) {
	os.Unsetenv("AWS_PROFILE")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIA...")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	if got := GetEnvApiKey("amazon-bedrock"); got != "<authenticated>" {
		t.Errorf("expected '<authenticated>', got %q", got)
	}
}

func TestGetEnvApiKey_Bedrock_None(t *testing.T) {
	os.Unsetenv("AWS_PROFILE")
	os.Unsetenv("AWS_ACCESS_KEY_ID")
	os.Unsetenv("AWS_SECRET_ACCESS_KEY")
	os.Unsetenv("AWS_BEARER_TOKEN_BEDROCK")
	os.Unsetenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI")
	os.Unsetenv("AWS_CONTAINER_CREDENTIALS_FULL_URI")
	os.Unsetenv("AWS_WEB_IDENTITY_TOKEN_FILE")
	if got := GetEnvApiKey("amazon-bedrock"); got != "" {
		t.Errorf("expected '', got %q", got)
	}
}

func TestGetEnvApiKey_UnknownProvider(t *testing.T) {
	if got := GetEnvApiKey("unknown-provider"); got != "" {
		t.Errorf("expected '', got %q", got)
	}
}

func TestGetEnvApiKey_Google(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "gem-key")
	if got := GetEnvApiKey("google"); got != "gem-key" {
		t.Errorf("expected 'gem-key', got %q", got)
	}
}

func TestGetEnvApiKey_AllStandardProviders(t *testing.T) {
	tests := []struct {
		provider string
		envVar   string
	}{
		{"groq", "GROQ_API_KEY"},
		{"cerebras", "CEREBRAS_API_KEY"},
		{"xai", "XAI_API_KEY"},
		{"openrouter", "OPENROUTER_API_KEY"},
		{"vercel-ai-gateway", "AI_GATEWAY_API_KEY"},
		{"zai", "ZAI_API_KEY"},
		{"mistral", "MISTRAL_API_KEY"},
		{"minimax", "MINIMAX_API_KEY"},
		{"minimax-cn", "MINIMAX_CN_API_KEY"},
		{"huggingface", "HF_TOKEN"},
		{"opencode", "OPENCODE_API_KEY"},
		{"kimi-coding", "KIMI_API_KEY"},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			want := "test-key-" + tt.provider
			t.Setenv(tt.envVar, want)
			if got := GetEnvApiKey(tt.provider); got != want {
				t.Errorf("expected %q, got %q", want, got)
			}
		})
	}
}

func TestGetEnvApiKey_GoogleVertex_NoCredentials(t *testing.T) {
	os.Unsetenv("GOOGLE_APPLICATION_CREDENTIALS")
	os.Unsetenv("GOOGLE_CLOUD_PROJECT")
	os.Unsetenv("GCLOUD_PROJECT")
	os.Unsetenv("GOOGLE_CLOUD_LOCATION")
	if got := GetEnvApiKey("google-vertex"); got != "" {
		t.Errorf("expected '', got %q", got)
	}
}
