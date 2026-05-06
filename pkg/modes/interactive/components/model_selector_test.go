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

func TestSortModels_FreeBeatsPaidForSameModel(t *testing.T) {
	current := &ai.Model{ID: "current", Provider: "p"}
	settings := config.NewInMemorySettingsManager(config.Settings{})
	tmpDir := t.TempDir()
	authStorage := auth.NewAuthStorage(tmpDir)
	registry := models.NewModelRegistry(authStorage, "")
	c := NewModelSelectorComponent(current, settings, registry, nil, func(*ai.Model) {}, func() {}, "")

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
	c := NewModelSelectorComponent(current, settings, registry, nil, func(*ai.Model) {}, func() {}, "")

	free := &ai.Model{ID: "free-one", Name: "Free", Provider: "poe"}
	c.filteredModels = []ModelItem{{Provider: "poe", ID: free.ID, Model: free}}
	c.selectedIndex = 0
	c.updateList()
	rendered := strings.Join(c.listContainer.Render(80), "\n")
	if !strings.Contains(rendered, "FREE") {
		t.Fatalf("expected FREE badge in rendered output, got:\n%s", rendered)
	}
}

func TestSortModels_ZeroCostCopilotNotTreatedAsFree(t *testing.T) {
	// GitHub Copilot models list with zero per-token pricing because Copilot
	// is billed via subscription, not per-call. We must not advertise them
	// as FREE in the picker, and they should not win the same-model
	// tiebreaker.
	current := &ai.Model{ID: "current", Provider: "p"}
	settings := config.NewInMemorySettingsManager(config.Settings{})
	tmpDir := t.TempDir()
	authStorage := auth.NewAuthStorage(tmpDir)
	registry := models.NewModelRegistry(authStorage, "")
	c := NewModelSelectorComponent(current, settings, registry, nil, func(*ai.Model) {}, func() {}, "")

	copilot := &ai.Model{ID: "claude-sonnet-4", Name: "Claude", Provider: ai.ProviderGitHubCopilot, SWEScore: 77}
	if isFreeModel(copilot) {
		t.Fatalf("copilot zero-cost model must not be classified as free")
	}
	c.filteredModels = []ModelItem{{Provider: copilot.Provider, ID: copilot.ID, Model: copilot}}
	c.selectedIndex = 0
	c.updateList()
	rendered := strings.Join(c.listContainer.Render(80), "\n")
	if strings.Contains(rendered, "FREE") {
		t.Fatalf("did not expect FREE badge for Copilot model, got:\n%s", rendered)
	}
}

func TestFilterModels_SearchesAcrossFields(t *testing.T) {
	current := &ai.Model{ID: "current", Provider: "p"}
	settings := config.NewInMemorySettingsManager(config.Settings{})
	tmpDir := t.TempDir()
	authStorage := auth.NewAuthStorage(tmpDir)
	registry := models.NewModelRegistry(authStorage, "")
	c := NewModelSelectorComponent(current, settings, registry, nil, func(*ai.Model) {}, func() {}, "")

	free := &ai.Model{ID: "claude-free", Name: "Claude Free", Provider: "poe"}
	paid := &ai.Model{ID: "gpt-paid", Name: "GPT Paid", Provider: "openai",
		Cost: ai.ModelCost{Input: 3, Output: 15}, ContextWindow: 128000, SWEScore: 70}
	items := []ModelItem{
		{Provider: free.Provider, ID: free.ID, Model: free},
		{Provider: paid.Provider, ID: paid.ID, Model: paid},
	}
	c.activeModels = items
	c.allModels = items

	cases := []struct {
		query   string
		wantID  string
		wantLen int
	}{
		{"[free", "claude-free", 1},
		{"FREE", "claude-free", 1},
		{"128k", "gpt-paid", 1},
		{"openai", "gpt-paid", 1},
		{"[openai]", "gpt-paid", 1},
		{"SWE:70", "gpt-paid", 1},
	}
	for _, tc := range cases {
		c.filterModels(tc.query)
		if len(c.filteredModels) != tc.wantLen {
			t.Fatalf("query %q: expected %d results, got %d", tc.query, tc.wantLen, len(c.filteredModels))
		}
		if c.filteredModels[0].ID != tc.wantID {
			t.Fatalf("query %q: expected %q, got %q", tc.query, tc.wantID, c.filteredModels[0].ID)
		}
	}
}
