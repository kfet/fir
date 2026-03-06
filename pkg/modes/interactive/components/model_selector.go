// Ported from: packages/coding-agent/src/modes/interactive/components/model-selector.ts
// Upstream hash: 1caadb2e
package components

import (
	"fmt"
	"sort"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/core"
	"github.com/kfet/fir/pkg/modes/interactive/theme"
	"github.com/kfet/fir/pkg/tui"
	tuicomp "github.com/kfet/fir/pkg/tui/components"
)

// ModelItem represents a model entry in the selector.
type ModelItem struct {
	Provider string
	ID       string
	Model    *ai.Model
}

// ScopedModelItem represents a scoped model with thinking level.
type ScopedModelItem struct {
	Model         *ai.Model
	ThinkingLevel string
}

// ModelScope determines which model list to show.
type ModelScope string

const (
	ModelScopeAll    ModelScope = "all"
	ModelScopeScoped ModelScope = "scoped"
)

// ModelSelectorComponent renders a model selector with search.
type ModelSelectorComponent struct {
	tui.Container
	searchInput      *tuicomp.Input
	listContainer    *tui.Container
	allModels        []ModelItem
	scopedModelItems []ModelItem
	activeModels     []ModelItem
	filteredModels   []ModelItem
	selectedIndex    int
	currentModel     *ai.Model
	settingsManager  *core.SettingsManager
	modelRegistry    *core.ModelRegistry
	onSelect         func(model *ai.Model)
	onCancel         func()
	errorMessage     string
	scopedModels     []ScopedModelItem
	scope            ModelScope
	scopeText        *tuicomp.Text
	scopeHintText    *tuicomp.Text
	focused          bool
}

var _ tui.Component = (*ModelSelectorComponent)(nil)
var _ tui.InputHandler = (*ModelSelectorComponent)(nil)
var _ tui.Focusable = (*ModelSelectorComponent)(nil)

// NewModelSelectorComponent creates a new ModelSelectorComponent.
func NewModelSelectorComponent(
	currentModel *ai.Model,
	settingsManager *core.SettingsManager,
	modelRegistry *core.ModelRegistry,
	scopedModels []ScopedModelItem,
	onSelect func(model *ai.Model),
	onCancel func(),
	initialSearch string,
) *ModelSelectorComponent {
	scope := ModelScopeAll
	if len(scopedModels) > 0 {
		scope = ModelScopeScoped
	}

	c := &ModelSelectorComponent{
		currentModel:    currentModel,
		settingsManager: settingsManager,
		modelRegistry:   modelRegistry,
		scopedModels:    scopedModels,
		scope:           scope,
		onSelect:        onSelect,
		onCancel:        onCancel,
		listContainer:   &tui.Container{},
	}

	t := theme.GetTheme()

	// Top border
	c.AddChild(NewDynamicBorder(nil))
	c.AddChild(tuicomp.NewSpacer(1))

	// Scope hint or API key note
	if len(scopedModels) > 0 {
		c.scopeText = tuicomp.NewText(c.getScopeText(), 0, 0, nil)
		c.AddChild(c.scopeText)
		c.scopeHintText = tuicomp.NewText(c.getScopeHintText(), 0, 0, nil)
		c.AddChild(c.scopeHintText)
	} else {
		hintText := "Only showing models with configured API keys (see README for details)"
		c.AddChild(tuicomp.NewText(t.Fg("warning", hintText), 0, 0, nil))
	}
	c.AddChild(tuicomp.NewSpacer(1))

	// Search input
	c.searchInput = tuicomp.NewInput()
	if initialSearch != "" {
		c.searchInput.SetValue(initialSearch)
	}
	c.searchInput.OnSubmit = func(val string) {
		if c.selectedIndex >= 0 && c.selectedIndex < len(c.filteredModels) {
			c.handleSelectModel(c.filteredModels[c.selectedIndex].Model)
		}
	}
	c.AddChild(c.searchInput)
	c.AddChild(tuicomp.NewSpacer(1))

	// List container
	c.AddChild(c.listContainer)
	c.AddChild(tuicomp.NewSpacer(1))

	// Bottom border
	c.AddChild(NewDynamicBorder(nil))

	// Load models
	c.loadModels()
	if initialSearch != "" {
		c.filterModels(initialSearch)
	} else {
		c.updateList()
	}

	return c
}

