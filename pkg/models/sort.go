// Sort logic shared by ACP and the interactive TUI model selector.
package models

import (
	"sort"

	"github.com/kfet/fir/pkg/ai"
)

// IsFreeModel reports whether m is genuinely free per-call.
//
// Only Poe models with zero cost across all four axes (Input, Output,
// CacheRead, CacheWrite) qualify. Other zero-cost entries in the registry
// (github-copilot, openai-codex, opencode, ...) are gated behind
// subscription or OAuth plans and must not be promoted above paid API
// models or advertised as "FREE".
func IsFreeModel(m *ai.Model) bool {
	if m == nil || m.Provider != ai.ProviderPoe {
		return false
	}
	c := m.Cost
	return c.Input == 0 && c.Output == 0 && c.CacheRead == 0 && c.CacheWrite == 0
}

// SortModels returns a new slice sorted by:
//
//  1. Pinned current model first (when currentModel is non-nil).
//  2. Provider rank from OrderedProviders(); unknown providers go last,
//     ordered alphabetically by provider ID among themselves.
//  3. SWE-bench Verified score descending (unscored last).
//  4. Same provider + display name: free variant first (Poe exposes the
//     same underlying model as paid and free bots).
//  5. Free models before paid (general tiebreaker).
//  6. Model ID alphabetic.
//
// The sort is stable so equal entries keep their input order.
func SortModels(in []*ai.Model, currentModel *ai.Model) []*ai.Model {
	order := OrderedProviders()
	rank := make(map[ai.Provider]int, len(order))
	for i, p := range order {
		rank[p] = i
	}
	unknown := len(order)

	out := make([]*ai.Model, len(in))
	copy(out, in)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]

		if currentModel != nil {
			aCur := ai.ModelsAreEqual(currentModel, a)
			bCur := ai.ModelsAreEqual(currentModel, b)
			if aCur != bCur {
				return aCur
			}
		}

		ar, aok := rank[a.Provider]
		br, bok := rank[b.Provider]
		if !aok {
			ar = unknown
		}
		if !bok {
			br = unknown
		}
		if ar != br {
			return ar < br
		}
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}

		if a.SWEScore != b.SWEScore {
			return a.SWEScore > b.SWEScore
		}

		aFree := IsFreeModel(a)
		bFree := IsFreeModel(b)
		if a.Name == b.Name && aFree != bFree {
			return aFree
		}
		if aFree != bFree {
			return aFree
		}
		return a.ID < b.ID
	})
	return out
}
