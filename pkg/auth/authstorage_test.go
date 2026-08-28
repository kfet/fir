package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/pinoauth"
)

func TestAuthStorage_SetAndGet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	s := NewAuthStorage(path)
	s.Set("anthropic", AuthCredential{Type: "api_key", Key: "sk-test"})

	cred := s.Get("anthropic")
	if cred == nil {
		t.Fatal("expected credential")
	}
	if cred.Key != "sk-test" {
		t.Errorf("key = %q, want sk-test", cred.Key)
	}

	// Verify persisted
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	if len(data) == 0 {
		t.Error("auth file should not be empty")
	}
}

func TestAuthStorage_Remove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	s := NewAuthStorage(path)
	s.Set("anthropic", AuthCredential{Type: "api_key", Key: "sk-test"})
	s.Remove("anthropic")

	if s.Has("anthropic") {
		t.Error("should not have anthropic after remove")
	}
}

func TestAuthStorage_List(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	s := NewAuthStorage(path)
	s.Set("anthropic", AuthCredential{Type: "api_key", Key: "key1"})
	s.Set("openai", AuthCredential{Type: "api_key", Key: "key2"})

	providers := s.List()
	if len(providers) != 2 {
		t.Errorf("expected 2 providers, got %d", len(providers))
	}
}

func TestAuthStorage_RuntimeOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	s := NewAuthStorage(path)
	s.Set("anthropic", AuthCredential{Type: "api_key", Key: "stored-key"})
	s.SetRuntimeApiKey("anthropic", "runtime-key")

	key := s.GetApiKey("anthropic")
	if key != "runtime-key" {
		t.Errorf("key = %q, want runtime-key", key)
	}

	s.RemoveRuntimeApiKey("anthropic")
	key = s.GetApiKey("anthropic")
	if key != "stored-key" {
		t.Errorf("key = %q, want stored-key", key)
	}
}

func TestAuthStorage_FallbackResolver(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	s := NewAuthStorage(path)
	s.SetFallbackResolver(func(provider string) string {
		if provider == "custom" {
			return "custom-key"
		}
		return ""
	})

	key := s.GetApiKey("custom")
	if key != "custom-key" {
		t.Errorf("key = %q, want custom-key", key)
	}
}

func TestAuthStorage_Reload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	s1 := NewAuthStorage(path)
	s1.Set("anthropic", AuthCredential{Type: "api_key", Key: "key1"})

	s2 := NewAuthStorage(path)
	if !s2.Has("anthropic") {
		t.Error("s2 should see anthropic after loading from same file")
	}
	cred := s2.Get("anthropic")
	if cred == nil || cred.Key != "key1" {
		t.Error("s2 should see same key")
	}
}

// A corrupt store must not wipe live credentials: Reload now runs mid-session
// (/mcp reload re-reads the store to pick up an out-of-band login), and
// swapping in an empty map would log the session out of every provider.
func TestAuthStorage_Reload_CorruptFileKeepsCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")

	s := NewAuthStorage(path)
	s.Set("anthropic", AuthCredential{Type: "api_key", Key: "key1"})

	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt auth.json: %v", err)
	}
	s.Reload()

	cred := s.Get("anthropic")
	if cred == nil || cred.Key != "key1" {
		t.Fatalf("credential lost on corrupt reload: %+v", cred)
	}
	if errs := s.DrainErrors(); len(errs) == 0 {
		t.Error("corrupt reload should record an error")
	}

	// Writes still work afterwards, repairing the damaged file.
	s.Set("openai", AuthCredential{Type: "api_key", Key: "key2"})
	s2 := NewAuthStorage(path)
	if c := s2.Get("openai"); c == nil || c.Key != "key2" {
		t.Errorf("login after a corrupt read must persist, got %+v", c)
	}
}

func TestAuthStorage_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	s := NewAuthStorage(path)
	if s.Has("anything") {
		t.Error("empty storage should not have any credentials")
	}

	key := s.GetApiKey("anthropic")
	if key != "" {
		t.Errorf("expected empty key, got %q", key)
	}
}

