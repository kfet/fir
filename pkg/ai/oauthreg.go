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
// the antigravity provider and the chatgpt_account_id
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

// oauthRegistry holds, per provider ID, a stack of registered instances. The
// top of the stack is the active provider. Several owners (e.g. the same
// builtin extension running once per ACP session) can register the same ID;
// each one removes only its own instance via UnregisterOAuthProviderInstance,
// so one owner going away (a session reaped, a child crashing) leaves the
// other owners' registrations intact instead of breaking auth for everyone.
var (
	oauthMu       sync.Mutex
	oauthRegistry = map[string][]OAuthProvider{}
)

// RegisterOAuthProvider registers p under p.ID() and makes it the active
// provider for that ID. Registering an instance already present moves it to
// the top.
func RegisterOAuthProvider(p OAuthProvider) {
	oauthMu.Lock()
	defer oauthMu.Unlock()
	id := p.ID()
	oauthRegistry[id] = append(removeInstance(oauthRegistry[id], p), p)
}

// UnregisterOAuthProvider removes every provider registered under id.
func UnregisterOAuthProvider(id string) {
	oauthMu.Lock()
	defer oauthMu.Unlock()
	delete(oauthRegistry, id)
}

// UnregisterOAuthProviderInstance removes only the given instance. If another
// instance is still registered under the same ID, the most recent remaining
// one becomes active.
func UnregisterOAuthProviderInstance(p OAuthProvider) {
	oauthMu.Lock()
	defer oauthMu.Unlock()
	id := p.ID()
	rest := removeInstance(oauthRegistry[id], p)
	if len(rest) == 0 {
		delete(oauthRegistry, id)
		return
	}
	oauthRegistry[id] = rest
}

func removeInstance(stack []OAuthProvider, p OAuthProvider) []OAuthProvider {
	out := make([]OAuthProvider, 0, len(stack))
	for _, q := range stack {
		if q != p {
			out = append(out, q)
		}
	}
	return out
}

// ResetOAuthProviders clears the registry.
func ResetOAuthProviders() {
	oauthMu.Lock()
	defer oauthMu.Unlock()
	oauthRegistry = map[string][]OAuthProvider{}
}

// GetOAuthProvider returns the active OAuth provider with the given ID, or nil.
func GetOAuthProvider(id string) OAuthProvider {
	oauthMu.Lock()
	defer oauthMu.Unlock()
	stack := oauthRegistry[id]
	if len(stack) == 0 {
		return nil
	}
	return stack[len(stack)-1]
}

// GetOAuthProviders returns the active provider for every ID, sorted by ID.
func GetOAuthProviders() []OAuthProvider {
	oauthMu.Lock()
	result := make([]OAuthProvider, 0, len(oauthRegistry))
	for _, stack := range oauthRegistry {
		result = append(result, stack[len(stack)-1])
	}
	oauthMu.Unlock()
	sort.Slice(result, func(i, j int) bool {
		return result[i].ID() < result[j].ID()
	})
	return result
}
