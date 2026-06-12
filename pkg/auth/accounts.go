package auth

import (
	"context"
	"sort"
	"strings"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/pinoauth"
)

// Typed provider accounts.
//
// A provider can hold N named accounts at once. Each account is stored in the
// AuthStorageData map under a composite slot key:
//
//	"<provider>"            -> the DEFAULT account (back-compat: an existing
//	                           bare key written by older fir is the default).
//	"<provider>#<account>"  -> an additional named account.
//
// This keeps the on-disk schema unchanged (still map[string]AuthCredential):
// older fir builds reading a newer auth.json still find the default account
// under the bare key and simply ignore the "#account" keys they don't know.
//
// Switching accounts = selecting a model under the account's synthesised
// sub-provider in the model selector. Core does the fan-out; provider
// extensions stay declarative (one base provider).

const slotSep = "#"

// SlotKey composes a provider id and account id into a storage slot key.
// An empty or "default" account id maps to the bare provider key.
func SlotKey(provider, accountID string) string {
	if accountID == "" || accountID == "default" {
		return provider
	}
	return provider + slotSep + accountID
}

// SplitSlot decomposes a slot key into its base provider id and account id.
// A bare key (no separator) yields an empty account id, denoting the default
// account.
func SplitSlot(key string) (provider, accountID string) {
	if i := strings.Index(key, slotSep); i >= 0 {
		return key[:i], key[i+1:]
	}
	return key, ""
}

// IsSlotKey reports whether the key carries a non-default account id.
func IsSlotKey(key string) bool {
	return strings.Contains(key, slotSep)
}

// Account describes a single stored provider account.
type Account struct {
	Provider  string         // base provider id, e.g. "anthropic"
	AccountID string         // "" for the default account
	SlotKey   string         // "anthropic" or "anthropic#<id>"
	Label     string         // human label (email/profile), may be empty
	Type      CredentialType // credential type of the stored account
	Extra     map[string]any // provider-specific config (e.g. Bedrock region/ARNs)
}

// DisplayName returns a human label for the account, falling back to the
// account id, then "default".
func (a Account) DisplayName() string {
	if a.Label != "" {
		return a.Label
	}
	if a.AccountID != "" {
		return a.AccountID
	}
	return "default"
}

// AccountsForProvider returns all stored accounts for a base provider id,
// sorted with the default account first and the rest by account id.
func (s *AuthStorage) AccountsForProvider(provider string) []Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Account
	for key, cred := range s.data {
		base, acctID := SplitSlot(key)
		if base != provider {
			continue
		}
		out = append(out, Account{
			Provider:  base,
			AccountID: acctID,
			SlotKey:   key,
			Label:     cred.Label,
			Type:      cred.Type,
			Extra:     cred.Extra,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].AccountID == "") != (out[j].AccountID == "") {
			return out[i].AccountID == "" // default first
		}
		return out[i].AccountID < out[j].AccountID
	})
	return out
}

