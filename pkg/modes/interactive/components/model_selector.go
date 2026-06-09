// Ported from: packages/coding-agent/src/modes/interactive/components/model-selector.ts
// Upstream hash: 1caadb2e
package components

import (
	"fmt"
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
	defaultModel    *ai.Model
	settingsManager *config.SettingsManager
	modelRegistry   *models.ModelRegistry
	keybindings     *tui.KeybindingsManager
	onSelect        func(model *ai.Model)
	onSetDefault    func(model *ai.Model)
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
	defaultModel *ai.Model,
	settingsManager *config.SettingsManager,
	modelRegistry *models.ModelRegistry,
	keybindings *tui.KeybindingsManager,
	onSelect func(model *ai.Model),
	onSetDefault func(model *ai.Model),
	onCancel func(),
	initialSearch string,
) *ModelSelectorComponent {
	c := &ModelSelectorComponent{
		currentModel:    currentModel,
		defaultModel:    defaultModel,
		settingsManager: settingsManager,
		modelRegistry:   modelRegistry,
		keybindings:     keybindings,
		onSelect:        onSelect,
		onSetDefault:    onSetDefault,
		onCancel:        onCancel,
		listContainer:   &tui.Container{},
	}

	t := theme.GetTheme()

	// Top border
	c.AddChild(NewDynamicBorder(nil))
	c.AddChild(tuicomp.NewSpacer(1))

	hintText := "Only showing models with configured API keys (see README for details)"
	c.AddChild(tuicomp.NewText(t.Fg("warning", hintText), 0, 0, nil))
	kbHint := "enter: use for this session  ·  ctrl+d: set as default"
	c.AddChild(tuicomp.NewText(t.Fg("muted", kbHint), 0, 0, nil))
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
	// Pin the configured default model to the top so it is always easy to find.
	sorted := models.SortModels(available, c.defaultModel)
	mdls := make([]ModelItem, len(sorted))
	for i, m := range sorted {
		mdls[i] = ModelItem{Provider: m.Provider, ID: m.ID, Model: m}
	}

	c.allModels = mdls
	c.activeModels = c.allModels
	c.filteredModels = c.activeModels
	c.clampSelection()

	// Open with the cursor on the active session model.
	for i, it := range c.filteredModels {
		if ai.ModelsAreEqual(c.currentModel, it.Model) {
			c.selectedIndex = i
			break
		}
	}
}

// isFreeModel is a thin wrapper over models.IsFreeModel kept for the badge
// rendering call sites in this file.
func isFreeModel(m *ai.Model) bool {
	return models.IsFreeModel(m)
}

func (c *ModelSelectorComponent) sortModels(items []ModelItem) []ModelItem {
	mdls := make([]*ai.Model, len(items))
	for i, it := range items {
		mdls[i] = it.Model
	}
	sortedM := models.SortModels(mdls, c.currentModel)
	out := make([]ModelItem, len(sortedM))
	for i, m := range sortedM {
		out[i] = ModelItem{Provider: m.Provider, ID: m.ID, Model: m}
	}
	return out
}

func (c *ModelSelectorComponent) filterModels(query string) {
	if query == "" {
		c.filteredModels = c.activeModels
	} else {
		c.filteredModels = tui.FuzzyFilter(c.activeModels, query, modelHaystack)
	}
	c.clampSelection()
	c.updateList()
}

// modelHaystack returns the searchable text for a model item — combining
// every field rendered in the row so the user can match by ID, provider,
// display name, or any badge ([FREE], cost, context size, SWE score).
func modelHaystack(item ModelItem) string {
	var b strings.Builder
	b.WriteString(item.ID)
	b.WriteByte(' ')
	b.WriteString(item.Provider)
	b.WriteString(" [")
	b.WriteString(item.Provider)
	b.WriteByte(']')
	if item.Model != nil {
		if item.Model.Name != "" {
			b.WriteByte(' ')
			b.WriteString(item.Model.Name)
		}
		if isFreeModel(item.Model) {
			b.WriteString(" [FREE] free")
		}
		if cb := formatCostBadge(item.Model.Cost); cb != "" {
			b.WriteByte(' ')
			b.WriteString(cb)
		}
		if ctx := formatContextBadge(item.Model.ContextWindow); ctx != "" {
			b.WriteByte(' ')
			b.WriteString(ctx)
		}
		if item.Model.SWEScore > 0 {
			if item.Model.SWEInferred {
				fmt.Fprintf(&b, " [SWE:~%.0f%%]", item.Model.SWEScore)
			} else {
				fmt.Fprintf(&b, " [SWE:%.0f%%]", item.Model.SWEScore)
			}
		}
	}
	return b.String()
}

