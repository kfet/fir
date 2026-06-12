package auth

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/pinoauth"
)

func TestSlotKeyRoundTrip(t *testing.T) {
	cases := []struct {
		provider, acct, key string
	}{
		{"anthropic", "", "anthropic"},
		{"anthropic", "default", "anthropic"},
		{"anthropic", "work", "anthropic#work"},
	}
	for _, c := range cases {
		got := SlotKey(c.provider, c.acct)
		if got != c.key {
			t.Errorf("SlotKey(%q,%q)=%q want %q", c.provider, c.acct, got, c.key)
		}
		p, a := SplitSlot(got)
		if p != c.provider {
			t.Errorf("SplitSlot(%q) provider=%q want %q", got, p, c.provider)
		}
		_ = a
	}
	if p, a := SplitSlot("anthropic#work"); p != "anthropic" || a != "work" {
		t.Errorf("SplitSlot composite = %q,%q", p, a)
	}
	if IsSlotKey("anthropic") {
		t.Error("bare key should not be a slot key")
	}
	if !IsSlotKey("anthropic#work") {
		t.Error("composite key should be a slot key")
	}
}

// TestDefaultPointerBackCompat: an existing bare key (written by older fir) is
// resolved as the default account, and "#account" keys are ignored by code
// that only knows the bare key.
func TestDefaultPointerBackCompat(t *testing.T) {
	s := NewInMemoryAuthStorage(AuthStorageData{
		"anthropic":      {Type: CredentialTypeAPIKey, Key: "default-key"},
		"anthropic#work": {Type: CredentialTypeAPIKey, Key: "work-key", Label: "work@x.com"},
	})

	// Bare key still resolves the default.
	if got := s.GetApiKey("anthropic"); got != "default-key" {
		t.Errorf("default GetApiKey = %q want default-key", got)
	}
	// Named account resolves independently.
	if got := s.GetApiKey("anthropic#work"); got != "work-key" {
		t.Errorf("named GetApiKey = %q want work-key", got)
	}

	accts := s.AccountsForProvider("anthropic")
	if len(accts) != 2 {
		t.Fatalf("AccountsForProvider = %d accounts, want 2", len(accts))
	}
	// Default first.
	if accts[0].AccountID != "" || accts[0].SlotKey != "anthropic" {
		t.Errorf("first account not default: %+v", accts[0])
	}
	if accts[1].AccountID != "work" || accts[1].Label != "work@x.com" {
		t.Errorf("second account wrong: %+v", accts[1])
	}
}

// recordingProvider injects a header onto its own provider's models so we can
// observe accountProvider delegation + relabelling.
type recordingProvider struct {
	id         string
	loginCreds *ai.OAuthCredentials
	modifySeen []string // provider ids seen during ModifyModels
}

func (m *recordingProvider) ID() string               { return m.id }
func (m *recordingProvider) Name() string             { return "Rec " + m.id }
func (m *recordingProvider) UsesCallbackServer() bool { return false }
func (m *recordingProvider) Login(_ context.Context, _ pinoauth.LoginCallbacks) (*ai.OAuthCredentials, error) {
	return m.loginCreds, nil
}
func (m *recordingProvider) ListModels(_ context.Context, _ *ai.OAuthCredentials) ([]string, error) {
	return nil, nil
}
func (m *recordingProvider) RefreshToken(_ context.Context, c *ai.OAuthCredentials) (*ai.OAuthCredentials, error) {
	return c, nil
}
func (m *recordingProvider) GetAPIKey(c *ai.OAuthCredentials) string { return c.Access }
func (m *recordingProvider) ModifyModels(models []*ai.Model, _ *ai.OAuthCredentials) []*ai.Model {
	out := make([]*ai.Model, 0, len(models))
	for _, mm := range models {
		cp := *mm
		if cp.Provider == m.id {
			m.modifySeen = append(m.modifySeen, cp.ID)
			if cp.Headers == nil {
				cp.Headers = map[string]string{}
			} else {
				h := make(map[string]string, len(cp.Headers))
				for k, v := range cp.Headers {
					h[k] = v
				}
				cp.Headers = h
			}
			cp.Headers["x-injected"] = "yes"
		}
		out = append(out, &cp)
	}
	return out
}
func (m *recordingProvider) ModelDefaults(_ string, _ []*ai.Model) *ai.Model { return nil }

