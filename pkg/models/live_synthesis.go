package models

import (
	"context"
	"strings"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/oauth"
	firlog "github.com/kfet/fir/pkg/log"
)

// MetadataEnricher fills in metadata for a model ID that was returned by a
// provider's live-list but not present in the built-in registry.
//
// This is the lowest-priority hook — providers should generally implement
// ModelDefaults on their oauth.Provider or ModelLister directly. The enricher
// is reserved for callers (e.g. extensions) that want to plug in an LLM-based
// fallback for novel naming schemes.
type MetadataEnricher interface {
	Enrich(ctx context.Context, provider, modelID string, siblings []*ai.Model) *ai.Model
}

// SetMetadataEnricher installs an enricher used when synthesising live-only
// model metadata. Pass nil to disable. Resolution order during synthesis is:
//  1. provider-specific ModelDefaults (oauth.Provider or ModelLister)
//  2. this enricher (if set)
//  3. generic sibling-clone heuristic
func (r *ModelRegistry) SetMetadataEnricher(e MetadataEnricher) {
	r.synthMu.Lock()
	defer r.synthMu.Unlock()
	r.enricher = e
	r.synthesised = nil
	r.synthesisedSiblings = nil
}

// invalidateSynthesisCache drops cached synthesised models. Called after a
// registry refresh so stale entries based on prior built-in models are cleared.
func (r *ModelRegistry) invalidateSynthesisCache() {
	r.synthMu.Lock()
	defer r.synthMu.Unlock()
	r.synthesised = nil
	r.synthesisedSiblings = nil
}

// siblingsFor returns built-in siblings for a provider, building & caching the
// per-provider index lazily under synthMu.
func (r *ModelRegistry) siblingsFor(provider string) []*ai.Model {
	r.synthMu.Lock()
	defer r.synthMu.Unlock()
	if r.synthesisedSiblings == nil {
		r.synthesisedSiblings = make(map[string][]*ai.Model)
		// Snapshot r.models under r.mu — caller may already hold it for read
		// (Find), so use a separate read here. Note: we accept the small race
		// window where r.models is replaced concurrently; in that case the
		// next Refresh -> invalidateSynthesisCache will clear stale entries.
		r.mu.RLock()
		for _, m := range r.models {
			r.synthesisedSiblings[m.Provider] = append(r.synthesisedSiblings[m.Provider], m)
		}
		r.mu.RUnlock()
	}
	// Return a defensive copy so callers can iterate without holding synthMu.
	src := r.synthesisedSiblings[provider]
	if len(src) == 0 {
		return nil
	}
	out := make([]*ai.Model, len(src))
	copy(out, src)
	return out
}

// lookupSynthesised returns a cached synthesised model. ok=false means
// "no entry"; ok=true with m=nil means "previously failed, don't retry".
func (r *ModelRegistry) lookupSynthesised(provider, id string) (m *ai.Model, ok bool) {
	r.synthMu.Lock()
	defer r.synthMu.Unlock()
	if r.synthesised == nil {
		return nil, false
	}
	m, ok = r.synthesised[provider+"\x00"+id]
	return
}

// storeSynthesised caches a synthesised model (or nil for "tried and failed").
func (r *ModelRegistry) storeSynthesised(provider, id string, m *ai.Model) {
	r.synthMu.Lock()
	defer r.synthMu.Unlock()
	if r.synthesised == nil {
		r.synthesised = make(map[string]*ai.Model)
	}
	r.synthesised[provider+"\x00"+id] = m
}

// synthesise produces a *ai.Model for a single (provider, modelID) pair using
// (in order): provider-specific defaulter, registered enricher, generic
// sibling-clone fallback. Cached. Safe to call concurrently. Does not require
// any caller-held locks.
func (r *ModelRegistry) synthesise(ctx context.Context, provider, id string) *ai.Model {
	if cached, ok := r.lookupSynthesised(provider, id); ok {
		return cached
	}
	siblings := r.siblingsFor(provider)
	m := r.runSynthesisPipeline(ctx, provider, id, siblings)
	r.storeSynthesised(provider, id, m)
	if m != nil {
		firlog.Debug("synthesised metadata for live-only model %s/%s", provider, id)
	}
	return m
}

// runSynthesisPipeline executes the resolution order without touching caches.
func (r *ModelRegistry) runSynthesisPipeline(ctx context.Context, provider, id string, siblings []*ai.Model) *ai.Model {
	r.synthMu.Lock()
	enricher := r.enricher
	r.synthMu.Unlock()

	// 1. provider-specific defaulter (oauth.Provider or ModelLister).
	if oauthProv := oauth.GetProvider(provider); oauthProv != nil {
		if d, ok := oauthProv.(oauth.ModelDefaulter); ok {
			if m := d.ModelDefaults(id, siblings); m != nil {
				m.Provider = provider
				m.ID = id
				return m
			}
		}
	}
	if lister := GetModelLister(provider); lister != nil {
		if d, ok := lister.(ListerModelDefaulter); ok {
			if m := d.ModelDefaults(provider, id, siblings); m != nil {
				m.Provider = provider
				m.ID = id
				return m
			}
		}
	}
	// 2. registered enricher.
	if enricher != nil {
		if m := enricher.Enrich(ctx, provider, id, siblings); m != nil {
			m.Provider = provider
			m.ID = id
			return m
		}
	}
	// 3. generic sibling-clone fallback.
	if m := synthesiseFromSibling(provider, id, siblings); m != nil {
		return m
	}
	return nil
}

// synthesiseForLiveIDs produces *ai.Model entries for a batch of live-listed
// IDs that aren't already in the built-in registry. Used by the live-list
// fetch path. Safe to call without holding any locks.
func (r *ModelRegistry) synthesiseForLiveIDs(ctx context.Context, provider string, ids []string) []*ai.Model {
	if len(ids) == 0 {
		return nil
	}
	// Build a built-in ID set once so we can skip in O(1).
	r.mu.RLock()
	builtIn := make(map[string]bool, len(r.models))
	for _, m := range r.models {
		if m.Provider == provider {
			builtIn[m.ID] = true
		}
	}
	r.mu.RUnlock()

	var out []*ai.Model
	for _, id := range ids {
		if builtIn[id] {
			continue
		}
		if m := r.synthesise(ctx, provider, id); m != nil {
			out = append(out, m)
		}
	}
	return out
}

// synthesiseFromSibling produces a best-effort *ai.Model for an unknown ID by
// cloning the sibling whose ID shares the longest common prefix.
//
// The clone is a shallow copy: Headers, Input, ServerTools, and Compat are
// shared with the sibling. Treat the result as read-only — mutating its
// reference fields will corrupt the built-in model.
func synthesiseFromSibling(provider, modelID string, siblings []*ai.Model) *ai.Model {
	if len(siblings) == 0 {
		return nil
	}
	best := siblings[0]
	bestScore := commonPrefixLen(best.ID, modelID)
	for _, s := range siblings[1:] {
		if n := commonPrefixLen(s.ID, modelID); n > bestScore {
			best, bestScore = s, n
		}
	}
	out := *best
	out.ID = modelID
	out.Name = humaniseID(modelID)
	out.SWEInferred = true
	return &out
}

func commonPrefixLen(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// humaniseID turns "claude-sonnet-4-7-20260601" into "Claude Sonnet 4 7 20260601".
func humaniseID(id string) string {
	parts := strings.FieldsFunc(id, func(r rune) bool { return r == '-' || r == '_' || r == '/' || r == ':' })
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}
