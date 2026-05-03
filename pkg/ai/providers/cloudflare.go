// Ported from: packages/ai/src/providers/cloudflare.ts
// Upstream hash: 036bde0a
package providers

import (
	"os"
	"strings"

	"github.com/kfet/fir/pkg/ai"
)

// IsCloudflareProvider reports whether the given provider id is one of the
// Cloudflare-hosted providers (Workers AI direct or AI Gateway).
func IsCloudflareProvider(provider ai.Provider) bool {
	return provider == ai.ProviderCloudflareWorkersAI || provider == ai.ProviderCloudflareAIGateway
}

// ResolveCloudflareBaseURL substitutes `{CLOUDFLARE_ACCOUNT_ID}` and
// `{CLOUDFLARE_GATEWAY_ID}` placeholders in the model's BaseURL with the
// values from the environment. Returns the original string unchanged when
// no placeholders are present.
//
// Required env vars:
//   - CLOUDFLARE_ACCOUNT_ID for both Workers AI and AI Gateway
//   - CLOUDFLARE_GATEWAY_ID for AI Gateway (optional default "default")
func ResolveCloudflareBaseURL(model *ai.Model) string {
	url := model.BaseURL
	if !strings.Contains(url, "{CLOUDFLARE_") {
		return url
	}
	if accountID := os.Getenv("CLOUDFLARE_ACCOUNT_ID"); accountID != "" {
		url = strings.ReplaceAll(url, "{CLOUDFLARE_ACCOUNT_ID}", accountID)
	}
	gatewayID := os.Getenv("CLOUDFLARE_GATEWAY_ID")
	if gatewayID == "" {
		gatewayID = "default"
	}
	url = strings.ReplaceAll(url, "{CLOUDFLARE_GATEWAY_ID}", gatewayID)
	return url
}

// applyCloudflareAuthHeaders rewrites the auth headers on a request map for
// the Cloudflare AI Gateway: replaces `Authorization: Bearer <key>` with
// `cf-aig-authorization: Bearer <key>` (the gateway authenticates against
// the gateway, then forwards to the upstream provider with that provider's
// own auth, which is configured in the gateway). No-op for other providers.
func applyCloudflareAuthHeaders(provider ai.Provider, headers map[string]string, apiKey string) {
	if provider != ai.ProviderCloudflareAIGateway {
		return
	}
	delete(headers, "Authorization")
	headers["cf-aig-authorization"] = "Bearer " + apiKey
}
