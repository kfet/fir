package acp

import (
	"context"
	"fmt"
	"strings"

	"github.com/kfet/fir/pkg/agent"
	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/models"
	firlog "github.com/kfet/fir/pkg/log"
)

const (
	thinkingConfigID = "thinking_level"
	modelConfigID    = "model"
)

// thinkingAccessor abstracts the thinking-level operations on a session.
type thinkingAccessor interface {
	ThinkingLevel() string
	GetAvailableThinkingLevels() []agent.ThinkingLevel
	SetThinkingLevel(level string)
}

// thinkingLevelNames maps thinking levels to human-readable labels.
var thinkingLevelNames = map[agent.ThinkingLevel]string{
	agent.ThinkingOff:     "Off",
	agent.ThinkingMinimal: "Minimal",
	agent.ThinkingLow:     "Low",
	agent.ThinkingMedium:  "Medium",
	agent.ThinkingHigh:    "High",
	agent.ThinkingXHigh:   "Extra High",
}

// buildThinkingConfigOptionFromAccessor creates a SessionConfigOption for the thinking level selector.
func buildThinkingConfigOptionFromAccessor(s thinkingAccessor) SessionConfigOption {
	available := s.GetAvailableThinkingLevels()
	current := s.ThinkingLevel()
	if current == "" {
		current = string(agent.ThinkingOff)
	}

	options := make([]SessionConfigSelectOption, len(available))
	for i, lvl := range available {
		name := thinkingLevelNames[lvl]
		if name == "" {
			name = strings.ToUpper(string(lvl)[:1]) + string(lvl)[1:]
		}
		options[i] = SessionConfigSelectOption{
			Value: string(lvl),
			Name:  name,
		}
	}

	desc := "Controls the depth of reasoning the model uses"
	return SessionConfigOption{
		Type:         "select",
		Id:           thinkingConfigID,
		Name:         "Thinking",
		Description:  &desc,
		Category:     SessionConfigCategoryThoughtLevel,
		CurrentValue: current,
		Options:      options,
	}
}

// buildModelConfigOption creates a SessionConfigOption for the model selector.
func buildModelConfigOption(reg *models.ModelRegistry, currentModel *ai.Model) SessionConfigOption {
	available := reg.GetAvailable()
	options := make([]SessionConfigSelectOption, 0, len(available))
	for _, m := range available {
		value := fmt.Sprintf("%s/%s", m.Provider, m.ID)
		name := fmt.Sprintf("%s / %s", m.Name, shortProvider(m.Provider))
		options = append(options, SessionConfigSelectOption{
			Value: value,
			Name:  name,
		})
	}

	currentValue := ""
	if currentModel != nil {
		currentValue = fmt.Sprintf("%s/%s", currentModel.Provider, currentModel.ID)
	}

	desc := "The model to use for this session"
	return SessionConfigOption{
		Type:         "select",
		Id:           modelConfigID,
		Name:         "Model",
		Description:  &desc,
		Category:     SessionConfigCategoryModel,
		CurrentValue: currentValue,
		Options:      options,
	}
}

// buildConfigOptions returns all session config options for the given session.
func buildConfigOptions(entry *firSession) []SessionConfigOption {
	var opts []SessionConfigOption
	opts = append(opts, buildThinkingConfigOptionFromAccessor(entry.getThinkingAccessor()))
	// Model config only if we have a real session with model registry.
	if entry.session != nil && entry.modelRegistry != nil {
		opts = append(opts, buildModelConfigOption(entry.modelRegistry, entry.session.Model()))
	}
	return opts
}

// SetSessionConfigOption handles the session/set_config_option method.
func (pa *firAgent) SetSessionConfigOption(_ context.Context, params SetSessionConfigOptionRequest) (SetSessionConfigOptionResponse, error) {
	pa.mu.Lock()
	entry, ok := pa.sessions[params.SessionId]
	pa.mu.Unlock()
	if !ok {
		return SetSessionConfigOptionResponse{}, fmt.Errorf("session not found: %s", params.SessionId)
	}

	switch params.ConfigId {
	case modelConfigID:
		provider, modelID, err := ParseModelID(params.Value)
		if err != nil {
			return SetSessionConfigOptionResponse{}, err
		}
		model := entry.modelRegistry.Find(provider, modelID)
		if model == nil {
			return SetSessionConfigOptionResponse{}, fmt.Errorf("model not found: %s", params.Value)
		}
		entry.session.SetModel(model)
		firlog.Info("acp set model via config", "sessionId", params.SessionId, "model", params.Value)

	case thinkingConfigID:
		accessor := entry.getThinkingAccessor()
		level := agent.ThinkingLevel(params.Value)
		// Validate the level is available.
		available := accessor.GetAvailableThinkingLevels()
		valid := false
		for _, l := range available {
			if l == level {
				valid = true
				break
			}
		}
		if !valid {
			return SetSessionConfigOptionResponse{}, fmt.Errorf("invalid thinking level: %s", params.Value)
		}
		accessor.SetThinkingLevel(params.Value)
		firlog.Info("acp set thinking level", "sessionId", params.SessionId, "level", params.Value)
	default:
		return SetSessionConfigOptionResponse{}, fmt.Errorf("unknown config option: %s", params.ConfigId)
	}

	return SetSessionConfigOptionResponse{
		ConfigOptions: buildConfigOptions(entry),
	}, nil
}
