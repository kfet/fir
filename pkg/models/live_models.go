package models

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	firlog "github.com/kfet/fir/pkg/log"
)

// liveModelCache is the on-disk cache format for a provider's live model list.
type liveModelCache struct {
	Provider  string          `json:"provider"`
	Models    []LiveModelInfo `json:"models"`
	FetchedAt time.Time      `json:"fetchedAt"`
}

const liveCacheTTL = 1 * time.Hour

// liveModelState tracks the background-fetched models for a single provider.
type liveModelState struct {
	mu       sync.RWMutex
	modelIDs map[string]bool // set of model IDs available from the API
	fetched  bool            // true once the first fetch completes (success or fail)
	err      error           // last fetch error, if any
}

func (s *liveModelState) set(ids []LiveModelInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modelIDs = make(map[string]bool, len(ids))
	for _, m := range ids {
		s.modelIDs[m.ID] = true
	}
	s.fetched = true
	s.err = nil
}

func (s *liveModelState) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetched = true
	s.err = err
}

func (s *liveModelState) has(modelID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.fetched || s.err != nil || s.modelIDs == nil {
		return true // not yet fetched, error, or no data: be permissive
	}
	return s.modelIDs[modelID]
}

// StartLiveModelFetch kicks off background goroutines to fetch live model lists
// from provider APIs. It populates liveModels which GetAvailable uses to filter.
// cacheDir is where disk caches are stored (e.g. ~/.config/fir/cache/).
func (r *ModelRegistry) StartLiveModelFetch(ctx context.Context, cacheDir string) {
	r.mu.RLock()
	providers := r.getProvidersWithAuth()
	r.mu.RUnlock()

	for _, provider := range providers {
		lister := GetModelLister(provider)
		if lister == nil {
			continue
		}

		state := &liveModelState{}
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

	models, err := lister.ListModels(ctx, baseURL, apiKey)
	if err != nil {
		firlog.Debug("live model list failed for %s: %v", provider, err)
		state.setError(err)
		return
	}

	state.set(models)
	r.saveLiveCache(cacheDir, provider, models)
	firlog.Debug("live model list for %s: %d models", provider, len(models))
}

func liveCachePath(cacheDir, provider string) string {
	return filepath.Join(cacheDir, "live-models-"+provider+".json")
}

func (r *ModelRegistry) loadLiveCache(cacheDir, provider string) ([]LiveModelInfo, bool) {
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

func (r *ModelRegistry) saveLiveCache(cacheDir, provider string, models []LiveModelInfo) {
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
