// Model ID parsing and model state building for ACP.
// Split from helpers.go.
package acp

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/models"

	acpsdk "github.com/coder/acp-go-sdk"
)

// ParseModelID splits an ACP model ID "provider/modelId" into its components.
func ParseModelID(acpModelID string) (provider, modelID string, err error) {
	idx := strings.Index(acpModelID, "/")
	if idx == -1 {
		return "", "", fmt.Errorf("invalid model ID format: %s. Expected \"provider/modelId\"", acpModelID)
	}
	return acpModelID[:idx], acpModelID[idx+1:], nil
}

// shortProvider abbreviates provider names for display.
func shortProvider(provider string) string {
	m := map[string]string{
		"anthropic": "anth", "openai": "oai", "google": "goog",
		"mistral": "mist", "groq": "groq", "openrouter": "or",
		"bedrock": "bed", "vertex": "vtx", "azure": "az",
		"deepseek": "ds", "xai": "xai",
	}
	if s, ok := m[provider]; ok {
		return s
	}
	return provider
}

// sortAvailableModels sorts a slice of available models grouped by
// provider first (using models.KnownProviderOrder, with unknown
// providers sorted alphabetically after the known set), then within
// each provider by capability:
//  1. Provider rank (knownProviderOrder index, else len(known)).
//  2. Provider name alphabetically (tiebreaker for unknowns).
//  3. Higher SWE-bench Verified score first (unscored last).
//  4. Free models (zero input+output cost) before paid.
//  5. Model ID alphabetically as final tiebreaker.
//
// The sort uses SliceStable so models that are equal on all criteria
// keep their original (registry) order.
func sortAvailableModels(available []*ai.Model) []*ai.Model {
	rank := make(map[ai.Provider]int, len(models.KnownProviderOrder))
	for i, p := range models.KnownProviderOrder {
		rank[p] = i
	}
	unknown := len(models.KnownProviderOrder)

	out := make([]*ai.Model, len(available))
	copy(out, available)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]

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
		if string(a.Provider) != string(b.Provider) {
			return string(a.Provider) < string(b.Provider)
		}

		if a.SWEScore != b.SWEScore {
			return a.SWEScore > b.SWEScore
		}
		aFree := a.Cost.Input == 0 && a.Cost.Output == 0
		bFree := b.Cost.Input == 0 && b.Cost.Output == 0
		if aFree != bFree {
			return aFree
		}
		return a.ID < b.ID
	})
	return out
}

// BuildModelState creates an ACP SessionModelState from the model registry.
// Only includes models that have auth configured (API key or OAuth token).
// Models are sorted in a stable priority order (by capability/SWE score) so
// the Poe chat model dropdown is consistent across sessions.
func BuildModelState(reg *models.ModelRegistry, currentModel *ai.Model) *acpsdk.SessionModelState {
	if currentModel == nil {
		return nil
	}
	available := sortAvailableModels(reg.GetAvailable())
	modelInfos := make([]acpsdk.ModelInfo, 0, len(available))
	for _, m := range available {
		modelInfos = append(modelInfos, acpsdk.ModelInfo{
			ModelId: acpsdk.ModelId(fmt.Sprintf("%s/%s", m.Provider, m.ID)),
			Name:    fmt.Sprintf("%s / %s", m.Name, shortProvider(m.Provider)),
		})
	}
	return &acpsdk.SessionModelState{
		AvailableModels: modelInfos,
		CurrentModelId:  acpsdk.ModelId(fmt.Sprintf("%s/%s", currentModel.Provider, currentModel.ID)),
	}
}
