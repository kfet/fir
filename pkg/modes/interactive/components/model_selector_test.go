package components

import (
	"strings"
	"testing"

	"github.com/kfet/pi-go/pkg/ai"
	"github.com/kfet/pi-go/pkg/core"
)

func TestModelSelectorComponent_Render(t *testing.T) {
	model := &ai.Model{ID: "test-model", Name: "Test Model", Provider: "test-provider"}
	settings := core.NewInMemorySettingsManager(core.Settings{})
	tmpDir := t.TempDir()
	authStorage := core.NewAuthStorage(tmpDir)
	registry := core.NewModelRegistry(authStorage, "")

	comp := NewModelSelectorComponent(model, settings, registry, nil, func(*ai.Model) {}, func() {}, "")
	lines := comp.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected rendered output")
	}
	// Should at least show the border and search area
	joined := strings.Join(lines, "\n")
	_ = joined
}
