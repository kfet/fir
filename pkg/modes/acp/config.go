package acp

import (
	"context"
	"fmt"
	"strings"

	"github.com/kfet/fir/pkg/agent"
	firlog "github.com/kfet/fir/pkg/log"
)

const thinkingConfigID = "thinking_level"

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

// buildConfigOptions returns all session config options for the given session.
func buildConfigOptions(entry *firSession) []SessionConfigOption {
	return []SessionConfigOption{
		buildThinkingConfigOptionFromAccessor(entry.getThinkingAccessor()),
	}
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
