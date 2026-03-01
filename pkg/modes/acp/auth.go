package acp

import (
	"context"
	"fmt"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/oauth"
	"github.com/kfet/fir/pkg/core"
	firlog "github.com/kfet/fir/pkg/log"
)

// providerKeyLinks maps provider IDs to URLs where users can obtain API keys.
var providerKeyLinks = map[string]string{
	"openai":    "https://platform.openai.com/api-keys",
	"anthropic": "https://console.anthropic.com/settings/keys",
	"google":    "https://aistudio.google.com/apikey",
	"groq":      "https://console.groq.com/keys",
	"xai":       "https://console.x.ai/",
	"mistral":   "https://console.mistral.ai/api-keys",
	"cerebras":  "https://cloud.cerebras.ai/",
}

// providerEnvVarInfo maps provider IDs to their primary env var name.
// Sourced from ai.providerEnvMap + special cases.
var providerEnvVarInfo = map[string]string{
	"openai":                  "OPENAI_API_KEY",
	"azure-openai-responses":  "AZURE_OPENAI_API_KEY",
	"google":                  "GEMINI_API_KEY",
	"groq":                    "GROQ_API_KEY",
	"cerebras":                "CEREBRAS_API_KEY",
	"xai":                     "XAI_API_KEY",
	"openrouter":              "OPENROUTER_API_KEY",
	"vercel-ai-gateway":       "AI_GATEWAY_API_KEY",
	"zai":                     "ZAI_API_KEY",
	"mistral":                 "MISTRAL_API_KEY",
	"minimax":                 "MINIMAX_API_KEY",
	"minimax-cn":              "MINIMAX_CN_API_KEY",
	"huggingface":             "HF_TOKEN",
	"anthropic":               "ANTHROPIC_API_KEY",
	"github-copilot":          "COPILOT_GITHUB_TOKEN",
}

// buildAuthMethods constructs the list of ExtendedAuthMethod for the initialize response.
// It inspects the auth storage and model registry to determine which providers
// are available and what auth methods each supports.
func buildAuthMethods(authStorage *core.AuthStorage, modelRegistry *core.ModelRegistry) []ExtendedAuthMethod {
	var methods []ExtendedAuthMethod

	// Collect unique provider IDs from all known models.
	providers := collectProviders(modelRegistry)

	// For each provider, add env_var auth methods.
	for _, pid := range providers {
		if envVar, ok := providerEnvVarInfo[pid]; ok {
			name := formatProviderName(pid) + " API Key"
			m := ExtendedAuthMethod{
				Id:          "env-" + pid,
				Name:        name,
				Description: fmt.Sprintf("Set %s environment variable", envVar),
				Type:        AuthMethodTypeEnvVar,
				VarName:     envVar,
			}
			if link, ok := providerKeyLinks[pid]; ok {
				m.Link = link
			}
			methods = append(methods, m)
		}
	}

	// Add OAuth auth methods for each registered OAuth provider.
	oauthProviders := authStorage.GetOAuthProviders()
	for _, op := range oauthProviders {
		methods = append(methods, ExtendedAuthMethod{
			Id:          "oauth-" + op.ID(),
			Name:        op.Name(),
			Description: fmt.Sprintf("Login with %s via OAuth", op.Name()),
			Type:        AuthMethodTypeAgent,
		})
	}

	return methods
}

// collectProviders returns a deduplicated sorted list of provider IDs from the model registry.
func collectProviders(modelRegistry *core.ModelRegistry) []string {
	allModels := modelRegistry.GetAll()
	seen := make(map[string]bool, len(allModels))
	var providers []string
	for _, m := range allModels {
		pid := string(m.Provider)
		if !seen[pid] {
			seen[pid] = true
			providers = append(providers, pid)
		}
	}
	return providers
}

