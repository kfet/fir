// Ported from: packages/ai/src/env-api-keys.ts
// Upstream hash: 1caadb2e
package ai

import (
	"os"
	"path/filepath"
	"sort"
)

// providerEnvMap maps provider names to their API key environment variables.
var providerEnvMap = map[string]string{
	string(ProviderOpenAI):              "OPENAI_API_KEY",
	string(ProviderAzureOpenAIResponses): "AZURE_OPENAI_API_KEY",
	string(ProviderGoogle):              "GEMINI_API_KEY",
	string(ProviderGroq):                "GROQ_API_KEY",
	string(ProviderCerebras):            "CEREBRAS_API_KEY",
	string(ProviderXAI):                 "XAI_API_KEY",
	string(ProviderOpenRouter):          "OPENROUTER_API_KEY",
	string(ProviderVercelAIGateway):     "AI_GATEWAY_API_KEY",
	string(ProviderZAI):                 "ZAI_API_KEY",
	string(ProviderMistral):             "MISTRAL_API_KEY",
	string(ProviderMinimax):             "MINIMAX_API_KEY",
	string(ProviderMinimaxCN):           "MINIMAX_CN_API_KEY",
	string(ProviderHuggingface):         "HF_TOKEN",
	string(ProviderOpenCode):            "OPENCODE_API_KEY",
	string(ProviderKimiCoding):          "KIMI_API_KEY",
}

// additionalAuthEnvVars lists env vars used by providers whose auth logic can't
// be expressed as a single key→envvar mapping (multi-var checks, special cases).
var additionalAuthEnvVars = []string{
	// anthropic
	"ANTHROPIC_OAUTH_TOKEN",
	"ANTHROPIC_API_KEY",
	// github-copilot
	"COPILOT_GITHUB_TOKEN",
	"GH_TOKEN",
	"GITHUB_TOKEN",
	// amazon-bedrock
	"AWS_PROFILE",
	"AWS_ACCESS_KEY_ID",
	"AWS_SECRET_ACCESS_KEY",
	"AWS_BEARER_TOKEN_BEDROCK",
	"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
	"AWS_CONTAINER_CREDENTIALS_FULL_URI",
	"AWS_WEB_IDENTITY_TOKEN_FILE",
	// google-vertex
	"GOOGLE_CLOUD_PROJECT",
	"GCLOUD_PROJECT",
	"GOOGLE_CLOUD_LOCATION",
	"GOOGLE_APPLICATION_CREDENTIALS",
}

// KnownApiKeyEnvVars returns the sorted list of all environment variable names
// that GetEnvApiKey (or HasAuth) inspects to determine provider authentication.
// Useful for tests that need a hermetic environment.
func KnownApiKeyEnvVars() []string {
	seen := make(map[string]struct{}, len(providerEnvMap)+len(additionalAuthEnvVars))
	for _, v := range providerEnvMap {
		seen[v] = struct{}{}
	}
	for _, v := range additionalAuthEnvVars {
		seen[v] = struct{}{}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// GetEnvApiKey returns the API key for a provider from known environment variables.
// Returns "" if no key is found.
func GetEnvApiKey(provider string) string {
	switch provider {
	case string(ProviderGitHubCopilot):
		if v := os.Getenv("COPILOT_GITHUB_TOKEN"); v != "" {
			return v
		}
		if v := os.Getenv("GH_TOKEN"); v != "" {
			return v
		}
		return os.Getenv("GITHUB_TOKEN")

	case string(ProviderAnthropic):
		// ANTHROPIC_OAUTH_TOKEN takes precedence over ANTHROPIC_API_KEY
		if v := os.Getenv("ANTHROPIC_OAUTH_TOKEN"); v != "" {
			return v
		}
		return os.Getenv("ANTHROPIC_API_KEY")

	case string(ProviderGoogleVertex):
		if hasVertexADCCredentials() &&
			(os.Getenv("GOOGLE_CLOUD_PROJECT") != "" || os.Getenv("GCLOUD_PROJECT") != "") &&
			os.Getenv("GOOGLE_CLOUD_LOCATION") != "" {
			return "<authenticated>"
		}
		return ""

	case string(ProviderAmazonBedrock):
		if os.Getenv("AWS_PROFILE") != "" ||
			(os.Getenv("AWS_ACCESS_KEY_ID") != "" && os.Getenv("AWS_SECRET_ACCESS_KEY") != "") ||
			os.Getenv("AWS_BEARER_TOKEN_BEDROCK") != "" ||
			os.Getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI") != "" ||
			os.Getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI") != "" ||
			os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE") != "" {
			return "<authenticated>"
		}
		return ""
	}

	envVar, ok := providerEnvMap[provider]
	if !ok {
		return ""
	}
	return os.Getenv(envVar)
}

// hasVertexADCCredentials checks if Google Application Default Credentials exist.
func hasVertexADCCredentials() bool {
	// Check GOOGLE_APPLICATION_CREDENTIALS env var first
	gacPath := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	if gacPath != "" {
		_, err := os.Stat(gacPath)
		return err == nil
	}

	// Fall back to default ADC path
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	adcPath := filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
	_, err = os.Stat(adcPath)
	return err == nil
}