func TestAccountProviderDelegationAndRelabel(t *testing.T) {
	base := &recordingProvider{id: "rec-prov"}
	ap := &accountProvider{base: base, slotKey: "rec-prov#work", label: "work@x.com"}

	if ap.ID() != "rec-prov#work" {
		t.Errorf("ID = %q", ap.ID())
	}
	if ap.Name() != "Rec rec-prov (work@x.com)" {
		t.Errorf("Name = %q", ap.Name())
	}

	models := []*ai.Model{
		{ID: "m1", Provider: "rec-prov"},      // default account's model
		{ID: "m1", Provider: "rec-prov#work"}, // this account's clone
	}
	out := ap.ModifyModels(models, &ai.OAuthCredentials{Access: "tok"})
	if out == nil {
		t.Fatal("ModifyModels returned nil")
	}
	// Default model untouched.
	if out[0].Headers["x-injected"] != "" {
		t.Error("default-account model should not be modified by the named account")
	}
	// Named-account clone got the header and kept its slot provider id.
	if out[1].Provider != "rec-prov#work" {
		t.Errorf("clone provider relabelled wrong: %q", out[1].Provider)
	}
	if out[1].Headers["x-injected"] != "yes" {
		t.Error("named-account clone should have injected header")
	}
}

func TestGetOAuthProvidersFanOut(t *testing.T) {
	ai.ResetOAuthProviders()
	t.Cleanup(ai.ResetOAuthProviders)
	base := &recordingProvider{id: "fanout-prov"}
	ai.RegisterOAuthProvider(base)

	s := NewInMemoryAuthStorage(AuthStorageData{
		"fanout-prov":       {Type: CredentialTypeOAuth, Access: "a", Label: "personal"},
		"fanout-prov#work":  {Type: CredentialTypeOAuth, Access: "b", Label: "work@x.com"},
		"fanout-prov#extra": {Type: CredentialTypeOAuth, Access: "c"},
	})

	ps := s.GetOAuthProviders()
	ids := map[string]bool{}
	for _, p := range ps {
		ids[p.ID()] = true
	}
	for _, want := range []string{"fanout-prov", "fanout-prov#work", "fanout-prov#extra"} {
		if !ids[want] {
			t.Errorf("missing provider %q in fan-out (%v)", want, ids)
		}
	}
}

