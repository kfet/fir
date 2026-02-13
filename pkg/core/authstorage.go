// Ported from: packages/coding-agent/src/core/auth-storage.ts
// Upstream hash: 1caadb2e
package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/kfet/pi-go/pkg/ai"
	"github.com/kfet/pi-go/pkg/ai/oauth"
)

// CredentialType identifies the kind of stored credential.
type CredentialType string

const (
	CredentialTypeAPIKey CredentialType = "api_key"
	CredentialTypeOAuth  CredentialType = "oauth"
)

// AuthCredential represents a stored credential.
type AuthCredential struct {
	Type CredentialType `json:"type"`

	// For api_key type
	Key string `json:"key,omitempty"`

	// For oauth type
	Access    string `json:"access,omitempty"`
	Refresh   string `json:"refresh,omitempty"`
	Expires   int64  `json:"expires,omitempty"`
	ProjectID string `json:"projectId,omitempty"` // Google providers
}

// AuthStorageData is the on-disk format: provider → credential.
type AuthStorageData map[string]AuthCredential

// AuthStorage manages credential storage backed by a JSON file.
type AuthStorage struct {
	mu               sync.RWMutex
	authPath         string
	data             AuthStorageData
	runtimeOverrides map[string]string
	fallbackResolver func(provider string) string
}

// NewAuthStorage creates an AuthStorage backed by the given file path.
func NewAuthStorage(authPath string) *AuthStorage {
	s := &AuthStorage{
		authPath:         authPath,
		data:             make(AuthStorageData),
		runtimeOverrides: make(map[string]string),
	}
	s.Reload()
	return s
}

// SetRuntimeApiKey sets a runtime API key override (not persisted to disk).
// Used for CLI --api-key flag.
func (s *AuthStorage) SetRuntimeApiKey(provider, apiKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtimeOverrides[provider] = apiKey
}

// RemoveRuntimeApiKey removes a runtime API key override.
func (s *AuthStorage) RemoveRuntimeApiKey(provider string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.runtimeOverrides, provider)
}

// SetFallbackResolver sets a fallback resolver for API keys not found elsewhere.
// Used for custom provider keys from models.json.
func (s *AuthStorage) SetFallbackResolver(resolver func(provider string) string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fallbackResolver = resolver
}

// Reload re-reads credentials from disk.
func (s *AuthStorage) Reload() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reload()
}

func (s *AuthStorage) reload() {
	data, err := os.ReadFile(s.authPath)
	if err != nil {
		s.data = make(AuthStorageData)
		return
	}
	var parsed AuthStorageData
	if err := json.Unmarshal(data, &parsed); err != nil {
		s.data = make(AuthStorageData)
		return
	}
	s.data = parsed
}

func (s *AuthStorage) save() error {
	dir := filepath.Dir(s.authPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create auth dir: %w", err)
	}
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal auth data: %w", err)
	}
	if err := os.WriteFile(s.authPath, data, 0600); err != nil {
		return fmt.Errorf("write auth file: %w", err)
	}
	return nil
}

// Get returns the credential for a provider, or nil if not found.
func (s *AuthStorage) Get(provider string) *AuthCredential {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cred, ok := s.data[provider]
	if !ok {
		return nil
	}
	return &cred
}

// Set stores a credential for a provider.
func (s *AuthStorage) Set(provider string, cred AuthCredential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[provider] = cred
	return s.save()
}

// Remove deletes a credential for a provider.
func (s *AuthStorage) Remove(provider string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, provider)
	return s.save()
}

// List returns all providers with stored credentials.
func (s *AuthStorage) List() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]string, 0, len(s.data))
	for k := range s.data {
		result = append(result, k)
	}
	return result
}

// Has returns whether credentials exist for a provider in auth.json.
func (s *AuthStorage) Has(provider string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.data[provider]
	return ok
}

// HasAuth checks if any form of auth is configured for a provider.
// Unlike GetApiKey, this doesn't refresh OAuth tokens.
func (s *AuthStorage) HasAuth(provider string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, ok := s.runtimeOverrides[provider]; ok {
		return true
	}
	if _, ok := s.data[provider]; ok {
		return true
	}
	if ai.GetEnvApiKey(provider) != "" {
		return true
	}
	if s.fallbackResolver != nil && s.fallbackResolver(provider) != "" {
		return true
	}
	return false
}