func TestAuthStorage_GetApiKey_OAuth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	s := NewAuthStorage(path)

	// OAuth credential without a registered provider should return empty —
	// the token can't be refreshed and the provider won't use Bearer auth.
	s.Set("anthropic", AuthCredential{
		Type:    CredentialTypeOAuth,
		Access:  "sk-ant-oat01-test-token",
		Refresh: "sk-ant-ort01-refresh",
		Expires: 9999999999999,
	})

	key := s.GetApiKey("anthropic")
	if key != "" {
		t.Errorf("expected empty key without OAuth provider, got %q", key)
	}

	// With a registered OAuth provider, the token should be returned.
	mock := &mockOAuthProvider{id: "anthropic"}
	ai.RegisterOAuthProvider(mock)
	defer ai.UnregisterOAuthProvider("anthropic")

	key = s.GetApiKey("anthropic")
	if key != "sk-ant-oat01-test-token" {
		t.Errorf("expected OAuth access token with provider, got %q", key)
	}
}

func TestAuthStorage_GetApiKey_OAuth_EmptyAccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	s := NewAuthStorage(path)

	// OAuth credential with empty access token should fall through
	s.Set("anthropic", AuthCredential{
		Type:    CredentialTypeOAuth,
		Access:  "",
		Refresh: "refresh-only",
	})

	key := s.GetApiKey("anthropic")
	if key != "" {
		t.Errorf("expected empty key for OAuth with no access token, got %q", key)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && len(sub) > 0 && findSubstring(s, sub))
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestAuthStorage_HasAuth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	s := NewAuthStorage(path)
	if s.HasAuth("anthropic") {
		t.Error("should not have auth initially")
	}

	s.Set("anthropic", AuthCredential{Type: "api_key", Key: "key"})
	if !s.HasAuth("anthropic") {
		t.Error("should have auth after set")
	}
}

// mockOAuthProvider implements ai.OAuthProvider for testing.
type mockOAuthProvider struct {
	id           string
	refreshCalls int
	loginCreds   *ai.OAuthCredentials
	refreshCreds *ai.OAuthCredentials
	refreshErr   error
}

func (m *mockOAuthProvider) ID() string               { return m.id }
func (m *mockOAuthProvider) Name() string             { return "Mock " + m.id }
func (m *mockOAuthProvider) UsesCallbackServer() bool { return false }

func (m *mockOAuthProvider) Login(_ context.Context, callbacks pinoauth.LoginCallbacks) (*ai.OAuthCredentials, error) {
	return m.loginCreds, nil
}

func (m *mockOAuthProvider) ListModels(_ context.Context, _ *ai.OAuthCredentials) ([]string, error) {
	return nil, nil
}

func (m *mockOAuthProvider) RefreshToken(_ context.Context, creds *ai.OAuthCredentials) (*ai.OAuthCredentials, error) {
	m.refreshCalls++
	if m.refreshErr != nil {
		return nil, m.refreshErr
	}
	return m.refreshCreds, nil
}

func (m *mockOAuthProvider) GetAPIKey(creds *ai.OAuthCredentials) string {
	return creds.Access
}

func (m *mockOAuthProvider) ModifyModels(models []*ai.Model, _ *ai.OAuthCredentials) []*ai.Model {
	return models
}

func (m *mockOAuthProvider) ModelDefaults(_ string, _ []*ai.Model) *ai.Model {
	return nil
}

func TestAuthStorage_GetApiKey_OAuthRefresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	mp := &mockOAuthProvider{
		id: "test-refresh-provider",
		refreshCreds: &ai.OAuthCredentials{
			Access:  "new-access-token",
			Refresh: "new-refresh-token",
			Expires: time.Now().UnixMilli() + 3600000,
		},
	}
	ai.RegisterOAuthProvider(mp)
	defer func() {
		// Can't unregister, but won't affect other tests
	}()

	s := NewAuthStorage(path)

	// Set expired OAuth credentials
	s.Set("test-refresh-provider", AuthCredential{
		Type:    CredentialTypeOAuth,
		Access:  "old-access-token",
		Refresh: "old-refresh-token",
		Expires: time.Now().UnixMilli() - 1000, // Expired
	})

	// GetApiKey should trigger refresh
	key := s.GetApiKey("test-refresh-provider")
	if key != "new-access-token" {
		t.Errorf("GetApiKey() = %q, want %q", key, "new-access-token")
	}
	if mp.refreshCalls != 1 {
		t.Errorf("refreshCalls = %d, want 1", mp.refreshCalls)
	}

	// Verify credentials were saved to disk
	s2 := NewAuthStorage(path)
	cred := s2.Get("test-refresh-provider")
	if cred == nil {
		t.Fatal("credentials not saved to disk")
	}
	if cred.Access != "new-access-token" {
		t.Errorf("saved access = %q", cred.Access)
	}

	// Verify no key error after successful refresh
	if err := s.GetApiKeyError("test-refresh-provider"); err != nil {
		t.Errorf("GetApiKeyError() = %v, want nil after successful refresh", err)
	}

	// The refresh must be recorded in the audit log (set + refresh).
	entries := readAuditLog(t, dir)
	var sawRefresh bool
	for _, e := range entries {
		if e.Action == AuditActionRefresh && e.Slot == "test-refresh-provider" {
			sawRefresh = true
		}
	}
	if !sawRefresh {
		t.Errorf("no refresh entry in audit log: %+v", entries)
	}
}

