// Ported from: packages/ai/src/env-api-keys.ts
// Upstream hash: f04d9bc4
package envkeys

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/kfet/fir/pkg/ai"
)

// providerEnvMap maps provider names to their API key environment variables.
var providerEnvMap = map[string]string{
	string(ai.ProviderOpenAI):               "OPENAI_API_KEY",
	string(ai.ProviderAzureOpenAIResponses): "AZURE_OPENAI_API_KEY",
	string(ai.ProviderGoogle):               "GEMINI_API_KEY",
	string(ai.ProviderGroq):                 "GROQ_API_KEY",
	string(ai.ProviderCerebras):             "CEREBRAS_API_KEY",
	string(ai.ProviderXAI):                  "XAI_API_KEY",
	string(ai.ProviderOpenRouter):           "OPENROUTER_API_KEY",
	string(ai.ProviderVercelAIGateway):      "AI_GATEWAY_API_KEY",
	string(ai.ProviderZAI):                  "ZAI_API_KEY",
	string(ai.ProviderMistral):              "MISTRAL_API_KEY",
	string(ai.ProviderMinimax):              "MINIMAX_API_KEY",
	string(ai.ProviderMinimaxCN):            "MINIMAX_CN_API_KEY",
	string(ai.ProviderHuggingface):          "HF_TOKEN",
	string(ai.ProviderOpenCode):             "OPENCODE_API_KEY",
	string(ai.ProviderOpenCodeGo):           "OPENCODE_API_KEY",
	string(ai.ProviderKimiCoding):           "KIMI_API_KEY",
	string(ai.ProviderPoe):                  "POE_API_KEY",
}

// additionalAuthEnvVars lists env vars used by providers whose auth logic can't
// be expressed as a single key→envvar mapping (multi-var checks, special cases).
var additionalAuthEnvVars = []string{
	// anthropic
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
	"GOOGLE_CLOUD_API_KEY",
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

// ProviderEnvVar returns the primary API key environment variable name for a provider.
// Returns "" if the provider has no simple env var mapping (e.g., bedrock, vertex).
func ProviderEnvVar(provider string) string {
	// Handle special cases with multiple env vars — return the primary one.
	switch provider {
	case string(ai.ProviderAnthropic):
		return "ANTHROPIC_API_KEY"
	case string(ai.ProviderGitHubCopilot):
		return "COPILOT_GITHUB_TOKEN"
	}
	v, _ := providerEnvMap[provider]
	return v
}

// GetEnvApiKey returns the API key for a provider from known environment variables.
// Returns "" if no key is found.
func GetEnvApiKey(provider string) string {
	switch provider {
	case string(ai.ProviderGitHubCopilot):
		if v := os.Getenv("COPILOT_GITHUB_TOKEN"); v != "" {
			return v
		}
		if v := os.Getenv("GH_TOKEN"); v != "" {
			return v
		}
		return os.Getenv("GITHUB_TOKEN")

	case string(ai.ProviderAnthropic):
		return os.Getenv("ANTHROPIC_API_KEY")

	case string(ai.ProviderGoogleVertex):
		// Explicit API key takes precedence over ADC
		if v := os.Getenv("GOOGLE_CLOUD_API_KEY"); v != "" {
			return v
		}
		if hasVertexADCCredentials() &&
			(os.Getenv("GOOGLE_CLOUD_PROJECT") != "" || os.Getenv("GCLOUD_PROJECT") != "") &&
			os.Getenv("GOOGLE_CLOUD_LOCATION") != "" {
			return "<authenticated>"
		}
		return ""

	case string(ai.ProviderAmazonBedrock):
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
