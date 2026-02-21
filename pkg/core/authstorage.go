// Ported from: packages/coding-agent/src/core/auth-storage.ts
// Upstream hash: 4ba3e5be
package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/oauth"
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

// AuthStorageBackend abstracts the storage and locking mechanism for AuthStorage.
// fn receives the current JSON content (nil if not found) and returns
// new content to write (nil to skip write), plus an arbitrary result value.
type AuthStorageBackend interface {
	// WithLock atomically reads, optionally writes, and returns a result.
	WithLock(fn func(current []byte) (result any, next []byte)) (any, error)
	// WithLockFallible is like WithLock but the callback can return an error
	// (used for OAuth token refresh which does synchronous network I/O).
	WithLockFallible(fn func(current []byte) (result any, next []byte, err error)) (any, error)
}

// FileAuthStorageBackend stores credentials in a JSON file.
type FileAuthStorageBackend struct {
	mu       sync.Mutex
	authPath string
}

// NewFileAuthStorageBackend creates a backend backed by the given file path.
func NewFileAuthStorageBackend(authPath string) *FileAuthStorageBackend {
	return &FileAuthStorageBackend{authPath: authPath}
}

func (b *FileAuthStorageBackend) ensureParentDir() error {
	dir := filepath.Dir(b.authPath)
	return os.MkdirAll(dir, 0700)
}

func (b *FileAuthStorageBackend) readCurrent() []byte {
	data, err := os.ReadFile(b.authPath)
	if err != nil {
		return nil
	}
	return data
}

func (b *FileAuthStorageBackend) writeNext(data []byte) error {
	if err := b.ensureParentDir(); err != nil {
		return err
	}
	return os.WriteFile(b.authPath, data, 0600)
}

