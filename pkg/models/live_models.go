package models

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/oauth"
	"github.com/kfet/fir/pkg/auth"
	firlog "github.com/kfet/fir/pkg/log"
)

// liveModelCache is the on-disk cache format for a provider's live model list.
// V2 stores fully-synthesised model metadata so cold-start Find() can resolve
// previously-seen IDs without waiting for a fresh fetch. The filename embeds
// the version (live-models-v2-<provider>.json) so v1 caches from older fir
// builds are ignored rather than silently mis-decoded into half-empty Models.
type liveModelCache struct {
	Provider  string      `json:"provider"`
	Models    []*ai.Model `json:"models"` // synthesised metadata, not just IDs
	FetchedAt time.Time   `json:"fetchedAt"`
}

const liveCacheTTL = 1 * time.Hour

// liveModelState tracks the background-fetched models for a single provider.
type liveModelState struct {
	mu      sync.RWMutex
	models  []*ai.Model   // fully-synthesised models
	fetched bool          // true once the first fetch completes (success or fail)
	err     error         // last fetch error, if any
	done    chan struct{} // closed when fetch completes
}

func newLiveModelState() *liveModelState {
	return &liveModelState{done: make(chan struct{})}
}

func (s *liveModelState) set(models []*ai.Model) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if models == nil {
		s.models = make([]*ai.Model, 0)
	} else {
		s.models = models
	}
	if !s.fetched {
		s.fetched = true
		close(s.done)
	}
	s.err = nil
}

func (s *liveModelState) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.fetched {
		s.fetched = true
		close(s.done)
	}
	s.err = err
}

func (s *liveModelState) has(modelID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.fetched || s.err != nil || s.models == nil {
		return true // not yet fetched, error, or no data: be permissive
	}
	for _, m := range s.models {
		if m.ID == modelID {
			return true
		}
	}
	return false
}

// get returns a synthesised model by ID, or nil if not found.
func (s *liveModelState) get(modelID string) *ai.Model {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, m := range s.models {
		if m.ID == modelID {
			return m
		}
	}
	return nil
}

// hasData reports whether the fetch produced a usable (possibly empty) list.
// Returns false if the fetch hasn't completed, errored, or the list is nil.
func (s *liveModelState) hasData() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fetched && s.err == nil && s.models != nil
}

// StartLiveModelFetch kicks off background goroutines to fetch live model lists
// from provider APIs. It populates liveModels which GetAvailable uses to filter.
// cacheDir is where disk caches are stored (e.g. ~/.config/fir/cache/).
// extReady, if non-nil, is waited on before fetching OAuth provider models
// (extension-based auth providers are not registered until extensions load).
func (r *ModelRegistry) StartLiveModelFetch(ctx context.Context, cacheDir string, extReady <-chan struct{}) {
	r.liveModelsMu.Lock()
	r.liveCacheDir = cacheDir
	r.liveExtReady = extReady
	r.liveModelsMu.Unlock()

	r.mu.RLock()
	providers := r.getProvidersWithAuth()
	r.mu.RUnlock()

	// API-key providers: use the ModelLister interface.
	for _, provider := range providers {
		lister := GetModelLister(provider)
		if lister == nil {
			continue
		}

		state := newLiveModelState()
		r.liveModelsMu.Lock()
		r.liveModels[provider] = state
		r.liveModelsMu.Unlock()

		// Try loading from disk cache first
		if cached, ok := r.loadLiveCache(cacheDir, provider); ok {
			state.set(cached)
			// Still refresh in background but don't block
		}

		go r.fetchLiveModels(ctx, provider, lister, state, cacheDir)
	}

	// OAuth providers: wait for extensions to register, then fetch.
	go r.startOAuthModelFetch(ctx, cacheDir, extReady)
}

