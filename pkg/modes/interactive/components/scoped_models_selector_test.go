package components

import (
	"strings"
	"testing"

	"github.com/kfet/tau/pkg/ai"
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