func (b *FileAuthStorageBackend) WithLock(fn func(current []byte) (result any, next []byte)) (any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ensureParentDir(); err != nil {
		return nil, err
	}
	current := b.readCurrent()
	result, next := fn(current)
	if next != nil {
		if err := b.writeNext(next); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (b *FileAuthStorageBackend) WithLockFallible(fn func(current []byte) (result any, next []byte, err error)) (any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ensureParentDir(); err != nil {
		return nil, err
	}
	current := b.readCurrent()
	result, next, err := fn(current)
	if err != nil {
		return nil, err
	}
	if next != nil {
		if err := b.writeNext(next); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// InMemoryAuthStorageBackend stores credentials in memory (no file I/O).
type InMemoryAuthStorageBackend struct {
	mu    sync.Mutex
	value []byte
}

func (b *InMemoryAuthStorageBackend) WithLock(fn func(current []byte) (result any, next []byte)) (any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	result, next := fn(b.value)
	if next != nil {
		b.value = next
	}
	return result, nil
}

func (b *InMemoryAuthStorageBackend) WithLockFallible(fn func(current []byte) (result any, next []byte, err error)) (any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	result, next, err := fn(b.value)
	if err != nil {
		return nil, err
	}
	if next != nil {
		b.value = next
	}
	return result, nil
}

// AuthStorage manages credential storage.
type AuthStorage struct {
	mu               sync.RWMutex
	storage          AuthStorageBackend
	data             AuthStorageData
	runtimeOverrides map[string]string
	fallbackResolver func(provider string) string
	loadError        error
	errors           []error
}

// NewAuthStorage creates an AuthStorage backed by the given file path.
// This is the primary constructor for file-based storage.
func NewAuthStorage(authPath string) *AuthStorage {
	if authPath == "" {
		authPath = filepath.Join(DefaultAgentDir(), "auth.json")
	}
	return newAuthStorage(NewFileAuthStorageBackend(authPath))
}

// NewInMemoryAuthStorage creates an AuthStorage backed by in-memory storage.
// Useful for tests.
func NewInMemoryAuthStorage(data AuthStorageData) *AuthStorage {
	backend := &InMemoryAuthStorageBackend{}
	if len(data) > 0 {
		b, _ := json.MarshalIndent(data, "", "  ")
		backend.value = b
	}
	return newAuthStorage(backend)
}

// newAuthStorage creates an AuthStorage backed by the given storage backend.
func newAuthStorage(storage AuthStorageBackend) *AuthStorage {
	s := &AuthStorage{
		storage:          storage,
		data:             make(AuthStorageData),
		runtimeOverrides: make(map[string]string),
	}
	s.Reload()
	return s
}

// SetRuntimeApiKey sets a runtime API key override (not persisted to disk).
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
func (s *AuthStorage) SetFallbackResolver(resolver func(provider string) string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fallbackResolver = resolver
}

func (s *AuthStorage) recordError(err error) {
	if err != nil {
		s.errors = append(s.errors, err)
	}
}

func (s *AuthStorage) parseStorageData(content []byte) AuthStorageData {
	if len(content) == 0 {
		return make(AuthStorageData)
	}
	var parsed AuthStorageData
	if err := json.Unmarshal(content, &parsed); err != nil {
		return make(AuthStorageData)
	}
	return parsed
}

// Reload re-reads credentials from storage.
func (s *AuthStorage) Reload() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.storage.WithLock(func(current []byte) (any, []byte) {
		s.data = s.parseStorageData(current)
		return nil, nil
	})
	if err != nil {
		s.loadError = err
		s.recordError(err)
	} else {
		s.loadError = nil
	}
}

func (s *AuthStorage) persistProviderChange(provider string, cred *AuthCredential) {
	if s.loadError != nil {
		return
	}
	_, err := s.storage.WithLock(func(current []byte) (any, []byte) {
		currentData := s.parseStorageData(current)
		if cred != nil {
			currentData[provider] = *cred
		} else {
			delete(currentData, provider)
		}
		b, _ := json.MarshalIndent(currentData, "", "  ")
		return nil, b
	})
	if err != nil {
		s.recordError(err)
	}
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
	s.persistProviderChange(provider, &cred)
	return nil
}

// Remove deletes a credential for a provider.
func (s *AuthStorage) Remove(provider string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, provider)
	s.persistProviderChange(provider, nil)
	return nil
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

// Has returns whether credentials exist for a provider.
func (s *AuthStorage) Has(provider string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.data[provider]
	return ok
}

// HasAuth checks if any form of auth is configured for a provider.
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

// DrainErrors returns and clears all accumulated errors.
func (s *AuthStorage) DrainErrors() []error {
	s.mu.Lock()
	defer s.mu.Unlock()
	drained := make([]error, len(s.errors))
	copy(drained, s.errors)
	s.errors = s.errors[:0]
	return drained
}

// GetApiKey resolves the API key for a provider.
// Priority:
// 1. Runtime override (CLI --api-key)
// 2. API key from auth storage
// 3. OAuth token from auth storage (auto-refreshed if expired)
// 4. Environment variable
// 5. Fallback resolver (models.json custom providers)
func (s *AuthStorage) GetApiKey(provider string) string {
	s.mu.RLock()

	// Runtime override takes highest priority
	if key, ok := s.runtimeOverrides[provider]; ok {
		s.mu.RUnlock()
		return key
	}

	// Check auth storage
	if cred, ok := s.data[provider]; ok {
		if cred.Type == CredentialTypeAPIKey && cred.Key != "" {
			s.mu.RUnlock()
			return cred.Key
		}
		if cred.Type == CredentialTypeOAuth && cred.Access != "" {
			oauthProvider := oauth.GetProvider(provider)
			if oauthProvider == nil {
				s.mu.RUnlock()
				return cred.Access
			}

			// Check if token needs refresh
			needsRefresh := cred.Expires > 0 && time.Now().UnixMilli() >= cred.Expires

			if needsRefresh {
				s.mu.RUnlock()
				return s.refreshOAuthToken(provider, oauthProvider)
			}

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

// refreshOAuthToken handles token refresh using the storage backend lock.
func (s *AuthStorage) refreshOAuthToken(provider string, oauthProvider oauth.Provider) string {
	type refreshResult struct {
		apiKey      string
		updatedData AuthStorageData
	}

	result, err := s.storage.WithLockFallible(func(current []byte) (any, []byte, error) {
		currentData := s.parseStorageData(current)

		cred, ok := currentData[provider]
		if !ok || cred.Type != CredentialTypeOAuth {
			return refreshResult{updatedData: currentData}, nil, nil
		}

		// Check if another process already refreshed
		if cred.Expires > 0 && time.Now().UnixMilli() < cred.Expires {
			oauthCreds := authCredToOAuthCreds(&cred)
			return refreshResult{apiKey: oauthProvider.GetAPIKey(oauthCreds), updatedData: currentData}, nil, nil
		}

		// Perform the refresh
		oauthCreds := authCredToOAuthCreds(&cred)
		newCreds, err := oauthProvider.RefreshToken(oauthCreds)
		if err != nil {
			return refreshResult{updatedData: currentData}, nil, nil
		}

		currentData[provider] = oauthCredsToAuthCred(newCreds)
		b, _ := json.MarshalIndent(currentData, "", "  ")
		return refreshResult{apiKey: oauthProvider.GetAPIKey(newCreds), updatedData: currentData}, b, nil
	})
	// Update in-memory state now that the backend lock is released.
	if r, ok := result.(refreshResult); ok {
		s.mu.Lock()
		s.data = r.updatedData
		s.loadError = nil
		s.mu.Unlock()
		if err == nil {
			return r.apiKey
		}
	}
	if err != nil {
		s.mu.Lock()
		s.recordError(err)
		s.mu.Unlock()
		// Refresh failed — re-read to check if another process succeeded.
		s.Reload()
		s.mu.RLock()
		cred, ok := s.data[provider]
		s.mu.RUnlock()
		if ok && cred.Type == CredentialTypeOAuth {
			oauthCreds := authCredToOAuthCreds(&cred)
			return oauthProvider.GetAPIKey(oauthCreds)
		}
	}
	return ""
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