func formatCostBadge(cost ai.ModelCost) string {
	if cost.Input == 0 && cost.Output == 0 {
		return ""
	}
	return fmt.Sprintf("[$%.2f/$%.2f]", cost.Input, cost.Output)
}

// formatContextBadge returns a short human-readable context window badge
// like "[128k]" or "[1M]". Returns "" for zero/unknown context windows.
func formatContextBadge(ctxWindow int) string {
	if ctxWindow <= 0 {
		return ""
	}
	switch {
	case ctxWindow >= 1_000_000 && ctxWindow%1_000_000 == 0:
		return fmt.Sprintf("[%dM]", ctxWindow/1_000_000)
	case ctxWindow >= 1_000_000:
		return fmt.Sprintf("[%.1fM]", float64(ctxWindow)/1_000_000)
	case ctxWindow%1000 == 0:
		return fmt.Sprintf("[%dk]", ctxWindow/1000)
	default:
		return fmt.Sprintf("[%.0fk]", float64(ctxWindow)/1000)
	}
}

// costTierColor returns the theme color name for a model based on input
// cost per million tokens.
//   - "" (default text) for free / unknown models — those get separate FREE badge
//   - "success" (green) for cheap (< $0.50/M)
//   - "warning" (yellow) for expensive (> $3/M)
//   - "" (default) for mid-range
func costTierColor(cost ai.ModelCost) string {
	if cost.Input <= 0 {
		return ""
	}
	if cost.Input < 0.50 {
		return "success"
	}
	if cost.Input > 3.0 {
		return "warning"
	}
	return ""
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
	switch {
	case isFreeModel(item.Model):
		idText = t.Fg("success", item.ID)
	default:
		if c := costTierColor(item.Model.Cost); c != "" {
			idText = t.Fg(c, item.ID)
		}
	}
	return "  " + idText + " " + providerBadge
}

func buildContextBadge(t *theme.Theme, item ModelItem) string {
	b := formatContextBadge(item.Model.ContextWindow)
	if b == "" {
		return ""
	}
	return t.Fg("muted", b)
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
	maxCtxWidth := 0
	maxSWEWidth := 0
	for _, item := range c.filteredModels {
		left := buildLeftPart(t, item, false)
		price := buildPriceBadge(t, item)
		ctx := buildContextBadge(t, item)
		swe := buildSWEBadge(t, item)

		if lw := tui.VisibleWidth(left); lw > maxLeftWidth {
			maxLeftWidth = lw
		}
		if pw := tui.VisibleWidth(price); pw > maxPriceWidth {
			maxPriceWidth = pw
		}
		if cw := tui.VisibleWidth(ctx); cw > maxCtxWidth {
			maxCtxWidth = cw
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
		defaultMark := ""
		if ai.ModelsAreEqual(c.defaultModel, item.Model) {
			defaultMark = t.Fg("muted", " [default]")
		}
		priceBadge := buildPriceBadge(t, item)
		ctxBadge := buildContextBadge(t, item)

		leftPart := buildLeftPart(t, item, isSelected)
		sweBadge := buildSWEBadge(t, item)
		leftPad := padToWidth(leftPart, maxLeftWidth) + " "

		line := leftPart + leftPad
		line += priceBadge + padToWidth(priceBadge, maxPriceWidth)
		line += " "
		line += ctxBadge + padToWidth(ctxBadge, maxCtxWidth)
		line += " "
		line += sweBadge + padToWidth(sweBadge, maxSWEWidth)
		line += defaultMark
		line += checkmark
		if isSelected {
			line = t.Bg("selectedBg", line)
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
	case c.keybindings != nil && c.keybindings.Matches(data, tui.ActionSetDefaultModel):
		if c.selectedIndex >= 0 && c.selectedIndex < len(c.filteredModels) {
			c.onSetDefault(c.filteredModels[c.selectedIndex].Model)
		}
	case c.keybindings != nil && c.keybindings.Matches(data, tui.ActionSelectModel):
		c.onCancel()
	default:
		c.searchInput.HandleInput(data)
		c.filterModels(c.searchInput.GetValue())
	}
}

func (c *ModelSelectorComponent) handleSelectModel(model *ai.Model) {
	c.onSelect(model)
}

// GetSearchInput returns the underlying search input for focus management.
func (c *ModelSelectorComponent) GetSearchInput() *tuicomp.Input {
	return c.searchInput
}
