// Ported from: packages/ai/src/utils/oauth/types.ts
// Upstream hash: 1caadb2e
package oauth

import (
	"context"

	"github.com/kfet/pi-go/pkg/ai"
)

// Credentials holds OAuth tokens for a provider.
type Credentials struct {
	Refresh string         `json:"refresh"`
	Access  string         `json:"access"`
	Expires int64          `json:"expires"` // Unix timestamp in seconds
	Extra   map[string]any `json:"extra,omitempty"`
}

// Prompt describes a text prompt shown to the user during login.
type Prompt struct {
	Message     string
	Placeholder string
	AllowEmpty  bool
}

// AuthInfo describes a URL the user should visit for authorization.
type AuthInfo struct {
	URL          string
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
	// ModifyModels optionally adjusts models for this provider (e.g., update baseUrl).
	// Returns nil if no modification is needed.
	ModifyModels(models []*ai.Model, creds *Credentials) []*ai.Model
}

// ProviderInfo describes an OAuth provider for display in the UI.
type ProviderInfo struct {
	ID        string
	Name      string
	Available bool
}