// startOAuthModelFetch waits for extensions to be ready, then fetches live
// model lists for all OAuth providers.
func (r *ModelRegistry) startOAuthModelFetch(ctx context.Context, cacheDir string, extReady <-chan struct{}) {
	if extReady != nil {
		select {
		case <-extReady:
		case <-ctx.Done():
			return
		}
	}

	// OAuth providers: use oauth.Provider.ListModels.
	for _, oauthProvider := range r.authStorage.GetOAuthProviders() {
		providerID := oauthProvider.ID()
		if !r.authStorage.HasAuth(providerID) {
			continue
		}

		// Skip if the API-key path already registered a state for this provider.
		r.liveModelsMu.RLock()
		_, already := r.liveModels[providerID]
		r.liveModelsMu.RUnlock()
		if already {
			continue
		}

		cred := r.authStorage.Get(providerID)
		if cred == nil || cred.Type != auth.CredentialTypeOAuth {
			continue
		}
		creds := auth.AuthCredToOAuthCreds(cred)

		state := newLiveModelState()
		r.liveModelsMu.Lock()
		r.liveModels[providerID] = state
		r.liveModelsMu.Unlock()

		if cached, ok := r.loadLiveCache(cacheDir, providerID); ok {
			state.set(cached)
		}

		go r.fetchOAuthModels(ctx, providerID, oauthProvider, creds, state, cacheDir)
	}
}

func (r *ModelRegistry) fetchOAuthModels(ctx context.Context, providerID string, provider oauth.Provider, creds *oauth.Credentials, state *liveModelState, cacheDir string) {
	// Ensure the token is fresh — GetApiKey auto-refreshes expired OAuth tokens.
	_ = r.authStorage.GetApiKey(providerID)
	// Re-read credentials after potential refresh.
	if freshCred := r.authStorage.Get(providerID); freshCred != nil && freshCred.Type == auth.CredentialTypeOAuth {
		creds = auth.AuthCredToOAuthCreds(freshCred)
	}

	ids, err := provider.ListModels(ctx, creds)
	if err != nil {
		firlog.Debug("live model list failed for OAuth provider %s: %v", providerID, err)
		state.setError(err)
		return
	}
	if ids == nil {
		// Provider signalled it doesn't support listing — be permissive.
		state.setError(nil)
		return
	}

	// Synthesise metadata for returned IDs using built-in siblings.
	models := r.synthesiseForLiveIDs(providerID, ids)
	state.set(models)
	r.saveLiveCache(cacheDir, providerID, models)
	firlog.Debug("live model list for OAuth provider %s: %d models", providerID, len(models))
}

func (r *ModelRegistry) getProvidersWithAuth() []string {
	seen := make(map[string]bool)
	var providers []string
	for _, m := range r.models {
		if !seen[m.Provider] && r.authStorage.HasAuth(m.Provider) {
			seen[m.Provider] = true
			providers = append(providers, m.Provider)
		}
	}
	return providers
}

func (r *ModelRegistry) fetchLiveModels(ctx context.Context, provider string, lister ModelLister, state *liveModelState, cacheDir string) {
	// Get baseURL and apiKey from first model of this provider
	r.mu.RLock()
	var baseURL string
	for _, m := range r.models {
		if m.Provider == provider && m.BaseURL != "" {
			baseURL = m.BaseURL
			break
		}
	}
	r.mu.RUnlock()

	if baseURL == "" {
		state.setError(nil) // no baseURL, just mark as done permissively
		return
	}

	apiKey := r.authStorage.GetApiKey(provider)
	if apiKey == "" {
		state.setError(nil)
		return
	}

	// Fetch raw IDs only from provider API
	raw, err := lister.ListModels(ctx, baseURL, apiKey)
	if err != nil {
		firlog.Debug("live model list failed for %s: %v", provider, err)
		state.setError(err)
		return
	}

	// Convert []LiveModelInfo to []string for synthesis
	ids := make([]string, len(raw))
	for i, m := range raw {
		ids[i] = m.ID
	}

	// Resolve full metadata using built-in models and synthesis fallback.
	models := r.synthesiseForLiveIDs(provider, ids)

	state.set(models)
	r.saveLiveCache(cacheDir, provider, models)
	firlog.Debug("live model list for %s: %d models", provider, len(models))
}

