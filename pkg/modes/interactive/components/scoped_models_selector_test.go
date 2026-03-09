package components

import (
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

func makeDummyModels() []*ai.Model {
	return []*ai.Model{
		{Provider: "anthropic", ID: "claude-3", Name: "Claude 3"},
		{Provider: "anthropic", ID: "claude-4", Name: "Claude 4"},
		{Provider: "openai", ID: "gpt-4o", Name: "GPT-4o"},
	}
}

func TestScopedModelsSelectorComponent_Render(t *testing.T) {
	config := ScopedModelsConfig{
		AllModels: makeDummyModels(),
	}
	callbacks := ScopedModelsCallbacks{
		OnModelToggle:    func(string, bool) {},
		OnPersist:        func([]string) {},
		OnEnableAll:      func([]string) {},
		OnClearAll:       func() {},
		OnToggleProvider: func(string, []string, bool) {},
		OnCancel:         func() {},
	}

	comp := NewScopedModelsSelectorComponent(config, callbacks)
	lines := comp.Render(80)

	if len(lines) == 0 {
		t.Fatal("expected rendered output")
	}

	output := strings.Join(lines, "\n")
	if !strings.Contains(output, "Model Configuration") {
		t.Error("expected header text")
	}
	if !strings.Contains(output, "claude-3") {
		t.Error("expected model ID in output")
	}
}

func TestScopedModelsSelectorComponent_Cancel(t *testing.T) {
	cancelled := false
	config := ScopedModelsConfig{AllModels: makeDummyModels()}
	callbacks := ScopedModelsCallbacks{
		OnModelToggle:    func(string, bool) {},
		OnPersist:        func([]string) {},
		OnEnableAll:      func([]string) {},
		OnClearAll:       func() {},
		OnToggleProvider: func(string, []string, bool) {},
		OnCancel:         func() { cancelled = true },
	}

	comp := NewScopedModelsSelectorComponent(config, callbacks)
	comp.HandleInput("\x1b") // escape
	if !cancelled {
		t.Error("expected cancel callback")
	}
}

func TestScopedModelsSelectorComponent_ToggleModel(t *testing.T) {
	var toggledID string
	var toggledEnabled bool
	config := ScopedModelsConfig{
		AllModels:       makeDummyModels(),
		EnabledModelIDs: map[string]bool{"anthropic/claude-3": true},
	}
	callbacks := ScopedModelsCallbacks{
		OnModelToggle:    func(id string, enabled bool) { toggledID = id; toggledEnabled = enabled },
		OnPersist:        func([]string) {},
		OnEnableAll:      func([]string) {},
		OnClearAll:       func() {},
		OnToggleProvider: func(string, []string, bool) {},
		OnCancel:         func() {},
	}

	comp := NewScopedModelsSelectorComponent(config, callbacks)
	// Enter (ActSelectConfirm) toggles the selected item
	comp.HandleInput("\r")
	if toggledID == "" {
		t.Fatal("expected toggle callback to be called")
	}
	_ = toggledEnabled // verify the callback fires
}

func TestScopedModelsSelectorComponent_ClearAll(t *testing.T) {
	clearCalled := false
	config := ScopedModelsConfig{
		AllModels:              makeDummyModels(),
		EnabledModelIDs:        map[string]bool{"anthropic/claude-3": true},
		HasEnabledModelsFilter: true,
	}
	callbacks := ScopedModelsCallbacks{
		OnModelToggle:    func(string, bool) {},
		OnPersist:        func([]string) {},
		OnEnableAll:      func([]string) {},
		OnClearAll:       func() { clearCalled = true },
		OnToggleProvider: func(string, []string, bool) {},
		OnCancel:         func() {},
	}

	comp := NewScopedModelsSelectorComponent(config, callbacks)
	// Ctrl+X clears all
	comp.HandleInput("\x18")
	if !clearCalled {
		t.Error("expected clear-all callback")
	}
}

func TestScopedModelsSelectorComponent_EnableAll(t *testing.T) {
	var enabledIDs []string
	config := ScopedModelsConfig{
		AllModels:              makeDummyModels(),
		EnabledModelIDs:        map[string]bool{},
		HasEnabledModelsFilter: true,
	}
	callbacks := ScopedModelsCallbacks{
		OnModelToggle:    func(string, bool) {},
		OnPersist:        func([]string) {},
		OnEnableAll:      func(ids []string) { enabledIDs = ids },
		OnClearAll:       func() {},
		OnToggleProvider: func(string, []string, bool) {},
		OnCancel:         func() {},
	}

	comp := NewScopedModelsSelectorComponent(config, callbacks)
	// Ctrl+A enables all
	comp.HandleInput("\x01")
	if len(enabledIDs) == 0 {
		t.Error("expected enable-all callback with model IDs")
	}
}

func TestScopedModelsSelectorComponent_Persist(t *testing.T) {
	var persistedIDs []string
	config := ScopedModelsConfig{
		AllModels:              makeDummyModels(),
		EnabledModelIDs:        map[string]bool{"anthropic/claude-3": true, "openai/gpt-4o": true},
		HasEnabledModelsFilter: true,
	}
	callbacks := ScopedModelsCallbacks{
		OnModelToggle:    func(string, bool) {},
		OnPersist:        func(ids []string) { persistedIDs = ids },
		OnEnableAll:      func([]string) {},
		OnClearAll:       func() {},
		OnToggleProvider: func(string, []string, bool) {},
		OnCancel:         func() {},
	}

	comp := NewScopedModelsSelectorComponent(config, callbacks)
	// Ctrl+S saves/persists
	comp.HandleInput("\x13")
	if persistedIDs == nil {
		t.Error("expected persist callback to be called")
	}
}
