package models

import (
	"strings"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/ai/oauth"
	firlog "github.com/kfet/fir/pkg/log"
)

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
	// Fast path: if cache already has this provider, return a copy quickly.
	r.synthMu.Lock()
	src := r.synthesisedSiblings[provider]
	r.synthMu.Unlock()
	if len(src) > 0 {
		out := make([]*ai.Model, len(src))
		copy(out, src)
		return out
	}
	if src != nil {
		return nil
	}

	// Snapshot r.models first under r.mu to avoid lock-order inversion with
	// refresh, which acquires r.mu then synthMu.
	r.mu.RLock()
	models := make([]*ai.Model, len(r.models))
	copy(models, r.models)
	r.mu.RUnlock()

	r.synthMu.Lock()
	defer r.synthMu.Unlock()
	if r.synthesisedSiblings == nil {
		r.synthesisedSiblings = make(map[string][]*ai.Model)
		for _, m := range models {
			r.synthesisedSiblings[m.Provider] = append(r.synthesisedSiblings[m.Provider], m)
		}
	}
	// Return a defensive copy so callers can iterate without holding synthMu.
	src = r.synthesisedSiblings[provider]
	if len(src) == 0 {
		return nil
	}
	out := make([]*ai.Model, len(src))
	copy(out, src)
	return out
}

// synthesise produces a *ai.Model for a single (provider, modelID) pair using
// (in order): provider-specific defaulter, generic sibling-clone fallback.
// Cached. Safe to call concurrently.
func (r *ModelRegistry) synthesise(provider, id string) *ai.Model {
	key := provider + "\x00" + id

	r.synthMu.Lock()
	if m, ok := r.synthesised[key]; ok {
		r.synthMu.Unlock()
		return m
	}
	r.synthMu.Unlock()

	siblings := r.siblingsFor(provider)
	m := runSynthesisPipeline(provider, id, siblings)

	r.synthMu.Lock()
	if r.synthesised == nil {
		r.synthesised = make(map[string]*ai.Model)
	}
	r.synthesised[key] = m
	r.synthMu.Unlock()

	if m != nil {
		firlog.Debug("synthesised metadata for live-only model %s/%s", provider, id)
	}
	return m
}

// runSynthesisPipeline resolves metadata for an unknown model ID using
// provider-specific defaulters, falling back to a sibling-clone heuristic.
func runSynthesisPipeline(provider, id string, siblings []*ai.Model) *ai.Model {
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
	return synthesiseFromSibling(id, siblings)
}

// synthesiseForLiveIDs produces *ai.Model entries for a batch of live-listed
// IDs that aren't already in the built-in registry. Used by the live-list
// fetch path. Safe to call without holding any locks.
func (r *ModelRegistry) synthesiseForLiveIDs(provider string, ids []string) []*ai.Model {
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
		if m := r.synthesise(provider, id); m != nil {
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
func synthesiseFromSibling(modelID string, siblings []*ai.Model) *ai.Model {
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