func liveCachePath(cacheDir, provider string) string {
	return filepath.Join(cacheDir, "live-models-v2-"+provider+".json")
}

// liveCachePrefixes lists every on-disk cache filename prefix this binary
// recognises. Used by RefreshLive to scrub stale files (including ones from
// previous schema versions, so they never resurrect bad metadata).
var liveCachePrefixes = []string{
	"live-models-v2-",
	"live-models-", // legacy v1 (raw IDs); always treated as stale.
}

func (r *ModelRegistry) loadLiveCache(cacheDir, provider string) ([]*ai.Model, bool) {
	if cacheDir == "" {
		return nil, false
	}
	data, err := os.ReadFile(liveCachePath(cacheDir, provider))
	if err != nil {
		return nil, false
	}
	var cache liveModelCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, false
	}
	if time.Since(cache.FetchedAt) > liveCacheTTL {
		return nil, false
	}
	return cache.Models, true
}

func (r *ModelRegistry) saveLiveCache(cacheDir, provider string, models []*ai.Model) {
	if cacheDir == "" {
		return
	}
	os.MkdirAll(cacheDir, 0o755)
	cache := liveModelCache{
		Provider:  provider,
		Models:    models,
		FetchedAt: time.Now(),
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return
	}
	_ = os.WriteFile(liveCachePath(cacheDir, provider), data, 0o644)
}

// isModelLive checks whether a model is confirmed available by the live API.
// Returns true if: no lister for this provider, fetch not done yet, fetch failed, or model is in the live list.
func (r *ModelRegistry) isModelLive(provider, modelID string) bool {
	r.liveModelsMu.RLock()
	state, ok := r.liveModels[provider]
	r.liveModelsMu.RUnlock()
	if !ok {
		return true // no live data for this provider, be permissive
	}
	return state.has(modelID)
}

// RefreshLive clears any in-memory and on-disk cached live model lists and
// re-triggers background fetches for every provider, using the cacheDir and
// extReady channel remembered from the initial StartLiveModelFetch call.
// Safe to call from a running session; fetches run in the background.
func (r *ModelRegistry) RefreshLive(ctx context.Context) {
	r.liveModelsMu.Lock()
	cacheDir := r.liveCacheDir
	extReady := r.liveExtReady
	// Drop in-memory state so fetchers re-populate fresh entries.
	r.liveModels = make(map[string]*liveModelState)
	r.liveModelsMu.Unlock()

	// Best-effort delete of on-disk caches so a stale <TTL file can't
	// resurrect old data on the next load. Includes legacy prefixes so
	// upgrades clean up old v1 caches the first time RefreshLive runs.
	if cacheDir != "" {
		if entries, err := os.ReadDir(cacheDir); err == nil {
			for _, e := range entries {
				for _, prefix := range liveCachePrefixes {
					if strings.HasPrefix(e.Name(), prefix) {
						_ = os.Remove(filepath.Join(cacheDir, e.Name()))
						break
					}
				}
			}
		}
	}

	r.StartLiveModelFetch(ctx, cacheDir, extReady)
}

// WaitForLiveFetch blocks until all in-flight live model fetches complete
// or the timeout expires. Returns true if all fetches finished in time.
func (r *ModelRegistry) WaitForLiveFetch(timeout time.Duration) bool {
	r.liveModelsMu.RLock()
	states := make([]*liveModelState, 0, len(r.liveModels))
	for _, s := range r.liveModels {
		states = append(states, s)
	}
	r.liveModelsMu.RUnlock()

	if len(states) == 0 {
		return true
	}

	deadline := time.After(timeout)
	for _, s := range states {
		select {
		case <-s.done:
		case <-deadline:
			return false
		}
	}
	return true
}
