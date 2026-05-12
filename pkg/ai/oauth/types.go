// Ported from: packages/ai/src/utils/oauth/types.ts
// Upstream hash: 1caadb2e
package oauth

import (
	"context"
	"net/http"
	"time"

	"github.com/kfet/fir/pkg/ai"
)

// oauthHTTPClient is used for all OAuth HTTP requests.
// It has a 30-second timeout so login flows don't hang indefinitely.
// Tests may swap it for a client pointed at an httptest.Server.
var oauthHTTPClient = &http.Client{Timeout: 30 * time.Second}

// Credentials holds OAuth tokens for a provider.
type Credentials struct {
	Refresh string         `json:"refresh"`
	Access  string         `json:"access"`
	Expires int64          `json:"expires"` // Unix timestamp in milliseconds
	Extra   map[string]any `json:"extra,omitempty"`
}

// Prompt describes a text prompt shown to the user during login.
type Prompt struct {
	Message     string
	Placeholder string
	AllowEmpty  bool
}

// AuthInfo describes a URL the user should visit for authorization.
//
// URL is the full authorize URL (always present). ShortURL is an optional
// pre-shortened form produced by the extension (e.g. via a public URL
// shortener that forwards click-time query params). Modes choose how to
// present them: typically the short URL is shown prominently and the full
// URL is shown as a fallback. Empty ShortURL means no short form exists.
type AuthInfo struct {
	URL          string
	ShortURL     string
	Instructions string
}

// LoginCallbacks are UI callbacks used during the OAuth login flow.
type LoginCallbacks struct {
	// OnAuth is called when the user should visit a URL to authorize.
	OnAuth func(info AuthInfo)
	// OnPrompt asks the user for text input (e.g., a code).
	OnPrompt func(prompt Prompt) (string, error)
	// OnProgress reports a status message during the flow.
	OnProgress func(message string)
	// OnManualCodeInput asks the user to paste an auth code manually.
	OnManualCodeInput func() (string, error)
	// OnDismissManualInput is called when the browser callback succeeds
	// and any visible manual-input prompt should be hidden/cleared.
	OnDismissManualInput func()
	// Ctx is used for cancellation.
	Ctx context.Context
}

// Provider is the interface that each OAuth provider implements.
type Provider interface {
	// ID returns the unique provider identifier (e.g., "anthropic", "github-copilot").
	ID() string
	// Name returns a human-readable provider name.
	Name() string
	// Login runs the full OAuth login flow and returns credentials to persist.
	Login(callbacks LoginCallbacks) (*Credentials, error)
	// UsesCallbackServer returns true if login uses a local HTTP callback server
	// and supports manual code input as a fallback.
	UsesCallbackServer() bool
	// RefreshToken exchanges expired credentials for fresh ones.
	RefreshToken(creds *Credentials) (*Credentials, error)
	// GetAPIKey extracts the API key string from credentials.
	GetAPIKey(creds *Credentials) string
	// ListModels returns the model IDs available for the given credentials.
	// Returns nil, nil if live listing is not supported for this provider
	// (the caller falls back to permissive mode).
	ListModels(ctx context.Context, creds *Credentials) ([]string, error)
	// ModifyModels optionally adjusts models for this provider (e.g., update baseUrl).
	// Returns nil if no modification is needed.
	ModifyModels(models []*ai.Model, creds *Credentials) []*ai.Model
}

// ModelDefaulter is an optional interface that Provider implementations may
// also satisfy. It lets the provider supply metadata for a model ID returned
// by ListModels but not present in the built-in registry.
//
// Returning nil means "I have nothing special for this ID" — the caller then
// falls back to a generic sibling-clone heuristic. Implementations should be
// cheap (no network) since this is called during live-list synthesis.
type ModelDefaulter interface {
	ModelDefaults(modelID string, siblings []*ai.Model) *ai.Model
}

// ProviderInfo describes an OAuth provider for display in the UI.
type ProviderInfo struct {
	ID        string
	Name      string
	Available bool
}