func TestAuthStorage_GetApiKey_OAuthNotExpired(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	mp := &mockOAuthProvider{
		id: "test-no-refresh",
	}
	ai.RegisterOAuthProvider(mp)

	s := NewAuthStorage(path)

	// Set non-expired OAuth credentials
	s.Set("test-no-refresh", AuthCredential{
		Type:    CredentialTypeOAuth,
		Access:  "valid-token",
		Refresh: "r",
		Expires: time.Now().UnixMilli() + 3600000,
	})

	key := s.GetApiKey("test-no-refresh")
	if key != "valid-token" {
		t.Errorf("GetApiKey() = %q, want %q", key, "valid-token")
	}
	if mp.refreshCalls != 0 {
		t.Errorf("should not have refreshed; refreshCalls = %d", mp.refreshCalls)
	}
}

func TestAuthStorage_GetApiKey_OAuthRefreshFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	mp := &mockOAuthProvider{
		id:         "test-refresh-fail",
		refreshErr: errors.New("refresh failed"),
	}
	ai.RegisterOAuthProvider(mp)

	s := NewAuthStorage(path)

	s.Set("test-refresh-fail", AuthCredential{
		Type:    CredentialTypeOAuth,
		Access:  "expired-token",
		Refresh: "r",
		Expires: time.Now().UnixMilli() - 1000,
	})

	// When refresh fails, should return empty string
	key := s.GetApiKey("test-refresh-fail")
	if key != "" {
		t.Errorf("GetApiKey() = %q, want empty on refresh failure", key)
	}

	// Error should be recorded and retrievable
	keyErr := s.GetApiKeyError("test-refresh-fail")
	if keyErr == nil {
		t.Fatal("GetApiKeyError() = nil, want error after refresh failure")
	}
	if !strings.Contains(keyErr.Error(), "refresh failed") {
		t.Errorf("GetApiKeyError() = %q, want it to contain 'refresh failed'", keyErr)
	}
	if !strings.Contains(keyErr.Error(), "test-refresh-fail") {
		t.Errorf("GetApiKeyError() = %q, want it to contain provider name", keyErr)
	}
}

func TestAuthStorage_Login(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	mp := &mockOAuthProvider{
		id: "test-login-provider",
		loginCreds: &ai.OAuthCredentials{
			Access:  "login-token",
			Refresh: "login-refresh",
			Expires: time.Now().UnixMilli() + 3600000,
		},
	}
	ai.RegisterOAuthProvider(mp)

	s := NewAuthStorage(path)

	err := s.Login(context.Background(), "test-login-provider", pinoauth.LoginCallbacks{
		OnAuth: func(_ pinoauth.AuthInfo) {},
		OnPrompt: func(_ pinoauth.Prompt) (string, error) {
			return "code", nil
		},
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	cred := s.Get("test-login-provider")
	if cred == nil {
		t.Fatal("credential not stored")
	}
	if cred.Type != CredentialTypeOAuth {
		t.Errorf("type = %q, want oauth", cred.Type)
	}
	if cred.Access != "login-token" {
		t.Errorf("access = %q", cred.Access)
	}
}

func TestAuthStorage_Login_UnknownProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	s := NewAuthStorage(path)

	err := s.Login(context.Background(), "nonexistent-provider", pinoauth.LoginCallbacks{})
	if err == nil {
		t.Error("expected error for unknown provider")
	}
}

func TestAuthStorage_GetOAuthProviders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	s := NewAuthStorage(path)

	providers := s.GetOAuthProviders()
	if len(providers) < 1 {
		t.Errorf("GetOAuthProviders() returned %d, want at least 1", len(providers))
	}
}