// SetFocused propagates focus to the search input.
func (c *ModelSelectorComponent) SetFocused(focused bool) {
	c.focused = focused
	c.searchInput.Focused = focused
}

func (c *ModelSelectorComponent) loadModels() {
	c.modelRegistry.Refresh()

	if err := c.modelRegistry.GetError(); err != "" {
		c.errorMessage = err
	}

	available := c.modelRegistry.GetAvailable()
	models := make([]ModelItem, len(available))
	for i, m := range available {
		models[i] = ModelItem{Provider: m.Provider, ID: m.ID, Model: m}
	}

	c.allModels = c.sortModels(models)

	scopedItems := make([]ModelItem, len(c.scopedModels))
	for i, s := range c.scopedModels {
		scopedItems[i] = ModelItem{Provider: s.Model.Provider, ID: s.Model.ID, Model: s.Model}
	}
	c.scopedModelItems = c.sortModels(scopedItems)

	if c.scope == ModelScopeScoped {
		c.activeModels = c.scopedModelItems
	} else {
		c.activeModels = c.allModels
	}
	c.filteredModels = c.activeModels
	c.clampSelection()
}

func (c *ModelSelectorComponent) sortModels(models []ModelItem) []ModelItem {
	sorted := make([]ModelItem, len(models))
	copy(sorted, models)
	sort.SliceStable(sorted, func(i, j int) bool {
		iCurrent := ai.ModelsAreEqual(c.currentModel, sorted[i].Model)
		jCurrent := ai.ModelsAreEqual(c.currentModel, sorted[j].Model)
		if iCurrent && !jCurrent {
			return true
		}
		if !iCurrent && jCurrent {
			return false
		}
		// Sort by SWE-bench Verified score descending; unscored models go last.
		iScore := sorted[i].Model.SWEScore
		jScore := sorted[j].Model.SWEScore
		if iScore != jScore {
			return iScore > jScore
		}
		return sorted[i].Provider < sorted[j].Provider
	})
	return sorted
}

func (c *ModelSelectorComponent) getScopeText() string {
	t := theme.GetTheme()
	var allText, scopedText string
	if c.scope == ModelScopeAll {
		allText = t.Fg("accent", "all")
	} else {
		allText = t.Fg("muted", "all")
	}
	if c.scope == ModelScopeScoped {
		scopedText = t.Fg("accent", "scoped")
	} else {
		scopedText = t.Fg("muted", "scoped")
	}
	return t.Fg("muted", "Scope: ") + allText + t.Fg("muted", " | ") + scopedText
}

func (c *ModelSelectorComponent) getScopeHintText() string {
	t := theme.GetTheme()
	return KeyHint("tab", "scope") + t.Fg("muted", " (all/scoped)")
}

func (c *ModelSelectorComponent) setScope(scope ModelScope) {
	if c.scope == scope {
		return
	}
	c.scope = scope
	if scope == ModelScopeScoped {
		c.activeModels = c.scopedModelItems
	} else {
		c.activeModels = c.allModels
	}
	c.selectedIndex = 0
	c.filterModels(c.searchInput.GetValue())
	if c.scopeText != nil {
		c.scopeText.SetText(c.getScopeText())
	}
}

func (c *ModelSelectorComponent) filterModels(query string) {
	if query == "" {
		c.filteredModels = c.activeModels
	} else {
		c.filteredModels = tui.FuzzyFilter(c.activeModels, query, func(item ModelItem) string {
			return item.ID + " " + item.Provider
		})
	}
	c.clampSelection()
	c.updateList()
}

func (c *ModelSelectorComponent) clampSelection() {
	max := len(c.filteredModels) - 1
	if max < 0 {
		max = 0
	}
	if c.selectedIndex > max {
		c.selectedIndex = max
	}
}

