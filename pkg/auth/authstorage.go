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
	"strconv"
	"strings"
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
	// CredentialTypeAWSIAM is an Amazon Bedrock account that authenticates via
	// AWS SigV4 — either a named ~/.aws profile or explicit static keys, plus a
	// region. The configuration lives in AuthCredential.Extra (see
	// bedrockIAMFromExtra); GetApiKey resolves it into a prefixed JSON envelope
	// (ai.EncodeBedrockIAMCreds) that the Bedrock provider decodes.
	CredentialTypeAWSIAM CredentialType = "aws_iam"
)

// AuthCredential represents a stored credential.
type AuthCredential struct {
	Type CredentialType `json:"type"`

	// Label is a human-readable name for this account (e.g. the account
	// email or profile name). Used to distinguish multiple accounts of the
	// same provider in selectors. Empty for legacy/default credentials.
	Label string `json:"label,omitempty"`

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

// externallyMutableBackend is implemented by backends whose contents can change
// behind this process's back — i.e. the file backend, which another fir process
// (or `fir auth refresh` from cron) can rewrite at any moment. Stamp returns a
// cheap value that changes whenever the stored bytes might have; AuthStorage
// uses it to notice a rotation without re-reading the file on every lookup.
//
// Backends that cannot be mutated externally (the in-memory one) simply don't
// implement this, and the staleness check becomes a no-op.
type externallyMutableBackend interface {
	Stamp() string
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

// Stamp returns a cheap fingerprint of the auth file's current state
// (modification time plus size). It is deliberately a stat, not a read: the
// caller uses it to decide whether a full locked read is warranted.
//
// A missing or unstattable file yields a constant, so a not-yet-created
// auth.json doesn't look like it's changing on every check.
func (b *FileAuthStorageBackend) Stamp() string {
	fi, err := os.Stat(b.authPath)
	if err != nil {
		return "absent"
	}
	return strconv.FormatInt(fi.ModTime().UnixNano(), 10) + ":" + strconv.FormatInt(fi.Size(), 10)
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
	audit            *auditWriter     // append-only mutation log; nil for in-memory
	// lastStamp is the backend fingerprint (see externallyMutableBackend) as
	// of the last load. Used by reloadIfStale to detect an out-of-process
	// rewrite of auth.json — e.g. `fir auth refresh` rotating tokens from
	// cron while this session is live.
	lastStamp string
}

// NewAuthStorage creates an AuthStorage backed by the given file path.
// authPath must be non-empty.
func NewAuthStorage(authPath string) *AuthStorage {
	s := newAuthStorage(NewFileAuthStorageBackend(authPath))
	s.audit = newAuditWriter(authPath)
	return s
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
	data, _ := decodeStorageData(content)
	return data
}

// decodeStorageData decodes a storage blob, reporting an error when content is
// present but unparseable. That distinction matters to Reload: "no
// credentials" and "unreadable credentials" must not be handled the same way.
func decodeStorageData(content []byte) (AuthStorageData, error) {
	if len(content) == 0 {
		return make(AuthStorageData), nil
	}
	var parsed AuthStorageData
	if err := json.Unmarshal(content, &parsed); err != nil {
		return make(AuthStorageData), fmt.Errorf("parse auth storage: %w", err)
	}
	if parsed == nil { // literal JSON null
		parsed = make(AuthStorageData)
	}
	return parsed, nil
}

// Reload re-reads credentials from storage.
//
// A corrupt or truncated file leaves the in-memory credentials untouched. This
// runs mid-session now — `/mcp reload` re-reads the store to pick up a token
// minted by another process — and swapping in an empty map on a bad read would
// log the session out of every provider at once. The error is recorded (so
// DrainErrors surfaces it) but loadError is left clear, so a subsequent login
// can still overwrite the damaged file.
func (s *AuthStorage) Reload() {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Stamp BEFORE reading. If the file changes between the stat and the
	// locked read we record the older stamp, so the next staleness check
	// re-reads redundantly rather than missing an update — the safe direction.
	stamp := s.backendStamp()
	var parseErr error
	_, err := s.storage.WithLock(func(current []byte) (any, []byte) {
		data, perr := decodeStorageData(current)
		if perr != nil {
			parseErr = perr
			return nil, nil
		}
		s.data = data
		return nil, nil
	})
	if err != nil {
		s.loadError = err
		s.recordError(err)
		return
	}
	s.lastStamp = stamp
	s.loadError = nil
	if parseErr != nil {
		s.recordError(parseErr)
	}
}

// backendStamp returns the backend's change fingerprint, or "" for backends
// that cannot be mutated externally. Callers must hold s.mu.
func (s *AuthStorage) backendStamp() string {
	if b, ok := s.storage.(externallyMutableBackend); ok {
		return b.Stamp()
	}
	return ""
}

// reloadIfStale re-reads auth.json when it has changed on disk since this
// AuthStorage last loaded it.
//
// This is what keeps a long-running session honest about credential rotation.
// OAuth refresh tokens rotate on every grant, and at least one provider
// (Anthropic) REVOKES the previous access token the instant the rotation
// happens — so a session holding a pre-rotation access token in memory is
// already dead, even though its cached `expires` is hours in the future and
// auth.json on disk is perfectly healthy. Without this check the session would
// 401 forever, because nothing in the expiry-driven refresh path ever looks at
// the file again. See RefreshApiKey for the post-401 belt-and-braces.
func (s *AuthStorage) reloadIfStale() {
	s.mu.RLock()
	last := s.lastStamp
	stamp := s.backendStamp()
	s.mu.RUnlock()
	if stamp == "" || stamp == last {
		return
	}
	s.Reload()
}

// RefreshApiKey re-reads auth.json from disk and then resolves the provider's
// API key. Callers use it when the key they were holding was REJECTED — an
// HTTP 401/403 from the provider — because the most likely cause is not an
// expired credential but a rotated one: another fir process, or `fir auth
// refresh` running from cron, performed a refresh grant, and the provider
// revoked the access token this session still holds.
//
// The re-read is unconditional rather than stamp-gated: a filesystem with
// coarse mtime granularity must not be what decides whether a live session
// recovers. If the key comes back unchanged, the credential really is bad and
// the caller should surface the error — deliberately NOT spending a refresh
// grant here, which on a genuinely dead credential would just produce a storm
// of failing grants.
func (s *AuthStorage) RefreshApiKey(provider string) string {
	s.Reload()
	return s.GetApiKey(provider)
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
		return
	}
	// We just wrote the file — adopt the new stamp so the next staleness
	// check doesn't re-read our own write.
	s.lastStamp = s.backendStamp()
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
	s.audit.record(AuditActionSet, provider, &cred, len(s.data))
	return nil
}

// Remove deletes a credential for a provider.
func (s *AuthStorage) Remove(provider string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, existed := s.data[provider]
	delete(s.data, provider)
	s.persistProviderChange(provider, nil)
	if existed {
		s.audit.record(AuditActionRemove, provider, &prev, len(s.data))
	} else {
		s.audit.record(AuditActionRemove, provider, nil, len(s.data))
	}
	return nil
}

// List returns all providers with stored credentials. MCP server credentials
// (see MCPKeyPrefix) are excluded — they are not provider accounts.
func (s *AuthStorage) List() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]string, 0, len(s.data))
	for k := range s.data {
		if IsMCPKey(k) {
			continue
		}
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
	// Pick up an out-of-process credential rotation before resolving. Cheap
	// (a stat) in the common case where nothing changed.
	s.reloadIfStale()

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
		if cred.Type == CredentialTypeAWSIAM {
			s.mu.RUnlock()
			return ai.EncodeBedrockIAMCreds(bedrockIAMFromExtra(cred.Extra))
		}
		if cred.Type == CredentialTypeOAuth && cred.Access != "" {
			oauthProvider := oauthProviderForSlot(provider)
			if oauthProvider == nil {
				// OAuth extension not loaded — token can't be refreshed and
				// the provider won't know to send it as Bearer auth. Return
				// empty so the caller gets a clear "no API key" error instead
				// of a confusing auth failure with x-api-key. Record a
				// specific error so the surfaced message names the real cause
				// (provider/extension not loaded) rather than the generic
				// "credentials may have expired".
				s.mu.RUnlock()
				base, _ := SplitSlot(provider)
				s.mu.Lock()
				s.lastKeyErrors[provider] = fmt.Errorf(
					"OAuth provider %q is not loaded — its auth extension failed to register; reload extensions or restart fir", base)
				s.mu.Unlock()
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

// refreshOAuthToken handles the mid-turn token refresh triggered from
// GetApiKey when a stored access token has expired. The rotate-and-persist
// mechanics live in refreshOAuthLocked (see there for the single-writer
// argument); this wrapper only supplies the "expired right now" policy and the
// GetApiKey-shaped return value.
func (s *AuthStorage) refreshOAuthToken(provider string, oauthProvider ai.OAuthProvider) string {
	// TODO(pinoauth-migration): RefreshToken takes a context, but the
	// synchronous GetApiKey path has no natural ctx to thread. Using
	// Background until/unless GetApiKey grows a ctx parameter.
	out, err := s.refreshOAuthLocked(context.Background(), provider, oauthProvider, func(cred AuthCredential) bool {
		// Refresh only if still expired as read under the lock — a
		// credential another process just rotated is usable as-is.
		return credentialNeedsRefresh(cred, 0)
	})
	if err == nil {
		return out.apiKey
	}

	// Refresh failed — re-read to check if another process succeeded.
	s.Reload()
	s.mu.RLock()
	cred, ok := s.data[provider]
	s.mu.RUnlock()
	if ok && cred.Type == CredentialTypeOAuth && cred.Expires > 0 && time.Now().UnixMilli() < cred.Expires {
		s.mu.Lock()
		delete(s.lastKeyErrors, provider)
		s.mu.Unlock()
		return oauthProvider.GetAPIKey(AuthCredToOAuthCreds(&cred))
	}
	// Token still expired after re-read — another process didn't help.
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

// Login performs OAuth login for the given provider and stores credentials
// under the appropriate account slot. See LoginAccount.
func (s *AuthStorage) Login(ctx context.Context, providerID string, callbacks pinoauth.LoginCallbacks) error {
	_, _, err := s.LoginAccount(ctx, providerID, callbacks)
	return err
}

// LoginAccount runs the OAuth login flow for a provider and stores the result
// as a typed account, returning the slot key it was stored under and the
// account's display label.
//
// Slot assignment:
//   - If the provider has no default account yet, the credential is stored
//     under the bare provider key (the default account).
//   - If an existing account (default or named) has the same account identity
//     (derived from the credential's profile), that account is overwritten —
//     re-logging in refreshes it in place.
//   - Otherwise a new named slot "<provider>#<accountId>" is created, so a
//     second login ADDS an account rather than evicting the first.
func (s *AuthStorage) LoginAccount(ctx context.Context, providerID string, callbacks pinoauth.LoginCallbacks) (slotKey, label string, err error) {
	provider := ai.GetOAuthProvider(providerID)
	if provider == nil {
		return "", "", fmt.Errorf("unknown OAuth provider: %s", providerID)
	}

	creds, err := provider.Login(ctx, callbacks)
	if err != nil {
		return "", "", err
	}

	cred := OAuthCredsToAuthCred(creds)
	cred.Label = labelFromCreds(creds)
	slot := s.assignSlot(providerID, creds)
	if err := s.Set(slot, cred); err != nil {
		return "", "", err
	}
	return slot, cred.Label, nil
}

// accountIDFromCreds derives a stable account identity from an OAuth
// credential's Extra map (set by the provider's post_exchange hook). Prefers
// an explicit accountId, then email/label. Returns "" when none is available.
func accountIDFromCreds(creds *ai.OAuthCredentials) string {
	if creds == nil || creds.Extra == nil {
		return ""
	}
	for _, k := range []string{"accountId", "account_id", "email", "label"} {
		if v, ok := creds.Extra[k].(string); ok && v != "" {
			return sanitizeAccountID(v)
		}
	}
	return ""
}

// labelFromCreds derives a human label (email/profile) from Extra.
func labelFromCreds(creds *ai.OAuthCredentials) string {
	if creds == nil || creds.Extra == nil {
		return ""
	}
	for _, k := range []string{"label", "email", "accountId", "account_id"} {
		if v, ok := creds.Extra[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// sanitizeAccountID makes a value safe to embed in a slot key (no '#').
func sanitizeAccountID(v string) string {
	return strings.ReplaceAll(v, slotSep, "_")
}

// assignSlot decides which storage slot a freshly-logged-in credential goes
// to. See LoginAccount for the policy.
func (s *AuthStorage) assignSlot(providerID string, creds *ai.OAuthCredentials) string {
	acctID := accountIDFromCreds(creds)
	existing := s.AccountsForProvider(providerID)

	// No default yet -> claim the default (bare) slot.
	hasDefault := false
	for _, a := range existing {
		if a.AccountID == "" {
			hasDefault = true
			break
		}
	}
	if !hasDefault {
		return providerID
	}

	// Same identity as an existing account -> overwrite it in place.
	if acctID != "" {
		for _, a := range existing {
			cred := s.Get(a.SlotKey)
			if cred == nil {
				continue
			}
			if accountIDFromCreds(AuthCredToOAuthCreds(cred)) == acctID {
				return a.SlotKey
			}
		}
		return SlotKey(providerID, acctID)
	}

	// No stable identity available -> allocate a sequential named slot.
	for i := 2; ; i++ {
		candidate := SlotKey(providerID, fmt.Sprintf("account%d", i))
		taken := false
		for _, a := range existing {
			if a.SlotKey == candidate {
				taken = true
				break
			}
		}
		if !taken {
			return candidate
		}
	}
}

// Logout removes credentials for a provider.
func (s *AuthStorage) Logout(provider string) error {
	return s.Remove(provider)
}

// StoreAccount stores a non-OAuth typed account (e.g. a Bedrock aws_iam or
// bearer credential) under the appropriate slot and returns the slot key.
//
// Slot policy: the first account for a provider claims the bare default slot; a
// re-store under the same named-account slot overwrites it in place; otherwise
// a new named slot "<provider>#<name>" is created. (A name matching the default
// account's label creates a new named slot rather than overwriting the default
// — non-OAuth accounts have no stable identity to dedupe the default on.)
// accountName is also recorded as the credential Label.
func (s *AuthStorage) StoreAccount(provider, accountName string, cred AuthCredential) (string, error) {
	cred.Label = accountName
	existing := s.AccountsForProvider(provider)

	hasDefault := false
	for _, a := range existing {
		if a.AccountID == "" {
			hasDefault = true
			break
		}
	}
	if !hasDefault {
		return provider, s.Set(provider, cred)
	}

	id := sanitizeAccountID(accountName)
	if id == "" {
		// No name and the default is taken — allocate a sequential slot.
		for i := 2; ; i++ {
			cand := SlotKey(provider, fmt.Sprintf("account%d", i))
			taken := false
			for _, a := range existing {
				if a.SlotKey == cand {
					taken = true
					break
				}
			}
			if !taken {
				id = fmt.Sprintf("account%d", i)
				break
			}
		}
	}
	slot := SlotKey(provider, id)
	return slot, s.Set(slot, cred)
}

// GetOAuthProviders returns OAuth providers for use by the model registry and
// selectors, fanning each base provider out into one entry per stored account.
// The base provider itself always appears (covering the default slot and
// enabling login when nothing is stored yet); each additional named account
// appears as a delegating accountProvider with a composite ID.
func (s *AuthStorage) GetOAuthProviders() []ai.OAuthProvider {
	bases := ai.GetOAuthProviders()
	out := make([]ai.OAuthProvider, 0, len(bases))
	for _, base := range bases {
		out = append(out, base)
		for _, acct := range s.AccountsForProvider(base.ID()) {
			if acct.AccountID == "" {
				continue // default account is covered by the base provider
			}
			out = append(out, &accountProvider{
				base:    base,
				slotKey: acct.SlotKey,
				label:   acct.DisplayName(),
			})
		}
	}
	return out
}
