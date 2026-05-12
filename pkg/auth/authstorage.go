// Ported from: packages/coding-agent/src/core/auth-storage.ts
// Upstream hash: 4ba3e5be
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/envkeys"
	"github.com/kfet/pinoauth"
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
	Access  string `json:"access,omitempty"`
	Refresh string `json:"refresh,omitempty"`
	Expires int64  `json:"expires,omitempty"`
	// ProjectID is a legacy back-compat column for Google OAuth
	// providers that pre-date the generic Extra map. New providers
	// should put provider-specific data in Extra. Reads still consult
	// ProjectID and lift it into Extra["projectId"]; writes mirror
	// Extra["projectId"] back into ProjectID so older fir builds
	// reading newer auth.json files continue to find it.
	ProjectID string `json:"projectId,omitempty"`
	// Extra carries provider-specific OAuth credential data
	// (chatgpt_account_id for codex, email for the Google providers,
	// etc.) that survives across refreshes. The shape is
	// provider-defined; persisted as-is into auth.json.
	Extra map[string]any `json:"extra,omitempty"`
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

// readFromFile reads all content from the locked file descriptor.
func (b *FileAuthStorageBackend) readFromFile(f *os.File) []byte {
	if _, err := f.Seek(0, 0); err != nil {
		return nil
	}
	data, err := io.ReadAll(f)
	if err != nil || len(data) == 0 {
		return nil
	}
	return data
}

// writeToFile truncates and writes data to the locked file descriptor.
func (b *FileAuthStorageBackend) writeToFile(f *os.File, data []byte) error {
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	_, err := f.Write(data)
	return err
}

// withFileLock opens the auth data file itself, acquires an exclusive flock on
// it, and passes the locked file descriptor to fn. No separate .lock file is
// needed — the advisory lock lives on the data file's inode.
func (b *FileAuthStorageBackend) withFileLock(fn func(f *os.File) (any, error)) (any, error) {
	if err := b.ensureParentDir(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(b.authPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open auth file: %w", err)
	}
	defer f.Close()
	if err := flockExclusive(int(f.Fd())); err != nil {
		return nil, fmt.Errorf("acquire auth file lock: %w", err)
	}
	defer flockUnlock(int(f.Fd())) //nolint:errcheck
	return fn(f)
}

func (b *FileAuthStorageBackend) WithLock(fn func(current []byte) (result any, next []byte)) (any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.withFileLock(func(f *os.File) (any, error) {
		current := b.readFromFile(f)
		result, next := fn(current)
		if next != nil {
			if err := b.writeToFile(f, next); err != nil {
				return nil, err
			}
		}
		return result, nil
	})
}

func (b *FileAuthStorageBackend) WithLockFallible(fn func(current []byte) (result any, next []byte, err error)) (any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.withFileLock(func(f *os.File) (any, error) {
		current := b.readFromFile(f)
		result, next, err := fn(current)
		if err != nil {
			return nil, err
		}
		if next != nil {
			if err := b.writeToFile(f, next); err != nil {
				return nil, err
			}
		}
		return result, nil
	})
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
	lastKeyErrors    map[string]error // per-provider last key resolution error
}

// NewAuthStorage creates an AuthStorage backed by the given file path.
// authPath must be non-empty.
func NewAuthStorage(authPath string) *AuthStorage {
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
		lastKeyErrors:    make(map[string]error),
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
	if envkeys.GetEnvApiKey(provider) != "" {
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

// GetApiKeyError returns the last error encountered when resolving an API key
// for the given provider (e.g. an OAuth token refresh failure). Returns nil
// if no error occurred.
func (s *AuthStorage) GetApiKeyError(provider string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastKeyErrors[provider]
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
			oauthProvider := ai.GetOAuthProvider(provider)
			if oauthProvider == nil {
				// OAuth extension not loaded — token can't be refreshed and
				// the provider won't know to send it as Bearer auth. Return
				// empty so the caller gets a clear "no API key" error instead
				// of a confusing auth failure with x-api-key.
				s.mu.RUnlock()
				return ""
			}

			// Check if token needs refresh
			needsRefresh := cred.Expires > 0 && time.Now().UnixMilli() >= cred.Expires

			if needsRefresh {
				s.mu.RUnlock()
				return s.refreshOAuthToken(provider, oauthProvider)
			}

			oauthCreds := AuthCredToOAuthCreds(&cred)
			key := oauthProvider.GetAPIKey(oauthCreds)
			s.mu.RUnlock()
			return key
		}
	}
	s.mu.RUnlock()

	// Environment variable
	if key := envkeys.GetEnvApiKey(provider); key != "" {
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
func (s *AuthStorage) refreshOAuthToken(provider string, oauthProvider ai.OAuthProvider) string {
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
			oauthCreds := AuthCredToOAuthCreds(&cred)
			return refreshResult{apiKey: oauthProvider.GetAPIKey(oauthCreds), updatedData: currentData}, nil, nil
		}

		// Perform the refresh.
		// TODO(pinoauth-migration): RefreshToken now takes a context, but
		// the synchronous GetApiKey path has no natural ctx to thread.
		// Using Background until/unless GetApiKey grows a ctx parameter.
		oauthCreds := AuthCredToOAuthCreds(&cred)
		newCreds, err := oauthProvider.RefreshToken(context.Background(), oauthCreds)
		if err != nil {
			return refreshResult{updatedData: currentData}, nil, fmt.Errorf("OAuth token refresh failed for %s: %w", provider, err)
		}

		currentData[provider] = OAuthCredsToAuthCred(newCreds)
		b, _ := json.MarshalIndent(currentData, "", "  ")
		return refreshResult{apiKey: oauthProvider.GetAPIKey(newCreds), updatedData: currentData}, b, nil
	})
	// Update in-memory state now that the backend lock is released.
	if r, ok := result.(refreshResult); ok {
		s.mu.Lock()
		s.data = r.updatedData
		s.loadError = nil
		if err == nil {
			delete(s.lastKeyErrors, provider)
		}
		s.mu.Unlock()
		if err == nil {
			return r.apiKey
		}
	}
	if err != nil {
		s.mu.Lock()
		s.recordError(err)
		s.lastKeyErrors[provider] = err
		s.mu.Unlock()
		// Refresh failed — re-read to check if another process succeeded.
		s.Reload()
		s.mu.RLock()
		cred, ok := s.data[provider]
		s.mu.RUnlock()
		if ok && cred.Type == CredentialTypeOAuth {
			// Check if another process refreshed the token successfully
			if cred.Expires > 0 && time.Now().UnixMilli() < cred.Expires {
				s.mu.Lock()
				delete(s.lastKeyErrors, provider)
				s.mu.Unlock()
				oauthCreds := AuthCredToOAuthCreds(&cred)
				return oauthProvider.GetAPIKey(oauthCreds)
			}
			// Token still expired after re-read — another process didn't help.
			// Fall through to return "".
		}
	}
	return ""
}

