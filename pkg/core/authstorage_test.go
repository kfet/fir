package core

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kfet/tau/pkg/ai"
	"github.com/kfet/tau/pkg/ai/oauth"
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

	// Test OAuth credential returns access token
	s.Set("anthropic", AuthCredential{
		Type:    CredentialTypeOAuth,
		Access:  "sk-ant-oat01-test-token",
		Refresh: "sk-ant-ort01-refresh",
		Expires: 9999999999999,
	})

	key := s.GetApiKey("anthropic")
	if key != "sk-ant-oat01-test-token" {
		t.Errorf("expected OAuth access token, got %q", key)
	}
}

func TestAuthStorage_GetApiKey_OAuth_Google(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	s := NewAuthStorage(path)

	// Google OAuth returns JSON with token + projectId
	s.Set("google-gemini-cli", AuthCredential{
		Type:      CredentialTypeOAuth,
		Access:    "ya29.test-google-token",
		Refresh:   "1//test-refresh",
		Expires:   9999999999999,
		ProjectID: "my-project-id",
	})

	key := s.GetApiKey("google-gemini-cli")
	if key == "" {
		t.Fatal("expected non-empty key")
	}
	// Should be JSON with token and projectId
	if !contains(key, "ya29.test-google-token") {
		t.Errorf("expected key to contain access token, got %q", key)
	}
	if !contains(key, "my-project-id") {
		t.Errorf("expected key to contain projectId, got %q", key)
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

func TestAuthStorage_GetApiKey_OAuth_ReloadFromDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	// Write OAuth credentials directly to the file (simulating what the TS version writes)
	jsonData := `{
		"anthropic": {
			"type": "oauth",
			"refresh": "sk-ant-ort01-refresh",
			"access": "sk-ant-oat01-access-token",
			"expires": 9999999999999
		},
		"google-gemini-cli": {
			"type": "oauth",
			"refresh": "1//refresh",
			"access": "ya29.google-token",
			"expires": 9999999999999,
			"projectId": "test-project"
		}
	}`
	os.WriteFile(path, []byte(jsonData), 0600)

	s := NewAuthStorage(path)

	// Anthropic OAuth
	key := s.GetApiKey("anthropic")
	if key != "sk-ant-oat01-access-token" {
		t.Errorf("anthropic: expected OAuth access token, got %q", key)
	}

	// Google OAuth (should return JSON)
	key = s.GetApiKey("google-gemini-cli")
	if !contains(key, "ya29.google-token") || !contains(key, "test-project") {
		t.Errorf("google-gemini-cli: expected JSON with token and projectId, got %q", key)
	}

	// HasAuth should work for OAuth providers
	if !s.HasAuth("anthropic") {
		t.Error("should have auth for anthropic")
	}
	if !s.HasAuth("google-gemini-cli") {
		t.Error("should have auth for google-gemini-cli")
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

// mockOAuthProvider implements oauth.Provider for testing.
type mockOAuthProvider struct {
	id           string
	refreshCalls int
	loginCreds   *oauth.Credentials
	refreshCreds *oauth.Credentials
	refreshErr   error
}

func (m *mockOAuthProvider) ID() string              { return m.id }
func (m *mockOAuthProvider) Name() string             { return "Mock " + m.id }
func (m *mockOAuthProvider) UsesCallbackServer() bool { return false }

func (m *mockOAuthProvider) Login(callbacks oauth.LoginCallbacks) (*oauth.Credentials, error) {
	return m.loginCreds, nil
}

func (m *mockOAuthProvider) RefreshToken(creds *oauth.Credentials) (*oauth.Credentials, error) {
	m.refreshCalls++
	if m.refreshErr != nil {
		return nil, m.refreshErr
	}
	return m.refreshCreds, nil
}

func (m *mockOAuthProvider) GetAPIKey(creds *oauth.Credentials) string {
	return creds.Access
}

func (m *mockOAuthProvider) ModifyModels(models []*ai.Model, _ *oauth.Credentials) []*ai.Model {
	return models
}

func TestAuthStorage_GetApiKey_OAuthRefresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	mp := &mockOAuthProvider{
		id: "test-refresh-provider",
		refreshCreds: &oauth.Credentials{
			Access:  "new-access-token",
			Refresh: "new-refresh-token",
			Expires: time.Now().UnixMilli() + 3600000,
		},
	}
	oauth.RegisterProvider(mp)
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
}

func TestAuthStorage_GetApiKey_OAuthNotExpired(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	mp := &mockOAuthProvider{
		id: "test-no-refresh",
	}
	oauth.RegisterProvider(mp)

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
	oauth.RegisterProvider(mp)

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
}

func TestAuthStorage_Login(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	mp := &mockOAuthProvider{
		id: "test-login-provider",
		loginCreds: &oauth.Credentials{
			Access:  "login-token",
			Refresh: "login-refresh",
			Expires: time.Now().UnixMilli() + 3600000,
		},
	}
	oauth.RegisterProvider(mp)

	s := NewAuthStorage(path)

	err := s.Login("test-login-provider", oauth.LoginCallbacks{
		OnAuth: func(_ oauth.AuthInfo) {},
		OnPrompt: func(_ oauth.Prompt) (string, error) {
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

	err := s.Login("nonexistent-provider", oauth.LoginCallbacks{})
	if err == nil {
		t.Error("expected error for unknown provider")
	}
}

func TestAuthStorage_GetOAuthProviders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	s := NewAuthStorage(path)

	providers := s.GetOAuthProviders()
	if len(providers) < 5 {
		t.Errorf("GetOAuthProviders() returned %d, want at least 5", len(providers))
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
