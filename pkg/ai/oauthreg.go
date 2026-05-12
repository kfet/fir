// Package ai's OAuth registry — typed registry of fir-side OAuth providers.
//
// fir uses pinoauth (https://github.com/kfet/pinoauth) as its OAuth toolkit
// for the wire-level work (PKCE, callback server, token endpoint), but the
// fir-domain shape — including hooks that need ai.Model awareness and
// credentials with a fir-side `Extra` map — lives here. Provider
// implementations consume pinoauth.Token internally and surface
// *ai.OAuthCredentials at the boundary so the rest of fir doesn't need to
// know what specific OAuth toolkit drives the flow.
package ai

import (
	"context"
	"sort"
	"sync"

	"github.com/kfet/pinoauth"
)

// OAuthCredentials is fir's stored OAuth credential shape. Provider
// implementations return this from Login and RefreshToken; the
// pkg/auth storage layer persists it directly (via OAuthCredsToAuthCred).
//
// Mirrors what pinoauth.Token carries (access/refresh/expires) and adds
// a fir-side `Extra` map for provider-specific data that must survive
// across refreshes — most notably the Google Cloud project ID used by
// the gemini-cli / antigravity providers and the chatgpt_account_id
// used by codex. pinoauth.Token itself has no Extra field by design
// (it's the raw RFC 6749 §5.1 token response shape); fir keeps Extra
// here so the toolkit stays stateless.
type OAuthCredentials struct {
	// Access is the bearer access token.
	Access string `json:"access"`
	// Refresh is the refresh token (empty when the provider issues none).
	Refresh string `json:"refresh"`
	// Expires is the wall-clock expiry as epoch milliseconds. Zero
	// means "never expires" (or "expiry unknown") — fir's auth
	// storage uses the same convention.
	Expires int64 `json:"expires"`
	// Extra is provider-specific data fir persists alongside the
	// standard fields. Used for things like Google Cloud project ID,
	// chatgpt_account_id, etc. The shape is provider-defined.
	Extra map[string]any `json:"extra,omitempty"`
}

// OAuthProvider is the fir-side OAuth provider interface. It is
// deliberately not an extension of pinoauth.Provider — the pinoauth
// interface uses *Token (no Extra) while fir's flows need to carry
// provider-specific extras across refreshes. Concrete implementations
// (extAuthProvider, genericAuthProvider, the Python-extension-backed
// providers) use pinoauth's primitives internally and convert at the
// boundary.
type OAuthProvider interface {
	// ID returns the stable provider identifier (e.g. "anthropic").
	ID() string
	// Name returns the human-readable provider name.
	Name() string
	// UsesCallbackServer reports whether the provider's Login flow
	// uses a loopback HTTP callback server (and thus supports
	// manual-code-input fallback / cannot run non-interactively).
	UsesCallbackServer() bool
	// Login runs the full OAuth login flow and returns fresh
	// credentials. Implementations must honour ctx for cancellation.
	Login(ctx context.Context, callbacks pinoauth.LoginCallbacks) (*OAuthCredentials, error)
	// RefreshToken exchanges expired credentials for fresh ones.
	// Implementations must honour ctx for cancellation. They MUST
	// preserve credentials.Extra (and any provider-specific fields
	// that don't change) across refreshes unless overwriting them is
	// itself the goal.
	RefreshToken(ctx context.Context, creds *OAuthCredentials) (*OAuthCredentials, error)
	// GetAPIKey returns the API key to use as the bearer token for
	// requests to the provider's models. Most providers return
	// creds.Access directly; some (e.g. antigravity) return a
	// JSON-encoded envelope of token + project ID.
	GetAPIKey(creds *OAuthCredentials) string
	// ListModels enumerates live model IDs available to the user
	// for this provider. Returning nil (no error) means "no live
	// list — use the static registry".
	ListModels(ctx context.Context, creds *OAuthCredentials) ([]string, error)
	// ModifyModels optionally adjusts models for this provider (e.g.,
	// inject OAuth-mode HTTP headers, update baseUrl). Returning nil
	// means "no changes".
	ModifyModels(models []*Model, creds *OAuthCredentials) []*Model
	// ModelDefaults supplies metadata for a model ID returned by
	// ListModels but not present in the built-in registry. Returning
	// nil defers to the generic sibling-clone fallback.
	ModelDefaults(modelID string, siblings []*Model) *Model
}

var oauthRegistry sync.Map // string → OAuthProvider

// RegisterOAuthProvider registers an OAuth provider under p.ID().
func RegisterOAuthProvider(p OAuthProvider) {
	oauthRegistry.Store(p.ID(), p)
}

// UnregisterOAuthProvider removes the provider with the given ID, if any.
func UnregisterOAuthProvider(id string) {
	oauthRegistry.Delete(id)
}

// ResetOAuthProviders clears the registry.
func ResetOAuthProviders() {
	oauthRegistry.Range(func(k, _ any) bool {
		oauthRegistry.Delete(k)
		return true
	})
}

// GetOAuthProvider returns the OAuth provider with the given ID, or nil.
func GetOAuthProvider(id string) OAuthProvider {
	v, ok := oauthRegistry.Load(id)
	if !ok {
		return nil
	}
	return v.(OAuthProvider)
}

// GetOAuthProviders returns all registered OAuth providers, sorted by ID.
func GetOAuthProviders() []OAuthProvider {
	var result []OAuthProvider
	oauthRegistry.Range(func(_, v any) bool {
		result = append(result, v.(OAuthProvider))
		return true
	})
	sort.Slice(result, func(i, j int) bool {
		return result[i].ID() < result[j].ID()
	})
	return result
}