// AuthCredToOAuthCreds converts an AuthCredential to an ai.OAuthCredentials.
// The provider-specific Extra map is round-tripped verbatim; the legacy
// ProjectID column (preserved on disk for back-compat with older fir
// builds) is lifted into Extra["projectId"] when the map doesn't
// already carry one.
func AuthCredToOAuthCreds(cred *AuthCredential) *ai.OAuthCredentials {
	c := &ai.OAuthCredentials{
		Access:  cred.Access,
		Refresh: cred.Refresh,
		Expires: cred.Expires,
	}
	if len(cred.Extra) > 0 {
		c.Extra = make(map[string]any, len(cred.Extra))
		for k, v := range cred.Extra {
			c.Extra[k] = v
		}
	}
	if cred.ProjectID != "" {
		if c.Extra == nil {
			c.Extra = make(map[string]any, 1)
		}
		// Don't overwrite an Extra["projectId"] the caller has set
		// — Extra wins over the legacy column.
		if _, ok := c.Extra["projectId"]; !ok {
			c.Extra["projectId"] = cred.ProjectID
		}
	}
	return c
}

// OAuthCredsToAuthCred converts ai.OAuthCredentials to an AuthCredential.
// Extra is persisted verbatim. As a back-compat measure, Extra["projectId"]
// is also mirrored into the dedicated ProjectID column so older fir
// builds reading the auth.json file still find the project.
func OAuthCredsToAuthCred(creds *ai.OAuthCredentials) AuthCredential {
	ac := AuthCredential{
		Type:    CredentialTypeOAuth,
		Access:  creds.Access,
		Refresh: creds.Refresh,
		Expires: creds.Expires,
	}
	if len(creds.Extra) > 0 {
		ac.Extra = make(map[string]any, len(creds.Extra))
		for k, v := range creds.Extra {
			ac.Extra[k] = v
		}
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
func (s *AuthStorage) Login(ctx context.Context, providerID string, callbacks pinoauth.LoginCallbacks) error {
	provider := ai.GetOAuthProvider(providerID)
	if provider == nil {
		return fmt.Errorf("unknown OAuth provider: %s", providerID)
	}

	creds, err := provider.Login(ctx, callbacks)
	if err != nil {
		return err
	}

	return s.Set(providerID, OAuthCredsToAuthCred(creds))
}

// Logout removes credentials for a provider.
func (s *AuthStorage) Logout(provider string) error {
	return s.Remove(provider)
}

// GetOAuthProviders returns all registered OAuth providers.
func (s *AuthStorage) GetOAuthProviders() []ai.OAuthProvider {
	return ai.GetOAuthProviders()
}
