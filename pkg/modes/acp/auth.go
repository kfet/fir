package acp

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/oauth"
	"github.com/kfet/fir/pkg/platform"
	"github.com/kfet/fir/pkg/models"
	"github.com/kfet/fir/pkg/auth"
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

// buildAuthMethods constructs the list of ExtendedAuthMethod for the initialize response.
// It inspects the auth storage and model registry to determine which providers
// are available and what auth methods each supports.
func buildAuthMethods(authStorage *auth.AuthStorage, modelRegistry *models.ModelRegistry, clientCaps acpsdk.ClientCapabilities) []ExtendedAuthMethod {
	var methods []ExtendedAuthMethod

	// Check if client supports terminal-auth (like Zed does).
	clientSupportsMeta := clientCaps.Meta
	supportsTerminalAuth := false
	if metaMap, ok := clientSupportsMeta.(map[string]any); ok {
		supportsTerminalAuth = metaMap["terminal-auth"] == true
	}

	// Collect unique provider IDs from all known models.
	providers := collectProviders(modelRegistry)

	// Add env_var auth methods only when the client doesn't support terminal-auth.
	// Clients like Zed that support terminal-auth don't handle env_var methods
	// (they render them as non-functional buttons), so we omit them to avoid clutter.
	if !supportsTerminalAuth {
		for _, pid := range providers {
			if envVar := ai.ProviderEnvVar(pid); envVar != "" {
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
	}

	// Determine the fir executable path for terminal-auth.
	execPath, _ := os.Executable()

	// Add OAuth auth methods.
	oauthProviders := authStorage.GetOAuthProviders()
	for _, op := range oauthProviders {
		m := ExtendedAuthMethod{
			Id:          "oauth-" + op.ID(),
			Name:        fmt.Sprintf("Login with %s", op.Name()),
			Description: fmt.Sprintf("Login with %s via OAuth", op.Name()),
		}

		if supportsTerminalAuth {
			// Client supports terminal-auth: put command info in _meta
			// so the client can spawn an interactive terminal.
			m.Meta = map[string]any{
				"terminal-auth": map[string]any{
					"command": execPath,
					"args":    []string{"--login", op.ID()},
					"label":   fmt.Sprintf("%s Login", op.Name()),
				},
			}
		} else {
			// Agent handles the OAuth flow directly: open browser, poll/wait.
			// Device code flows (GitHub Copilot) and callback server flows
			// (OpenAI, Google) both work without terminal interaction.
			m.Type = AuthMethodTypeAgent
		}

		methods = append(methods, m)
	}

	return methods
}

// collectProviders returns a deduplicated, sorted list of provider IDs from the model registry.
func collectProviders(modelRegistry *models.ModelRegistry) []string {
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
	sort.Strings(providers)
	return providers
}

// providerDisplayNames maps provider IDs to human-readable names.
var providerDisplayNames = map[string]string{
	"openai":                 "OpenAI",
	"anthropic":              "Anthropic",
	"google":                 "Google",
	"groq":                   "Groq",
	"xai":                    "xAI",
	"cerebras":               "Cerebras",
	"openrouter":             "OpenRouter",
	"mistral":                "Mistral",
	"github-copilot":         "GitHub Copilot",
	"azure-openai-responses": "Azure OpenAI",
	"vercel-ai-gateway":      "Vercel AI Gateway",
	"zai":                    "ZAI",
	"minimax":                "MiniMax",
	"minimax-cn":             "MiniMax CN",
	"huggingface":            "Hugging Face",
	"amazon-bedrock":         "Amazon Bedrock",
	"google-vertex":          "Google Vertex",
}

// formatProviderName converts a provider ID like "openai" to "OpenAI" for display.
func formatProviderName(pid string) string {
	if name, ok := providerDisplayNames[pid]; ok {
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
// It opens the browser for the user, blocks until the OAuth flow completes,
// and returns success/failure. The auth URL is returned in Meta so ACP clients
// can display it or open a browser. For device-code flows (GitHub Copilot),
// the verification code is included in the instructions.
func (pa *firAgent) authenticateOAuth(ctx context.Context, method *ExtendedAuthMethod) (acpsdk.AuthenticateResponse, error) {
	// Extract provider ID from method ID (e.g., "oauth-anthropic" → "anthropic").
	providerID := strings.TrimPrefix(method.Id, "oauth-")

	pa.mu.Lock()
	authStorage := pa.authStorage
	pa.mu.Unlock()

	if authStorage == nil {
		return acpsdk.AuthenticateResponse{}, fmt.Errorf("auth storage not initialized")
	}

	provider := oauth.GetProvider(providerID)
	if provider == nil {
		return acpsdk.AuthenticateResponse{}, fmt.Errorf("unknown OAuth provider: %s", providerID)
	}

	// Providers that don't use a callback server require user interaction
	// (e.g. pasting a code or entering a domain). We can't prompt through ACP,
	// so these providers must use terminal-auth instead.
	if !provider.UsesCallbackServer() {
		return acpsdk.AuthenticateResponse{}, fmt.Errorf(
			"%s OAuth requires interactive input which is not supported in ACP agent mode; "+
				"use terminal-auth or set %s environment variable",
			formatProviderName(providerID), ai.ProviderEnvVar(providerID))
	}

	err := authStorage.Login(providerID, oauth.LoginCallbacks{
		Ctx: ctx,
		OnAuth: func(info oauth.AuthInfo) {
			firlog.Info("acp oauth: opening browser", "url", info.URL)
			if err := platform.OpenBrowser(info.URL); err != nil {
				firlog.Info("acp oauth: failed to open browser", "error", err)
			}
		},
		OnProgress: func(message string) {
			firlog.Info("acp oauth progress", "message", message)
		},
	})
	if err != nil {
		return acpsdk.AuthenticateResponse{}, fmt.Errorf("oauth login failed for %s: %w", providerID, err)
	}

	// Refresh all session model registries so newly authenticated models are available.
	pa.refreshAllModelRegistries()

	firlog.Info("acp oauth login completed successfully", "provider", providerID)
	return acpsdk.AuthenticateResponse{}, nil
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
	// Refresh model registries so models using this API key become available.
	pa.refreshAllModelRegistries()
	firlog.Info("acp env_var auth confirmed", "var", method.VarName)
	return acpsdk.AuthenticateResponse{}, nil
}

// refreshAllModelRegistries refreshes the model registry in every active session.
// Called after successful authentication so newly available models appear immediately.
func (pa *firAgent) refreshAllModelRegistries() {
	pa.mu.Lock()
	sessions := make(map[string]*firSession, len(pa.sessions))
	for k, v := range pa.sessions {
		sessions[k] = v
	}
	pa.mu.Unlock()

	for _, entry := range sessions {
		entry.modelRegistry.Refresh()
	}
}
