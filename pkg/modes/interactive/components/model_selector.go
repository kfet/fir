// Ported from: packages/coding-agent/src/modes/interactive/components/model-selector.ts
// Upstream hash: 1caadb2e
package components

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/config"
	"github.com/kfet/fir/pkg/models"
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

// ModelSelectorComponent renders a model selector with search.
type ModelSelectorComponent struct {
	tui.Container
	searchInput     *tuicomp.Input
	listContainer   *tui.Container
	allModels       []ModelItem
	activeModels    []ModelItem
	filteredModels  []ModelItem
	selectedIndex   int
	currentModel    *ai.Model
	settingsManager *config.SettingsManager
	modelRegistry   *models.ModelRegistry
	keybindings     *tui.KeybindingsManager
	onSelect        func(model *ai.Model)
	onCancel        func()
	errorMessage    string
	focused         bool
}

var _ tui.Component = (*ModelSelectorComponent)(nil)
var _ tui.InputHandler = (*ModelSelectorComponent)(nil)
var _ tui.Focusable = (*ModelSelectorComponent)(nil)

// NewModelSelectorComponent creates a new ModelSelectorComponent.
func NewModelSelectorComponent(
	currentModel *ai.Model,
	settingsManager *config.SettingsManager,
	modelRegistry *models.ModelRegistry,
	keybindings *tui.KeybindingsManager,
	onSelect func(model *ai.Model),
	onCancel func(),
	initialSearch string,
) *ModelSelectorComponent {
	c := &ModelSelectorComponent{
		currentModel:    currentModel,
		settingsManager: settingsManager,
		modelRegistry:   modelRegistry,
		keybindings:     keybindings,
		onSelect:        onSelect,
		onCancel:        onCancel,
		listContainer:   &tui.Container{},
	}

	t := theme.GetTheme()

	// Top border
	c.AddChild(NewDynamicBorder(nil))
	c.AddChild(tuicomp.NewSpacer(1))

	hintText := "Only showing models with configured API keys (see README for details)"
	c.AddChild(tuicomp.NewText(t.Fg("warning", hintText), 0, 0, nil))
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
	// Don't call modelRegistry.Refresh() here — it synchronously re-reads
	// models.json, rebuilds all built-in models, and runs OAuth hooks, which
	// is expensive on low-power devices (e.g. Raspberry Pi). The registry is
	// already populated at startup and kept current by background live-model
	// fetches and login/logout flows.

	if err := c.modelRegistry.GetError(); err != "" {
		c.errorMessage = err
	}

	available := c.modelRegistry.GetAvailable()
	mdls := make([]ModelItem, len(available))
	for i, m := range available {
		mdls[i] = ModelItem{Provider: m.Provider, ID: m.ID, Model: m}
	}

	c.allModels = c.sortModels(mdls)
	c.activeModels = c.allModels
	c.filteredModels = c.activeModels
	c.clampSelection()
}

// isFreeModel reports whether the model is genuinely free to call — i.e. it
// has zero pricing across all cost axes AND is hosted on a provider where
// zero cost reflects per-call reality rather than a subscription plan the
// user may or may not have. Today that means Poe, which exposes the same
// underlying models as both paid and free bots; other zero-cost entries
// (GitHub Copilot, Gemini CLI, Antigravity, OpenAI Codex) are behind
// subscription/OAuth gates and shouldn't be advertised as "free".
func isFreeModel(m *ai.Model) bool {
	if m == nil || m.Provider != ai.ProviderPoe {
		return false
	}
	c := m.Cost
	return c.Input == 0 && c.Output == 0 && c.CacheRead == 0 && c.CacheWrite == 0
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
		// When two entries refer to the "same" model (same provider + display
		// name — common on Poe where the same model is exposed as multiple
		// bots with different pricing), put the free variant first.
		iFree := isFreeModel(sorted[i].Model)
		jFree := isFreeModel(sorted[j].Model)
		if sorted[i].Provider == sorted[j].Provider &&
			sorted[i].Model.Name == sorted[j].Model.Name &&
			iFree != jFree {
			return iFree
		}
		// Sort by SWE-bench Verified score descending; unscored models go last.
		iScore := sorted[i].Model.SWEScore
		jScore := sorted[j].Model.SWEScore
		if iScore != jScore {
			return iScore > jScore
		}
		if sorted[i].Provider != sorted[j].Provider {
			return sorted[i].Provider < sorted[j].Provider
		}
		// Final tiebreaker: free models ahead of paid ones.
		if iFree != jFree {
			return iFree
		}
		return false
	})
	return sorted
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

