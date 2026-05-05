// Provider-keyed registry of `RegisteredProvider` records.
//
// This is the data-driven companion to the existing `Api`-keyed `Registry`
// (which carries wire-protocol stream functions).  Where `Api` is the wire
// family ("openai-completions", "anthropic-messages"), `Provider` is the
// hosted service ("openai", "fireworks", "groq").  Many providers share an
// Api.
//
// Cross-cutting code that previously switched on provider IDs (env-var name,
// default model, display name, key link, OAuth wiring) reads from this
// registry instead, so adding a provider — including via an extension —
// requires no edits to consumers.
package ai

import (
	"sort"
	"strings"
	"sync"
)

// EnvKeySpec describes how an API key is sourced from environment variables.
type EnvKeySpec struct {
	// Primary is the canonical env var name (e.g. "OPENAI_API_KEY"). Empty
	// string for providers that don't use a single env-var (Authenticated).
	Primary string
	// Fallbacks are alternative env vars consulted in order when Primary is
	// unset (e.g. github-copilot's GH_TOKEN, GITHUB_TOKEN).
	Fallbacks []string
	// Authenticated marks providers whose auth state isn't a single env var
	// — bedrock (AWS profile/keys/...), google-vertex (ADC), etc.  When true
	// the registry-driven envkeys path returns "<authenticated>" sentinel
	// only if the provider's bespoke detection logic (still in envkeys.go)
	// reports a credential present.
	Authenticated bool
}

// RegisteredProvider carries the cross-cutting metadata for a hosted provider.
// Built-ins populate this at package init; ext-shipped providers populate it
// after handshake.  Func fields (Stream, Lister, ClaimsModelID,
// ResolveCustomID) are added in Phase A2+ and ext wiring (Phase B) — for now
// the record carries pure data only.
type RegisteredProvider struct {
	ID              Provider
	DisplayName     string
	ShortName       string
	Priority        int
	DefaultModelID  string
	KeyLink         string
	EnvKeys         EnvKeySpec
	OAuthProviderID string
	// ClaimsModelIDGlobs lists ID-shape glob patterns the provider claims for
	// pass-through resolution.  When a CLI model id matches one of these
	// patterns and the provider is named, the resolver returns the
	// fallback-cloned model silently rather than warning the user.  Example:
	// amazon-bedrock claims "arn:aws:bedrock:*" inference-profile ARNs.
	ClaimsModelIDGlobs []string
	// RefuseFuzzyMatch disables sibling-clone fallback for unknown model IDs
	// under this provider.  Used when an unknown id almost always means a
	// typo (e.g. Poe's bot catalogue is exhaustive in models_generated.go).
	RefuseFuzzyMatch bool
	Source           string // "builtin" or "ext:<name>"
}

var providerRegistry sync.Map // Provider → *RegisteredProvider

// RegisterProvider stores a provider record by ID.  Last write wins.
func RegisterProvider(p *RegisteredProvider) {
	if p == nil || p.ID == "" {
		return
	}
	providerRegistry.Store(p.ID, p)
}

// UnregisterProvider removes a provider record (used when an extension
// shutting down withdraws its registrations).
func UnregisterProvider(id Provider) {
	providerRegistry.Delete(id)
}

// GetProviderRecord returns the registered record for a provider, or nil.
func GetProviderRecord(id Provider) *RegisteredProvider {
	v, ok := providerRegistry.Load(id)
	if !ok {
		return nil
	}
	return v.(*RegisteredProvider)
}

// GetRegisteredProviders returns all registered provider records, sorted by
// (Priority, ID) for stable display ordering.
func GetRegisteredProviders() []*RegisteredProvider {
	var out []*RegisteredProvider
	providerRegistry.Range(func(_, v any) bool {
		out = append(out, v.(*RegisteredProvider))
		return true
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// IsBuiltInProviderID reports whether id refers to a registered built-in
// provider.  Used by extension validation to prevent extensions from
// accidentally overriding built-in IDs. "Built-in" covers both core
// (Source == "builtin") and registrations contributed by builtin-scope
// extensions (Source has prefix "builtin-ext:") — both are protected
// from override by user/project-local extensions.
func IsBuiltInProviderID(id string) bool {
	p := GetProviderRecord(Provider(id))
	if p == nil {
		return false
	}
	return p.Source == "builtin" || strings.HasPrefix(p.Source, "builtin-ext:")
}