func TestLoginAccountSlotAssignment(t *testing.T) {
	ai.ResetOAuthProviders()
	t.Cleanup(ai.ResetOAuthProviders)
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	s := NewAuthStorage(path)

	prov := &recordingProvider{
		id: "login-prov",
		loginCreds: &ai.OAuthCredentials{
			Access: "tok1",
			Extra:  map[string]any{"accountId": "alice", "label": "alice@x.com"},
		},
	}
	ai.RegisterOAuthProvider(prov)

	// First login -> default slot.
	slot, label, err := s.LoginAccount(context.Background(), "login-prov", pinoauth.LoginCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	if slot != "login-prov" {
		t.Errorf("first login slot = %q want login-prov", slot)
	}
	if label != "alice@x.com" {
		t.Errorf("label = %q", label)
	}

	// Second login, different identity -> new named slot.
	prov.loginCreds = &ai.OAuthCredentials{
		Access: "tok2",
		Extra:  map[string]any{"accountId": "bob", "label": "bob@x.com"},
	}
	slot2, _, err := s.LoginAccount(context.Background(), "login-prov", pinoauth.LoginCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	if slot2 != "login-prov#bob" {
		t.Errorf("second login slot = %q want login-prov#bob", slot2)
	}

	// Re-login as alice -> overwrites the default slot in place (no new slot).
	prov.loginCreds = &ai.OAuthCredentials{
		Access: "tok1b",
		Extra:  map[string]any{"accountId": "alice", "label": "alice@x.com"},
	}
	slot3, _, err := s.LoginAccount(context.Background(), "login-prov", pinoauth.LoginCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	if slot3 != "login-prov" {
		t.Errorf("re-login alice slot = %q want login-prov (overwrite default)", slot3)
	}
	if got := s.GetApiKey("login-prov"); got != "tok1b" {
		t.Errorf("default token after re-login = %q want tok1b", got)
	}

	accts := s.AccountsForProvider("login-prov")
	if len(accts) != 2 {
		t.Fatalf("accounts = %d want 2", len(accts))
	}
}

// TestRefreshPreservesAccountIdentity ensures a token refresh that returns no
// profile data does not drop the account Label/identity — otherwise a re-login
// of the same account would spawn a duplicate slot.
func TestRefreshPreservesAccountIdentity(t *testing.T) {
	ai.ResetOAuthProviders()
	t.Cleanup(ai.ResetOAuthProviders)
	dir := t.TempDir()
	s := NewAuthStorage(filepath.Join(dir, "auth.json"))

	prov := &recordingProvider{id: "refresh-prov"}
	ai.RegisterOAuthProvider(prov)

	// Stored account with identity in Extra, already expired.
	s.Set("refresh-prov", AuthCredential{
		Type:    CredentialTypeOAuth,
		Access:  "old",
		Refresh: "r",
		Label:   "me@x.com",
		Expires: 1, // expired
		Extra:   map[string]any{"accountId": "uuid-me"},
	})

	// RefreshToken returns a bare triple (no Extra) — common case.
	prov.modifySeen = nil
	provRefresh := &refreshingProvider{recordingProvider: prov, fresh: &ai.OAuthCredentials{
		Access: "new", Refresh: "r2", Expires: 1 << 62,
	}}
	ai.RegisterOAuthProvider(provRefresh)

	if got := s.GetApiKey("refresh-prov"); got != "new" {
		t.Fatalf("refreshed key = %q want new", got)
	}
	cred := s.Get("refresh-prov")
	if cred == nil {
		t.Fatal("cred missing after refresh")
	}
	if cred.Label != "me@x.com" {
		t.Errorf("label lost across refresh: %q", cred.Label)
	}
	if accountIDFromCreds(AuthCredToOAuthCreds(cred)) != "uuid-me" {
		t.Errorf("accountId lost across refresh: %+v", cred.Extra)
	}
}

// refreshingProvider overrides RefreshToken to return a fixed fresh credential.
type refreshingProvider struct {
	*recordingProvider
	fresh *ai.OAuthCredentials
}

func (m *refreshingProvider) RefreshToken(_ context.Context, _ *ai.OAuthCredentials) (*ai.OAuthCredentials, error) {
	return m.fresh, nil
}

// TestGetApiKey_AWSIAMEnvelope: an aws_iam account resolves to a decodable
// Bedrock IAM envelope; a bearer (api_key) account resolves to the raw token.
func TestGetApiKey_AWSIAMEnvelope(t *testing.T) {
	s := NewInMemoryAuthStorage(AuthStorageData{
		"amazon-bedrock": {
			Type:  CredentialTypeAWSIAM,
			Extra: map[string]any{"mode": "profile", "profile": "work", "region": "eu-west-1"},
		},
		"amazon-bedrock#bear": {
			Type: CredentialTypeAPIKey,
			Key:  "bedrock-bearer-xyz",
		},
	})

	env := s.GetApiKey("amazon-bedrock")
	iam, ok := ai.DecodeBedrockIAMCreds(env)
	if !ok {
		t.Fatalf("aws_iam GetApiKey did not produce an IAM envelope: %q", env)
	}
	if iam.Mode != "profile" || iam.Profile != "work" || iam.Region != "eu-west-1" {
		t.Errorf("decoded IAM creds wrong: %+v", iam)
	}

	if got := s.GetApiKey("amazon-bedrock#bear"); got != "bedrock-bearer-xyz" {
		t.Errorf("bearer GetApiKey = %q", got)
	}
}

func TestBedrockIAMFromExtra_ModeDefault(t *testing.T) {
	// No explicit mode but access key present -> "keys".
	c := bedrockIAMFromExtra(map[string]any{"accessKey": "AK", "secretKey": "SK"})
	if c.Mode != "keys" {
		t.Errorf("mode = %q want keys", c.Mode)
	}
	// No mode, no keys -> "profile".
	c2 := bedrockIAMFromExtra(map[string]any{"profile": "p"})
	if c2.Mode != "profile" {
		t.Errorf("mode = %q want profile", c2.Mode)
	}
}