// GetApiKey resolves the API key for a provider.
// Priority:
// 1. Runtime override (CLI --api-key)
// 2. API key from auth.json
// 3. OAuth token from auth.json (auto-refreshed if expired)
// 4. Environment variable
// 5. Fallback resolver (models.json custom providers)
func (s *AuthStorage) GetApiKey(provider string) string {
	s.mu.RLock()

	// Runtime override takes highest priority
	if key, ok := s.runtimeOverrides[provider]; ok {
		s.mu.RUnlock()
		return key
	}

	// Check auth.json
	if cred, ok := s.data[provider]; ok {
		if cred.Type == CredentialTypeAPIKey && cred.Key != "" {
			s.mu.RUnlock()
			return cred.Key
		}
		if cred.Type == CredentialTypeOAuth && cred.Access != "" {
			oauthProvider := oauth.GetProvider(provider)
			if oauthProvider == nil {
				// Unknown OAuth provider — return access token as-is
				s.mu.RUnlock()
				return cred.Access
			}

			// Check if token needs refresh
			needsRefresh := cred.Expires > 0 && time.Now().UnixMilli() >= cred.Expires

			if needsRefresh {
				s.mu.RUnlock()
				// Upgrade to write lock for refresh
				return s.refreshOAuthToken(provider, oauthProvider)
			}

			// Token not expired — return via provider's GetAPIKey
			oauthCreds := authCredToOAuthCreds(&cred)
			key := oauthProvider.GetAPIKey(oauthCreds)
			s.mu.RUnlock()
			return key
		}
	}
	s.mu.RUnlock()

	// Environment variable
	if key := ai.GetEnvApiKey(provider); key != "" {
		return key
	}

	// Fallback resolver
	if s.fallbackResolver != nil {
		if key := s.fallbackResolver(provider); key != "" {
			return key
		}
	}

	return ""
}

// refreshOAuthToken handles token refresh under a write lock.
// It re-checks the token after acquiring the lock in case another
// goroutine already refreshed it.
func (s *AuthStorage) refreshOAuthToken(provider string, oauthProvider oauth.Provider) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Re-read from disk to check if another process refreshed
	s.reload()

	cred, ok := s.data[provider]
	if !ok || cred.Type != CredentialTypeOAuth {
		return ""
	}

	// Check if still expired (another goroutine/process may have refreshed)
	if cred.Expires > 0 && time.Now().UnixMilli() < cred.Expires {
		oauthCreds := authCredToOAuthCreds(&cred)
		return oauthProvider.GetAPIKey(oauthCreds)
	}

	// Perform the refresh
	oauthCreds := authCredToOAuthCreds(&cred)
	newCreds, err := oauthProvider.RefreshToken(oauthCreds)
	if err != nil {
		// Refresh failed — return empty (user can /login to re-auth)
		return ""
	}

	// Save the refreshed credentials
	s.data[provider] = oauthCredsToAuthCred(newCreds)
	_ = s.save()

	return oauthProvider.GetAPIKey(newCreds)
}

// authCredToOAuthCreds converts an AuthCredential to an oauth.Credentials.
func authCredToOAuthCreds(cred *AuthCredential) *oauth.Credentials {
	c := &oauth.Credentials{
		Access:  cred.Access,
		Refresh: cred.Refresh,
		Expires: cred.Expires,
	}
	if cred.ProjectID != "" {
		c.Extra = map[string]any{"projectId": cred.ProjectID}
	}
	return c
}

// oauthCredsToAuthCred converts oauth.Credentials to an AuthCredential.
func oauthCredsToAuthCred(creds *oauth.Credentials) AuthCredential {
	ac := AuthCredential{
		Type:    CredentialTypeOAuth,
		Access:  creds.Access,
		Refresh: creds.Refresh,
		Expires: creds.Expires,
	}
	if projectID, ok := creds.Extra["projectId"].(string); ok {
		ac.ProjectID = projectID
	}
	return ac
}

// GetAll returns a copy of all stored credentials.
func (s *AuthStorage) GetAll() AuthStorageData {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(AuthStorageData, len(s.data))
	for k, v := range s.data {
		result[k] = v
	}
	return result
}

// Login performs OAuth login for the given provider and stores credentials.
func (s *AuthStorage) Login(providerID string, callbacks oauth.LoginCallbacks) error {
	provider := oauth.GetProvider(providerID)
	if provider == nil {
		return fmt.Errorf("unknown OAuth provider: %s", providerID)
	}

	creds, err := provider.Login(callbacks)
	if err != nil {
		return err
	}

	return s.Set(providerID, oauthCredsToAuthCred(creds))
}

// Logout removes credentials for a provider.
func (s *AuthStorage) Logout(provider string) error {
	return s.Remove(provider)
}

// GetOAuthProviders returns all registered OAuth providers.
func (s *AuthStorage) GetOAuthProviders() []oauth.Provider {
	return oauth.GetProviders()
}
