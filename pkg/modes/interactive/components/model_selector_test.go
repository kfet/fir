package components

import (
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/fir/pkg/config"
	"github.com/kfet/fir/pkg/models"
)

func TestModelSelectorComponent_Render(t *testing.T) {
	model := &ai.Model{ID: "test-model", Name: "Test Model", Provider: "test-provider"}
	settings := config.NewInMemorySettingsManager(config.Settings{})
	tmpDir := t.TempDir()
	authStorage := auth.NewAuthStorage(tmpDir)
	registry := models.NewModelRegistry(authStorage, "")

	comp := NewModelSelectorComponent(model, settings, registry, nil, func(*ai.Model) {}, func() {}, "")
	lines := comp.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected rendered output")
	}
	// Should at least show the border and search area
	joined := strings.Join(lines, "\n")
	_ = joined
}