func formatCostBadge(cost ai.ModelCost) string {
	if cost.Input == 0 && cost.Output == 0 {
		return ""
	}
	return fmt.Sprintf("[$%.2f/$%.2f]", cost.Input, cost.Output)
}

func formatCostDetails(cost ai.ModelCost) string {
	parts := []string{
		fmt.Sprintf("in: $%.2f/1M", cost.Input),
		fmt.Sprintf("out: $%.2f/1M", cost.Output),
	}
	// Only surface cache pricing when the model actually charges for it;
	// most models have zero on both axes and the extra columns are noise.
	if cost.CacheRead != 0 || cost.CacheWrite != 0 {
		parts = append(parts,
			fmt.Sprintf("cache read: $%.2f/1M", cost.CacheRead),
			fmt.Sprintf("cache write: $%.2f/1M", cost.CacheWrite),
		)
	}
	return strings.Join(parts, " · ")
}

// buildLeftPart renders the portion of a row up to provider badge.
// It's used both for the final render and for computing the alignment column
// across all filtered models. The visible width is independent of `selected`
// because both the "→ " accent prefix and the plain "  " prefix occupy two
// visible columns.
func buildLeftPart(t *theme.Theme, item ModelItem, selected bool) string {
	providerBadge := t.Fg("muted", "["+item.Provider+"]")
	if selected {
		return t.Fg("accent", "→ ") + t.Fg("accent", item.ID) + " " + providerBadge
	}
	idText := item.ID
	if isFreeModel(item.Model) {
		idText = t.Fg("success", item.ID)
	}
	return "  " + idText + " " + providerBadge
}

func buildPriceBadge(t *theme.Theme, item ModelItem) string {
	if isFreeModel(item.Model) {
		return t.Fg("success", "[FREE]")
	}
	if b := formatCostBadge(item.Model.Cost); b != "" {
		return t.Fg("muted", b)
	}
	return ""
}

func buildSWEBadge(t *theme.Theme, item ModelItem) string {
	if item.Model.SWEScore > 0 {
		if item.Model.SWEInferred {
			return " " + t.Fg("warning", fmt.Sprintf("[SWE:~%.0f%%]", item.Model.SWEScore))
		}
		return " " + t.Fg("muted", fmt.Sprintf("[SWE:%.0f%%]", item.Model.SWEScore))
	}
	return ""
}

func padToWidth(text string, width int) string {
	pad := width - tui.VisibleWidth(text)
	if pad <= 0 {
		return ""
	}
	return strings.Repeat(" ", pad)
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

	// Compute alignment widths across ALL filtered models so columns stay stable
	// while scrolling.
	maxLeftWidth := 0
	maxPriceWidth := 0
	maxSWEWidth := 0
	for _, item := range c.filteredModels {
		left := buildLeftPart(t, item, false)
		price := buildPriceBadge(t, item)
		swe := buildSWEBadge(t, item)

		if lw := tui.VisibleWidth(left); lw > maxLeftWidth {
			maxLeftWidth = lw
		}
		if pw := tui.VisibleWidth(price); pw > maxPriceWidth {
			maxPriceWidth = pw
		}
		if sw := tui.VisibleWidth(swe); sw > maxSWEWidth {
			maxSWEWidth = sw
		}
	}

	for i := startIndex; i < endIndex; i++ {
		item := c.filteredModels[i]
		isSelected := i == c.selectedIndex
		isCurrent := ai.ModelsAreEqual(c.currentModel, item.Model)

		checkmark := ""
		if isCurrent {
			checkmark = t.Fg("success", " ✓")
		}
		priceBadge := buildPriceBadge(t, item)

		leftPart := buildLeftPart(t, item, isSelected)
		sweBadge := buildSWEBadge(t, item)
		leftPad := padToWidth(leftPart, maxLeftWidth) + " "

		line := leftPart + leftPad
		line += priceBadge + padToWidth(priceBadge, maxPriceWidth)
		line += " "
		line += sweBadge + padToWidth(sweBadge, maxSWEWidth)
		line += checkmark
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
		c.listContainer.AddChild(tuicomp.NewText(t.Fg("muted", "  Cost (per 1M tokens): "+formatCostDetails(sel.Model.Cost)), 0, 0, nil))
	}
}

// HandleInput handles keyboard input.
func (c *ModelSelectorComponent) HandleInput(data string) {
	switch {
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
	case c.keybindings != nil && c.keybindings.Matches(data, tui.ActionSelectModel):
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