func (c *ModelSelectorComponent) updateList() {
	t := theme.GetTheme()
	c.listContainer.Clear()

	maxVisible := 10
	startIndex := c.selectedIndex - maxVisible/2
	if startIndex > len(c.filteredModels)-maxVisible {
		startIndex = len(c.filteredModels) - maxVisible
	}
	if startIndex < 0 {
		startIndex = 0
	}
	endIndex := startIndex + maxVisible
	if endIndex > len(c.filteredModels) {
		endIndex = len(c.filteredModels)
	}

	for i := startIndex; i < endIndex; i++ {
		item := c.filteredModels[i]
		isSelected := i == c.selectedIndex
		isCurrent := ai.ModelsAreEqual(c.currentModel, item.Model)

		checkmark := ""
		if isCurrent {
			checkmark = t.Fg("success", " ✓")
		}
		providerBadge := t.Fg("muted", "["+item.Provider+"]")
		sweBadge := ""
		if item.Model.SWEScore > 0 {
			if item.Model.SWEInferred {
				sweBadge = " " + t.Fg("warning", fmt.Sprintf("[SWE:~%.0f%%]", item.Model.SWEScore))
			} else {
				sweBadge = " " + t.Fg("muted", fmt.Sprintf("[SWE:%.0f%%]", item.Model.SWEScore))
			}
		}

		var line string
		if isSelected {
			prefix := t.Fg("accent", "→ ")
			line = prefix + t.Fg("accent", item.ID) + " " + providerBadge + sweBadge + checkmark
		} else {
			line = "  " + item.ID + " " + providerBadge + sweBadge + checkmark
		}
		c.listContainer.AddChild(tuicomp.NewText(line, 0, 0, nil))
	}

	// Scroll indicator
	if startIndex > 0 || endIndex < len(c.filteredModels) {
		scrollInfo := fmt.Sprintf("  (%d/%d)", c.selectedIndex+1, len(c.filteredModels))
		c.listContainer.AddChild(tuicomp.NewText(t.Fg("muted", scrollInfo), 0, 0, nil))
	}

	// Error or empty state
	if c.errorMessage != "" {
		c.listContainer.AddChild(tuicomp.NewText(t.Fg("error", c.errorMessage), 0, 0, nil))
	} else if len(c.filteredModels) == 0 {
		c.listContainer.AddChild(tuicomp.NewText(t.Fg("muted", "  No matching models"), 0, 0, nil))
	} else if c.selectedIndex >= 0 && c.selectedIndex < len(c.filteredModels) {
		sel := c.filteredModels[c.selectedIndex]
		c.listContainer.AddChild(tuicomp.NewSpacer(1))
		c.listContainer.AddChild(tuicomp.NewText(t.Fg("muted", "  Model Name: "+sel.Model.Name), 0, 0, nil))
	}
}

// HandleInput handles keyboard input.
func (c *ModelSelectorComponent) HandleInput(data string) {
	switch {
	case tuicomp.MatchesEditorAction(data, "tab"):
		if len(c.scopedModelItems) > 0 {
			next := ModelScopeScoped
			if c.scope == ModelScopeScoped {
				next = ModelScopeAll
			}
			c.setScope(next)
			if c.scopeHintText != nil {
				c.scopeHintText.SetText(c.getScopeHintText())
			}
		}
	case tuicomp.MatchesEditorAction(data, tuicomp.ActSelectUp):
		if len(c.filteredModels) == 0 {
			return
		}
		if c.selectedIndex == 0 {
			c.selectedIndex = len(c.filteredModels) - 1
		} else {
			c.selectedIndex--
		}
		c.updateList()
	case tuicomp.MatchesEditorAction(data, tuicomp.ActSelectDown):
		if len(c.filteredModels) == 0 {
			return
		}
		if c.selectedIndex == len(c.filteredModels)-1 {
			c.selectedIndex = 0
		} else {
			c.selectedIndex++
		}
		c.updateList()
	case tuicomp.MatchesEditorAction(data, tuicomp.ActSelectConfirm):
		if c.selectedIndex >= 0 && c.selectedIndex < len(c.filteredModels) {
			c.handleSelectModel(c.filteredModels[c.selectedIndex].Model)
		}
	case tuicomp.MatchesEditorAction(data, tuicomp.ActSelectCancel):
		c.onCancel()
	default:
		c.searchInput.HandleInput(data)
		c.filterModels(c.searchInput.GetValue())
	}
}

func (c *ModelSelectorComponent) handleSelectModel(model *ai.Model) {
	c.settingsManager.SetDefaultModelAndProvider(model.Provider, model.ID)
	c.onSelect(model)
}

// GetSearchInput returns the underlying search input for focus management.
func (c *ModelSelectorComponent) GetSearchInput() *tuicomp.Input {
	return c.searchInput
}