func TestAuthStorage_DrainErrors_NoErrors(t *testing.T) {
	dir := t.TempDir()
	s := NewAuthStorage(filepath.Join(dir, "auth.json"))
	errs := s.DrainErrors()
	if len(errs) != 0 {
		t.Errorf("DrainErrors() = %v, want empty", errs)
	}
}

func TestAuthStorage_DrainErrors_ClearsAfterDrain(t *testing.T) {
	dir := t.TempDir()
	s := NewAuthStorage(filepath.Join(dir, "auth.json"))
	s.DrainErrors()
	errs := s.DrainErrors()
	if len(errs) != 0 {
		t.Errorf("second DrainErrors() = %v, want empty", errs)
	}
}

func TestNewInMemoryAuthStorage_Empty(t *testing.T) {
	s := NewInMemoryAuthStorage(nil)
	cred := s.Get("anthropic")
	if cred != nil {
		t.Errorf("expected nil credential for empty storage")
	}
}

func TestNewInMemoryAuthStorage_WithData(t *testing.T) {
	data := AuthStorageData{
		"anthropic": {Type: CredentialTypeAPIKey, Key: "sk-test"},
	}
	s := NewInMemoryAuthStorage(data)
	cred := s.Get("anthropic")
	if cred == nil {
		t.Fatal("expected credential")
	}
	if cred.Key != "sk-test" {
		t.Errorf("Key = %q, want sk-test", cred.Key)
	}
}

func TestInMemoryAuthStorage_SetAndGet(t *testing.T) {
	s := NewInMemoryAuthStorage(nil)
	err := s.Set("openai", AuthCredential{Type: CredentialTypeAPIKey, Key: "sk-openai"})
	if err != nil {
		t.Fatalf("Set() error: %v", err)
	}

	cred := s.Get("openai")
	if cred == nil {
		t.Fatal("expected credential")
	}
	if cred.Key != "sk-openai" {
		t.Errorf("Key = %q, want sk-openai", cred.Key)
	}
}

// TestAuthCredOAuthCreds_RoundTripExtra is a regression test for a
// pinoauth v0.2.0-migration-era footgun: AuthCredential's on-disk shape
// only knew about ProjectID as a top-level column, so any other
// provider-specific Extra key (codex's accountId, antigravity's email,
// etc.) silently dropped on persistence even though the fir-side
// ai.OAuthCredentials.Extra map promised general round-tripping.
func TestAuthCredOAuthCreds_RoundTripExtra(t *testing.T) {
	in := &ai.OAuthCredentials{
		Access:  "AT",
		Refresh: "RT",
		Expires: 12345,
		Extra: map[string]any{
			"projectId": "rising-fact-p41fc",
			"accountId": "acc-123",
			"email":     "user@example.com",
		},
	}
	stored := OAuthCredsToAuthCred(in)
	if stored.ProjectID != "rising-fact-p41fc" {
		t.Errorf("ProjectID legacy column = %q, want rising-fact-p41fc", stored.ProjectID)
	}
	out := AuthCredToOAuthCreds(&stored)
	if out.Access != "AT" || out.Refresh != "RT" || out.Expires != 12345 {
		t.Errorf("core fields lost: %+v", out)
	}
	if out.Extra["projectId"] != "rising-fact-p41fc" {
		t.Errorf("projectId lost: %+v", out.Extra)
	}
	if out.Extra["accountId"] != "acc-123" {
		t.Errorf("accountId lost: %+v", out.Extra)
	}
	if out.Extra["email"] != "user@example.com" {
		t.Errorf("email lost: %+v", out.Extra)
	}
}

// TestAuthCredToOAuthCreds_LegacyProjectID covers the on-disk back-compat
// path: an AuthCredential written by an older fir build has only the
// ProjectID column populated (no Extra map). Reading it must lift the
// projectId into the new Extra["projectId"] slot transparently.
func TestAuthCredToOAuthCreds_LegacyProjectID(t *testing.T) {
	legacy := &AuthCredential{
		Type:      CredentialTypeOAuth,
		Access:    "AT",
		Refresh:   "RT",
		Expires:   1,
		ProjectID: "legacy-proj",
		// Extra intentionally nil — older on-disk shape.
	}
	out := AuthCredToOAuthCreds(legacy)
	if out.Extra["projectId"] != "legacy-proj" {
		t.Errorf("legacy ProjectID column not lifted into Extra: %+v", out.Extra)
	}
}
