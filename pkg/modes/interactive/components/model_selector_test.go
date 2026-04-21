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

	comp := NewModelSelectorComponent(model, settings, registry, func(*ai.Model) {}, func() {}, "")
	lines := comp.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected rendered output")
	}
	// Should at least show the border and search area
	joined := strings.Join(lines, "\n")
	_ = joined
}

func TestSortModels_FreeBeatsPaidForSameModel(t *testing.T) {
	current := &ai.Model{ID: "current", Provider: "p"}
	settings := config.NewInMemorySettingsManager(config.Settings{})
	tmpDir := t.TempDir()
	authStorage := auth.NewAuthStorage(tmpDir)
	registry := models.NewModelRegistry(authStorage, "")
	c := NewModelSelectorComponent(current, settings, registry, func(*ai.Model) {}, func() {}, "")

	paid := &ai.Model{ID: "claude-paid", Name: "Claude", Provider: "poe",
		Cost: ai.ModelCost{Input: 3, Output: 15}, SWEScore: 70}
	free := &ai.Model{ID: "claude-free", Name: "Claude", Provider: "poe", SWEScore: 70}

	out := c.sortModels([]ModelItem{
		{Provider: "poe", ID: paid.ID, Model: paid},
		{Provider: "poe", ID: free.ID, Model: free},
	})
	if out[0].Model != free {
		t.Fatalf("expected free model first, got %s", out[0].ID)
	}
}

func TestSortModels_FreeMarkedWithBadge(t *testing.T) {
	current := &ai.Model{ID: "current", Provider: "p"}
	settings := config.NewInMemorySettingsManager(config.Settings{})
	tmpDir := t.TempDir()
	authStorage := auth.NewAuthStorage(tmpDir)
	registry := models.NewModelRegistry(authStorage, "")
	c := NewModelSelectorComponent(current, settings, registry, func(*ai.Model) {}, func() {}, "")

	free := &ai.Model{ID: "free-one", Name: "Free", Provider: "poe"}
	c.filteredModels = []ModelItem{{Provider: "poe", ID: free.ID, Model: free}}
	c.selectedIndex = 0
	c.updateList()
	rendered := strings.Join(c.listContainer.Render(80), "\n")
	if !strings.Contains(rendered, "FREE") {
		t.Fatalf("expected FREE badge in rendered output, got:\n%s", rendered)
	}
}
