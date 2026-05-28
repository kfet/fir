// Built-in provider metadata table.
//
// Single source of truth for cross-cutting per-provider data: env-var name,
// display name, default model, key link, short name, priority order, and
// OAuth provider ID.  Consumers (envkeys, modelresolver, acp/auth, etc.) read
// this via GetProviderRecord rather than maintaining their own maps.
//
// Adding a new built-in provider: append a record below.  Adding an
// ext-shipped provider: the extension manager calls RegisterProvider after
// handshake (Phase B).
package ai

func init() {
	for _, p := range builtInProviders() {
		p.Source = "builtin"
		RegisterProvider(p)
	}
}

// builtInProviders returns the canonical metadata records for all built-in
// providers.  Priority values come from the previous knownProviderOrder list
// in pkg/models/modelresolver.go (lower = preferred); display names and key
// links come from pkg/modes/acp/auth.go; env var names from
// pkg/ai/envkeys/envkeys.go; default model IDs from
// pkg/models/modelresolver.go DefaultModelPerProvider.
func builtInProviders() []*RegisteredProvider {
	return []*RegisteredProvider{
		{
			ID: ProviderAnthropic, DisplayName: "Anthropic", ShortName: "anth", Priority: 0,
			DefaultModelID: "claude-opus-4-8",
			KeyLink:        "https://console.anthropic.com/settings/keys",
			EnvKeys: EnvKeySpec{
				Primary:   "ANTHROPIC_API_KEY",
				Fallbacks: []string{"ANTHROPIC_OAUTH_TOKEN"},
			},
			OAuthProviderID: "anthropic",
		},
		{
			ID: ProviderOpenAI, DisplayName: "OpenAI", ShortName: "oai", Priority: 1,
			DefaultModelID: "gpt-5.4",
			KeyLink:        "https://platform.openai.com/api-keys",
			EnvKeys:        EnvKeySpec{Primary: "OPENAI_API_KEY"},
		},
		{
			ID: ProviderGoogle, DisplayName: "Google", ShortName: "goog", Priority: 2,
			DefaultModelID: "gemini-3.1-pro-preview",
			KeyLink:        "https://aistudio.google.com/apikey",
			EnvKeys:        EnvKeySpec{Primary: "GEMINI_API_KEY"},
		},
		{
			ID: ProviderAmazonBedrock, DisplayName: "Amazon Bedrock", ShortName: "bed", Priority: 3,
			DefaultModelID: "us.anthropic.claude-opus-4-6-v1",
			EnvKeys: EnvKeySpec{
				Authenticated: true,
				// Listed for hermetic-test cleanup; not used as primary key lookup.
				Fallbacks: []string{
					"AWS_PROFILE", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY",
					"AWS_BEARER_TOKEN_BEDROCK", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
					"AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_WEB_IDENTITY_TOKEN_FILE",
				},
			},
			ClaimsModelIDGlobs: []string{"arn:aws:bedrock:*"},
		},
		{
			ID: ProviderAzureOpenAIResponses, DisplayName: "Azure OpenAI", Priority: 4,
			DefaultModelID: "gpt-5.4",
			EnvKeys:        EnvKeySpec{Primary: "AZURE_OPENAI_API_KEY"},
		},
		{
			ID: ProviderOpenAICodex, DisplayName: "OpenAI Codex", Priority: 5,
			DefaultModelID:  "gpt-5.5",
			OAuthProviderID: "openai-codex",
		},
		// Other providers may be registered dynamically by extensions
		// via fir_ext.register_provider(...) and fir_ext.register_api(...).
		{
			ID: ProviderGoogleVertex, DisplayName: "Google Vertex", ShortName: "vtx", Priority: 8,
			DefaultModelID: "gemini-3.1-pro-preview",
			EnvKeys: EnvKeySpec{
				Authenticated: true,
				Fallbacks: []string{
					"GOOGLE_CLOUD_API_KEY", "GOOGLE_CLOUD_PROJECT", "GCLOUD_PROJECT",
					"GOOGLE_CLOUD_LOCATION", "GOOGLE_APPLICATION_CREDENTIALS",
				},
			},
		},
		{
			ID: ProviderGitHubCopilot, DisplayName: "GitHub Copilot", Priority: 9,
			DefaultModelID: "gpt-5.4",
			EnvKeys: EnvKeySpec{
				Primary:   "COPILOT_GITHUB_TOKEN",
				Fallbacks: []string{"GH_TOKEN", "GITHUB_TOKEN"},
			},
			OAuthProviderID: "github-copilot",
		},
		{
			ID: ProviderOpenRouter, DisplayName: "OpenRouter", ShortName: "or", Priority: 10,
			DefaultModelID: "moonshotai/kimi-k2.6",
			EnvKeys:        EnvKeySpec{Primary: "OPENROUTER_API_KEY"},
		},
		{
			ID: ProviderVercelAIGateway, DisplayName: "Vercel AI Gateway", Priority: 11,
			DefaultModelID: "zai/glm-5.1",
			EnvKeys:        EnvKeySpec{Primary: "AI_GATEWAY_API_KEY"},
		},
		{
			ID: ProviderXAI, DisplayName: "xAI", ShortName: "xai", Priority: 12,
			DefaultModelID: "grok-4.20-0309-reasoning",
			KeyLink:        "https://console.x.ai/",
			EnvKeys:        EnvKeySpec{Primary: "XAI_API_KEY"},
		},
		{
			ID: ProviderGroq, DisplayName: "Groq", ShortName: "groq", Priority: 13,
			DefaultModelID: "openai/gpt-oss-120b",
			KeyLink:        "https://console.groq.com/keys",
			EnvKeys:        EnvKeySpec{Primary: "GROQ_API_KEY"},
		},
		{
			ID: ProviderCerebras, DisplayName: "Cerebras", Priority: 14,
			DefaultModelID: "zai-glm-4.7",
			KeyLink:        "https://cloud.cerebras.ai/",
			EnvKeys:        EnvKeySpec{Primary: "CEREBRAS_API_KEY"},
		},
		{
			ID: ProviderZAI, DisplayName: "ZAI", Priority: 15,
			DefaultModelID: "glm-5.1",
			EnvKeys:        EnvKeySpec{Primary: "ZAI_API_KEY"},
		},
		{
			ID: ProviderMistral, DisplayName: "Mistral", ShortName: "mist", Priority: 16,
			DefaultModelID: "devstral-medium-latest",
			KeyLink:        "https://console.mistral.ai/api-keys",
			EnvKeys:        EnvKeySpec{Primary: "MISTRAL_API_KEY"},
		},
		{
			ID: ProviderMinimax, DisplayName: "MiniMax", Priority: 17,
			DefaultModelID: "MiniMax-M2.7",
			EnvKeys:        EnvKeySpec{Primary: "MINIMAX_API_KEY"},
		},
		{
			ID: ProviderMinimaxCN, DisplayName: "MiniMax CN", Priority: 18,
			DefaultModelID: "MiniMax-M2.7",
			EnvKeys:        EnvKeySpec{Primary: "MINIMAX_CN_API_KEY"},
		},
		{
			ID: ProviderMoonshotAI, DisplayName: "Moonshot AI", Priority: 19,
			DefaultModelID: "kimi-k2.6",
			EnvKeys:        EnvKeySpec{Primary: "MOONSHOT_API_KEY"},
		},
		{
			ID: ProviderMoonshotAICN, DisplayName: "Moonshot AI CN", Priority: 20,
			DefaultModelID: "kimi-k2.6",
			EnvKeys:        EnvKeySpec{Primary: "MOONSHOT_API_KEY"},
		},
		{
			ID: ProviderHuggingface, DisplayName: "Hugging Face", Priority: 21,
			DefaultModelID: "moonshotai/Kimi-K2.6",
			EnvKeys:        EnvKeySpec{Primary: "HF_TOKEN"},
		},
		{
			ID: ProviderOpenCode, DisplayName: "OpenCode", Priority: 22,
			DefaultModelID: "kimi-k2.6",
			EnvKeys:        EnvKeySpec{Primary: "OPENCODE_API_KEY"},
		},
		{
			ID: ProviderOpenCodeGo, DisplayName: "OpenCode Go", Priority: 23,
			DefaultModelID: "kimi-k2.6",
			EnvKeys:        EnvKeySpec{Primary: "OPENCODE_API_KEY"},
		},
		{
			ID: ProviderKimiCoding, DisplayName: "Kimi Coding", Priority: 24,
			DefaultModelID: "kimi-for-coding",
			EnvKeys:        EnvKeySpec{Primary: "KIMI_API_KEY"},
		},
		{
			ID: ProviderCloudflareWorkersAI, DisplayName: "Cloudflare Workers AI", Priority: 25,
			DefaultModelID: "@cf/moonshotai/kimi-k2.6",
			EnvKeys:        EnvKeySpec{Primary: "CLOUDFLARE_API_KEY"},
		},
		{
			ID: ProviderCloudflareAIGateway, DisplayName: "Cloudflare AI Gateway", Priority: 26,
			DefaultModelID: "workers-ai/@cf/moonshotai/kimi-k2.6",
			EnvKeys:        EnvKeySpec{Primary: "CLOUDFLARE_API_KEY"},
		},
		{
			ID: ProviderXiaomi, DisplayName: "Xiaomi", Priority: 27,
			DefaultModelID: "mimo-v2.5-pro",
			EnvKeys:        EnvKeySpec{Primary: "XIAOMI_API_KEY"},
		},
		{
			ID: ProviderDeepseek, DisplayName: "Deepseek", ShortName: "ds", Priority: 28,
			DefaultModelID: "deepseek-v4-pro",
			EnvKeys:        EnvKeySpec{Primary: "DEEPSEEK_API_KEY"},
		},
		{
			ID: ProviderFireworks, DisplayName: "Fireworks", Priority: 29,
			DefaultModelID: "accounts/fireworks/models/kimi-k2p6",
			EnvKeys:        EnvKeySpec{Primary: "FIREWORKS_API_KEY"},
		},
		{
			ID: ProviderPoe, DisplayName: "Poe", Priority: 30,
			EnvKeys:          EnvKeySpec{Primary: "POE_API_KEY"},
			OAuthProviderID:  "poe",
			RefuseFuzzyMatch: true,
		},
	}
}
