// Model ID parsing and model state building for ACP.
// Split from helpers.go.
package acp

import (
	"fmt"
	"strings"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/models"
)

// ParseModelID splits an ACP model ID "provider/modelId" into its components.
func ParseModelID(acpModelID string) (provider, modelID string, err error) {
	idx := strings.Index(acpModelID, "/")
	if idx == -1 {
		return "", "", fmt.Errorf("invalid model ID format: %s. Expected \"provider/modelId\"", acpModelID)
	}
	return acpModelID[:idx], acpModelID[idx+1:], nil
}

// shortProvider abbreviates provider names for display.  Sourced from the
// ai.RegisteredProvider registry's ShortName field.
func shortProvider(provider string) string {
	if r := ai.GetProviderRecord(ai.Provider(provider)); r != nil && r.ShortName != "" {
		return r.ShortName
	}
	return provider
}

// BuildModelState creates an ACP SessionModelState from the model registry.
// Only includes models that have auth configured (API key or OAuth token).
// Models are sorted in a stable priority order (by capability/SWE score) so
// the Poe chat model dropdown is consistent across sessions.
func BuildModelState(reg *models.ModelRegistry, currentModel *ai.Model) *SessionModelState {
	if currentModel == nil {
		return nil
	}
	available := models.SortModels(reg.GetAvailable(), nil)
	modelInfos := make([]ModelInfo, 0, len(available))
	for _, m := range available {
		modelInfos = append(modelInfos, ModelInfo{
			ModelId: ModelId(fmt.Sprintf("%s/%s", m.Provider, m.ID)),
			Name:    fmt.Sprintf("%s / %s", m.Name, shortProvider(m.Provider)),
		})
	}
	return &SessionModelState{
		AvailableModels: modelInfos,
		CurrentModelId:  ModelId(fmt.Sprintf("%s/%s", currentModel.Provider, currentModel.ID)),
	}
}