// formatProviderName converts a provider ID like "openai" to "OpenAI" for display.
func formatProviderName(pid string) string {
	replacer := map[string]string{
		"openai":                  "OpenAI",
		"anthropic":               "Anthropic",
		"google":                  "Google",
		"groq":                    "Groq",
		"xai":                     "xAI",
		"cerebras":                "Cerebras",
		"openrouter":              "OpenRouter",
		"mistral":                 "Mistral",
		"github-copilot":          "GitHub Copilot",
		"azure-openai-responses":  "Azure OpenAI",
		"vercel-ai-gateway":       "Vercel AI Gateway",
		"zai":                     "ZAI",
		"minimax":                 "MiniMax",
		"minimax-cn":              "MiniMax CN",
		"huggingface":             "Hugging Face",
		"amazon-bedrock":          "Amazon Bedrock",
		"google-vertex":           "Google Vertex",
	}
	if name, ok := replacer[pid]; ok {
		return name
	}
	// Fallback: capitalize first letter of each segment.
	parts := strings.Split(pid, "-")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

// toSDKAuthMethods converts ExtendedAuthMethod slices to acpsdk.AuthMethod slices
// for the SDK InitializeResponse. The extended fields are placed in Meta.
func toSDKAuthMethods(methods []ExtendedAuthMethod) []acpsdk.AuthMethod {
	if len(methods) == 0 {
		return nil
	}
	result := make([]acpsdk.AuthMethod, len(methods))
	for i, m := range methods {
		desc := m.Description
		result[i] = acpsdk.AuthMethod{
			Id:          acpsdk.AuthMethodId(m.Id),
			Name:        m.Name,
			Description: &desc,
			Meta:        buildAuthMeta(m),
		}
	}
	return result
}

// buildAuthMeta constructs the Meta map for extended auth method fields.
// Returns nil if the method is a plain "agent" type with no extra fields.
func buildAuthMeta(m ExtendedAuthMethod) map[string]any {
	meta := make(map[string]any)
	if m.Type != "" {
		meta["type"] = string(m.Type)
	}
	if m.VarName != "" {
		meta["varName"] = m.VarName
	}
	if m.Link != "" {
		meta["link"] = m.Link
	}
	if len(m.Args) > 0 {
		meta["args"] = m.Args
	}
	if len(m.Env) > 0 {
		meta["env"] = m.Env
	}
	if len(meta) == 0 {
		return nil
	}
	return meta
}

// handleAuthenticate processes an authenticate request, dispatching based on the
// method type. It finds the matching auth method and performs the appropriate action.
func (pa *firAgent) handleAuthenticate(ctx context.Context, req acpsdk.AuthenticateRequest) (acpsdk.AuthenticateResponse, error) {
	methodID := string(req.MethodId)
	firlog.Info("acp authenticate", "methodId", methodID)

	pa.mu.Lock()
	authMethods := pa.authMethods
	pa.mu.Unlock()

	// Find the matching method.
	var method *ExtendedAuthMethod
	for i := range authMethods {
		if authMethods[i].Id == methodID {
			method = &authMethods[i]
			break
		}
	}
	if method == nil {
		return acpsdk.AuthenticateResponse{}, fmt.Errorf("unknown auth method: %s", methodID)
	}

	switch method.Type {
	case AuthMethodTypeAgent:
		return pa.authenticateOAuth(ctx, method)
	case AuthMethodTypeEnvVar:
		return pa.authenticateEnvVar(ctx, method)
	case AuthMethodTypeTerminal:
		// Terminal auth is handled client-side; nothing to do here.
		return acpsdk.AuthenticateResponse{}, nil
	default:
		return pa.authenticateOAuth(ctx, method)
	}
}

// authenticateOAuth triggers the OAuth login flow for the given method.
func (pa *firAgent) authenticateOAuth(_ context.Context, method *ExtendedAuthMethod) (acpsdk.AuthenticateResponse, error) {
	// Extract provider ID from method ID (e.g., "oauth-anthropic" → "anthropic").
	providerID := strings.TrimPrefix(method.Id, "oauth-")

	pa.mu.Lock()
	sessions := pa.sessions
	pa.mu.Unlock()

	// Find any session's auth storage to perform login.
	for _, entry := range sessions {
		authStorage := entry.modelRegistry.AuthStorage()
		err := authStorage.Login(providerID, oauth.LoginCallbacks{
			OnAuth: func(info oauth.AuthInfo) {
				firlog.Info("acp oauth auth url", "url", info.URL)
			},
		})
		if err != nil {
			return acpsdk.AuthenticateResponse{}, fmt.Errorf("oauth login failed for %s: %w", providerID, err)
		}
		return acpsdk.AuthenticateResponse{}, nil
	}

	return acpsdk.AuthenticateResponse{}, fmt.Errorf("no active session for oauth login")
}

// authenticateEnvVar checks that the expected env var is set.
// The actual env var value is set by the client (possibly by restarting the process).
func (pa *firAgent) authenticateEnvVar(_ context.Context, method *ExtendedAuthMethod) (acpsdk.AuthenticateResponse, error) {
	if method.VarName == "" {
		return acpsdk.AuthenticateResponse{}, fmt.Errorf("env_var auth method missing varName")
	}
	val := ai.GetEnvApiKey(strings.TrimPrefix(method.Id, "env-"))
	if val == "" {
		return acpsdk.AuthenticateResponse{}, fmt.Errorf("environment variable %s is not set", method.VarName)
	}
	firlog.Info("acp env_var auth confirmed", "var", method.VarName)
	return acpsdk.AuthenticateResponse{}, nil
}