// AllAccounts returns every stored account across all providers, sorted by
// provider then account id (default first within a provider).
func (s *AuthStorage) AllAccounts() []Account {
	s.mu.RLock()
	keys := make([]string, 0, len(s.data))
	for k := range s.data {
		keys = append(keys, k)
	}
	creds := make(map[string]AuthCredential, len(s.data))
	for k, v := range s.data {
		creds[k] = v
	}
	s.mu.RUnlock()

	out := make([]Account, 0, len(keys))
	for _, key := range keys {
		base, acctID := SplitSlot(key)
		out = append(out, Account{
			Provider:  base,
			AccountID: acctID,
			SlotKey:   key,
			Label:     creds[key].Label,
			Type:      creds[key].Type,
			Extra:     creds[key].Extra,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		if (out[i].AccountID == "") != (out[j].AccountID == "") {
			return out[i].AccountID == ""
		}
		return out[i].AccountID < out[j].AccountID
	})
	return out
}

// accountProvider wraps a base ai.OAuthProvider so it presents as a distinct
// per-account sub-provider. It overrides ID() to the composite slot key and
// Name() to carry the account label, but delegates every behavioural hook to
// the base provider. ModifyModels is bridged so the base provider's
// model-matching (which keys on the base provider id) still injects headers
// onto this account's cloned models (whose Provider is the slot key).
type accountProvider struct {
	base    ai.OAuthProvider
	slotKey string
	label   string
}

func (a *accountProvider) ID() string { return a.slotKey }

func (a *accountProvider) Name() string {
	if a.label != "" {
		return a.base.Name() + " (" + a.label + ")"
	}
	return a.base.Name()
}

func (a *accountProvider) UsesCallbackServer() bool { return a.base.UsesCallbackServer() }

func (a *accountProvider) Login(ctx context.Context, callbacks pinoauth.LoginCallbacks) (*ai.OAuthCredentials, error) {
	return a.base.Login(ctx, callbacks)
}

func (a *accountProvider) RefreshToken(ctx context.Context, creds *ai.OAuthCredentials) (*ai.OAuthCredentials, error) {
	return a.base.RefreshToken(ctx, creds)
}

func (a *accountProvider) GetAPIKey(creds *ai.OAuthCredentials) string {
	return a.base.GetAPIKey(creds)
}

func (a *accountProvider) ListModels(ctx context.Context, creds *ai.OAuthCredentials) ([]string, error) {
	return a.base.ListModels(ctx, creds)
}

// ModifyModels relabels this account's cloned models (Provider == slotKey) to
// the base provider id, runs the base provider's model modification (which
// keys on the base id), then relabels the results back to the slot key. Models
// not belonging to this account are passed through untouched.
func (a *accountProvider) ModifyModels(models []*ai.Model, creds *ai.OAuthCredentials) []*ai.Model {
	baseID := a.base.ID()
	if a.slotKey == baseID {
		// Default account: slot key is the base id; no relabelling needed.
		return a.base.ModifyModels(models, creds)
	}

	var subset []*ai.Model
	var idx []int
	for i, m := range models {
		if m.Provider == a.slotKey {
			cp := *m
			cp.Provider = baseID
			subset = append(subset, &cp)
			idx = append(idx, i)
		}
	}
	if len(subset) == 0 {
		return nil
	}
	modified := a.base.ModifyModels(subset, creds)
	if modified == nil {
		// Base made no changes; nothing to merge back.
		return nil
	}

	// Merge results back by matching model ID rather than trusting position,
	// so a base provider that reorders/filters models can never cause a
	// cross-account header (credential) mixup.
	byID := make(map[string]*ai.Model, len(modified))
	for _, m := range modified {
		if m != nil {
			byID[m.ID] = m
		}
	}
	result := make([]*ai.Model, len(models))
	copy(result, models)
	for _, mi := range idx {
		m := byID[models[mi].ID]
		if m == nil {
			continue
		}
		cp := *m
		cp.Provider = a.slotKey
		result[mi] = &cp
	}
	return result
}

func (a *accountProvider) ModelDefaults(modelID string, siblings []*ai.Model) *ai.Model {
	return a.base.ModelDefaults(modelID, siblings)
}

// oauthProviderForSlot resolves the OAuth provider that backs a slot key,
// stripping any "#account" suffix to find the registered base provider.
func oauthProviderForSlot(slotKey string) ai.OAuthProvider {
	base, _ := SplitSlot(slotKey)
	return ai.GetOAuthProvider(base)
}

// bedrockIAMFromExtra reconstructs an ai.BedrockIAMCreds from a stored
// aws_iam credential's Extra map. Missing keys yield zero values; an unset
// mode defaults to "profile".
func bedrockIAMFromExtra(extra map[string]any) ai.BedrockIAMCreds {
	get := func(k string) string {
		if extra == nil {
			return ""
		}
		if v, ok := extra[k].(string); ok {
			return v
		}
		return ""
	}
	c := ai.BedrockIAMCreds{
		Mode:         get("mode"),
		Profile:      get("profile"),
		AccessKey:    get("accessKey"),
		SecretKey:    get("secretKey"),
		SessionToken: get("sessionToken"),
		Region:       get("region"),
	}
	if c.Mode == "" {
		if c.AccessKey != "" {
			c.Mode = "keys"
		} else {
			c.Mode = "profile"
		}
	}
	return c
}

// AccountRegion returns the AWS region configured for an account (from Extra),
// or "" if none. Applies to Bedrock accounts (aws_iam and bearer).
func AccountRegion(extra map[string]any) string {
	if extra == nil {
		return ""
	}
	if v, ok := extra["region"].(string); ok {
		return v
	}
	return ""
}

// AccountModelOverride returns a per-account model-id/ARN override for a base
// model id (from Extra["modelOverrides"]), or "" if none. This lets a Bedrock
// account map a logical model onto an account-specific inference-profile ARN.
func AccountModelOverride(extra map[string]any, baseModelID string) string {
	if extra == nil {
		return ""
	}
	m, ok := extra["modelOverrides"].(map[string]any)
	if !ok {
		return ""
	}
	if v, ok := m[baseModelID].(string); ok {
		return v
	}
	return ""
}
