// Model ID parsing and model state building for ACP.
// Split from helpers.go.
package acp

import (
	"fmt"
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

// BuildModelState creates an ACP SessionModelState from the model registry.
// Only includes models that have auth configured (API key or OAuth token).
func BuildModelState(reg *models.ModelRegistry, currentModel *ai.Model) *acpsdk.SessionModelState {
	if currentModel == nil {
		return nil
	}
	available := reg.GetAvailable()
	models := make([]acpsdk.ModelInfo, 0, len(available))
	for _, m := range available {
		models = append(models, acpsdk.ModelInfo{
			ModelId: acpsdk.ModelId(fmt.Sprintf("%s/%s", m.Provider, m.ID)),
			Name:    fmt.Sprintf("%s / %s", m.Name, shortProvider(m.Provider)),
		})
	}
	return &acpsdk.SessionModelState{
		AvailableModels: models,
		CurrentModelId:  acpsdk.ModelId(fmt.Sprintf("%s/%s", currentModel.Provider, currentModel.ID)),
	}
}
