// Ported from: packages/ai/src/utils/oauth/types.ts
// Upstream hash: 3256d3c0
package oauth

import (
	"context"

	"github.com/kfet/pi-go/pkg/ai"
)

// Credentials holds OAuth token data persisted between sessions.
type Credentials struct {
	Refresh string `json:"refresh"`
	Access  string `json:"access"`
	Expires int64  `json:"expires"` // Unix timestamp (seconds)

	// Extra holds provider-specific fields.
	Extra map[string]any `json:"extra,omitempty"`
}

// ProviderID identifies an OAuth provider (e.g. "anthropic", "github-copilot").
type ProviderID = string

// Prompt describes a text prompt shown to the user during login.
type Prompt struct {
	Message     string
	Placeholder string
	AllowEmpty  bool
}

// AuthInfo contains the URL and optional instructions for browser-based login.
type AuthInfo struct {
	URL          string
	Instructions string
}

// LoginCallbacks are provided by the caller to interact with the user during login.
type LoginCallbacks struct {
	OnAuth             func(info AuthInfo)
	OnPrompt           func(ctx context.Context, prompt Prompt) (string, error)
	OnProgress         func(message string)
	OnManualCodeInput  func(ctx context.Context) (string, error)
}

// Provider defines the interface that each OAuth provider must implement.
type Provider interface {
	// ID returns the unique identifier for this provider.
	ID() ProviderID

	// Name returns a human-readable name.
	Name() string

	// UsesCallbackServer reports whether login uses a local HTTP callback server
	// and supports manual code input as a fallback.
	UsesCallbackServer() bool

	// Login runs the interactive login flow and returns credentials to persist.
	Login(ctx context.Context, callbacks LoginCallbacks) (Credentials, error)

	// RefreshToken refreshes expired credentials and returns updated ones.
	RefreshToken(ctx context.Context, creds Credentials) (Credentials, error)

	// GetAPIKey converts credentials to the API key string used by the provider.
	GetAPIKey(creds Credentials) string

	// ModifyModels optionally adjusts models for this provider (e.g. updating baseUrl).
	// Implementations that don't need this should return models unchanged.
	ModifyModels(models []ai.Model, creds Credentials) []ai.Model
}
