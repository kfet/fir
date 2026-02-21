// Ported from: packages/coding-agent/src/modes/interactive/components/scoped-models-selector.ts
// Upstream hash: 1caadb2e
package components

import (
	"fmt"
	"strings"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/modes/interactive/theme"
	"github.com/kfet/fir/pkg/tui"
	tuicomp "github.com/kfet/fir/pkg/tui/components"
)

// ScopedModelsConfig holds the configuration for the scoped models selector.
type ScopedModelsConfig struct {
	AllModels              []*ai.Model
	EnabledModelIDs        map[string]bool // set of full IDs
	HasEnabledModelsFilter bool
}

// ScopedModelsCallbacks holds callbacks for model selection events.
type ScopedModelsCallbacks struct {
	OnModelToggle   func(modelID string, enabled bool)
	OnPersist       func(enabledModelIDs []string)
	OnEnableAll     func(allModelIDs []string)
	OnClearAll      func()
	OnToggleProvider func(provider string, modelIDs []string, enabled bool)
	OnCancel        func()
}

// scopedModelItem is a view item in the scoped model list.
type scopedModelItem struct {
	FullID  string
	Model   *ai.Model
	Enabled bool
}

// ScopedModelsSelectorComponent allows enabling/disabling models for Ctrl+P cycling.
type ScopedModelsSelectorComponent struct {
	tui.Container
	modelsById    map[string]*ai.Model
	allIDs        []string
	enabledIDs    []string // nil means all enabled
	allEnabled    bool     // true when enabledIDs is nil (all enabled)
	filteredItems []scopedModelItem
	selectedIndex int
	searchInput   *tuicomp.Input
	listContainer *tui.Container
	footerText    *tuicomp.Text
	callbacks     ScopedModelsCallbacks
	maxVisible    int
	isDirty       bool
	focused       bool
}

var _ tui.Component = (*ScopedModelsSelectorComponent)(nil)
var _ tui.InputHandler = (*ScopedModelsSelectorComponent)(nil)
var _ tui.Focusable = (*ScopedModelsSelectorComponent)(nil)

// NewScopedModelsSelectorComponent creates a new ScopedModelsSelectorComponent.
func NewScopedModelsSelectorComponent(config ScopedModelsConfig, callbacks ScopedModelsCallbacks) *ScopedModelsSelectorComponent {
	t := theme.GetTheme()

	c := &ScopedModelsSelectorComponent{
		modelsById:    make(map[string]*ai.Model),
		callbacks:     callbacks,
		maxVisible:    15,
		listContainer: &tui.Container{},
	}

	for _, m := range config.AllModels {
		fullID := m.Provider + "/" + m.ID
		c.modelsById[fullID] = m
		c.allIDs = append(c.allIDs, fullID)
	}

	if config.HasEnabledModelsFilter {
		for id := range config.EnabledModelIDs {
			c.enabledIDs = append(c.enabledIDs, id)
		}
		c.allEnabled = false
	} else {
		c.allEnabled = true
	}

	c.filteredItems = c.buildItems()

	// Header
	c.AddChild(NewDynamicBorder(nil))
	c.AddChild(tuicomp.NewSpacer(1))
	c.AddChild(tuicomp.NewText(t.Fg("accent", t.Bold("Model Configuration")), 0, 0, nil))
	c.AddChild(tuicomp.NewText(t.Fg("muted", "Session-only. Ctrl+S to save to settings."), 0, 0, nil))
	c.AddChild(tuicomp.NewSpacer(1))

	// Search input
	c.searchInput = tuicomp.NewInput()
	c.AddChild(c.searchInput)
	c.AddChild(tuicomp.NewSpacer(1))

	// List container
	c.AddChild(c.listContainer)

	// Footer
	c.AddChild(tuicomp.NewSpacer(1))
	c.footerText = tuicomp.NewText(c.getFooterText(), 0, 0, nil)
	c.AddChild(c.footerText)
	c.AddChild(NewDynamicBorder(nil))

	c.updateList()
	return c
}

// SetFocused propagates focus to the search input.
func (c *ScopedModelsSelectorComponent) SetFocused(focused bool) {
	c.focused = focused
	c.searchInput.Focused = focused
}

func (c *ScopedModelsSelectorComponent) isEnabled(id string) bool {
	if c.allEnabled {
		return true
	}
	for _, eid := range c.enabledIDs {
		if eid == id {
			return true
		}
	}
	return false
}

func (c *ScopedModelsSelectorComponent) toggle(id string) {
	if c.allEnabled {
		// First toggle: start with only this one
		c.enabledIDs = []string{id}
		c.allEnabled = false
		return
	}
	for i, eid := range c.enabledIDs {
		if eid == id {
			c.enabledIDs = append(c.enabledIDs[:i], c.enabledIDs[i+1:]...)
			return
		}
	}
	c.enabledIDs = append(c.enabledIDs, id)
}

func (c *ScopedModelsSelectorComponent) enableAll(targetIDs []string) {
	if targetIDs == nil {
		c.allEnabled = true
		c.enabledIDs = nil
		return
	}
	if c.allEnabled {
		return
	}
	set := make(map[string]bool)
	for _, id := range c.enabledIDs {
		set[id] = true
	}
	for _, id := range targetIDs {
		if !set[id] {
			c.enabledIDs = append(c.enabledIDs, id)
			set[id] = true
		}
	}
	if len(c.enabledIDs) >= len(c.allIDs) {
		c.allEnabled = true
		c.enabledIDs = nil
	}
}

func (c *ScopedModelsSelectorComponent) clearAll(targetIDs []string) {
	if targetIDs == nil {
		if c.allEnabled {
			c.enabledIDs = nil
			c.allEnabled = false
		} else {
			c.enabledIDs = nil
		}
		return
	}
	remove := make(map[string]bool)
	for _, id := range targetIDs {
		remove[id] = true
	}
	if c.allEnabled {
		c.allEnabled = false
		c.enabledIDs = nil
		for _, id := range c.allIDs {
			if !remove[id] {
				c.enabledIDs = append(c.enabledIDs, id)
			}
		}
	} else {
		var kept []string
		for _, id := range c.enabledIDs {
			if !remove[id] {
				kept = append(kept, id)
			}
		}
		c.enabledIDs = kept
	}
}

func (c *ScopedModelsSelectorComponent) getSortedIDs() []string {
	if c.allEnabled {
		return c.allIDs
	}
	enabledSet := make(map[string]bool)
	for _, id := range c.enabledIDs {
		enabledSet[id] = true
	}
	result := make([]string, 0, len(c.allIDs))
	result = append(result, c.enabledIDs...)
	for _, id := range c.allIDs {
		if !enabledSet[id] {
			result = append(result, id)
		}
	}
	return result
}

func (c *ScopedModelsSelectorComponent) buildItems() []scopedModelItem {
	var items []scopedModelItem
	for _, id := range c.getSortedIDs() {
		m, ok := c.modelsById[id]
		if !ok {
			continue
		}
		items = append(items, scopedModelItem{
			FullID:  id,
			Model:   m,
			Enabled: c.isEnabled(id),
		})
	}
	return items
}

func (c *ScopedModelsSelectorComponent) getFooterText() string {
	t := theme.GetTheme()
	enabledCount := len(c.enabledIDs)
	if c.allEnabled {
		enabledCount = len(c.allIDs)
	}
	countText := fmt.Sprintf("%d/%d enabled", enabledCount, len(c.allIDs))
	if c.allEnabled {
		countText = "all enabled"
	}
	parts := []string{"Enter toggle", "^A all", "^X clear", "^P provider", "Alt+↑↓ reorder", "^S save", countText}
	line := "  " + strings.Join(parts, " · ")
	if c.isDirty {
		return t.Fg("dim", line+" ") + t.Fg("warning", "(unsaved)")
	}
	return t.Fg("dim", line)
}

func (c *ScopedModelsSelectorComponent) refresh() {
	query := c.searchInput.GetValue()
	items := c.buildItems()
	if query != "" {
		c.filteredItems = tui.FuzzyFilter(items, query, func(i scopedModelItem) string {
			return i.Model.ID + " " + i.Model.Provider
		})
	} else {
		c.filteredItems = items
	}
	max := len(c.filteredItems) - 1
	if max < 0 {
		max = 0
	}
	if c.selectedIndex > max {
		c.selectedIndex = max
	}
	c.updateList()
	c.footerText.SetText(c.getFooterText())
}

func (c *ScopedModelsSelectorComponent) updateList() {
	t := theme.GetTheme()
	c.listContainer.Clear()

	if len(c.filteredItems) == 0 {
		c.listContainer.AddChild(tuicomp.NewText(t.Fg("muted", "  No matching models"), 0, 0, nil))
		return
	}

	startIndex := c.selectedIndex - c.maxVisible/2
	if startIndex > len(c.filteredItems)-c.maxVisible {
		startIndex = len(c.filteredItems) - c.maxVisible
	}
	if startIndex < 0 {
		startIndex = 0
	}
	endIndex := startIndex + c.maxVisible
	if endIndex > len(c.filteredItems) {
		endIndex = len(c.filteredItems)
	}

	for i := startIndex; i < endIndex; i++ {
		item := c.filteredItems[i]
		isSelected := i == c.selectedIndex

		prefix := "  "
		if isSelected {
			prefix = t.Fg("accent", "→ ")
		}

		modelText := item.Model.ID
		if isSelected {
			modelText = t.Fg("accent", modelText)
		}

		providerBadge := t.Fg("muted", " ["+item.Model.Provider+"]")

		status := ""
		if !c.allEnabled {
			if item.Enabled {
				status = t.Fg("success", " ✓")
			} else {
				status = t.Fg("dim", " ✗")
			}
		}

		c.listContainer.AddChild(tuicomp.NewText(prefix+modelText+providerBadge+status, 0, 0, nil))
	}

	if startIndex > 0 || endIndex < len(c.filteredItems) {
		scrollInfo := fmt.Sprintf("  (%d/%d)", c.selectedIndex+1, len(c.filteredItems))
		c.listContainer.AddChild(tuicomp.NewText(t.Fg("muted", scrollInfo), 0, 0, nil))
	}

	if c.selectedIndex >= 0 && c.selectedIndex < len(c.filteredItems) {
		sel := c.filteredItems[c.selectedIndex]
		c.listContainer.AddChild(tuicomp.NewSpacer(1))
		c.listContainer.AddChild(tuicomp.NewText(t.Fg("muted", "  Model Name: "+sel.Model.Name), 0, 0, nil))
	}
}

// HandleInput handles keyboard input.
func (c *ScopedModelsSelectorComponent) HandleInput(data string) {
	switch {
	case tuicomp.MatchesEditorAction(data, tuicomp.ActSelectUp):
		if len(c.filteredItems) == 0 {
			return
		}
		if c.selectedIndex == 0 {
			c.selectedIndex = len(c.filteredItems) - 1
		} else {
			c.selectedIndex--
		}
		c.updateList()

	case tuicomp.MatchesEditorAction(data, tuicomp.ActSelectDown):
		if len(c.filteredItems) == 0 {
			return
		}
		if c.selectedIndex == len(c.filteredItems)-1 {
			c.selectedIndex = 0
		} else {
			c.selectedIndex++
		}
		c.updateList()

	case tuicomp.MatchesEditorAction(data, tuicomp.ActSelectConfirm):
		if c.selectedIndex >= 0 && c.selectedIndex < len(c.filteredItems) {
			item := c.filteredItems[c.selectedIndex]
			wasAllEnabled := c.allEnabled
			c.toggle(item.FullID)
			c.isDirty = true
			if wasAllEnabled {
				c.callbacks.OnClearAll()
			}
			c.callbacks.OnModelToggle(item.FullID, c.isEnabled(item.FullID))
			c.refresh()
		}

	case tui.MatchesKey(data, tui.KeyCtrl("a")):
		var targetIDs []string
		if c.searchInput.GetValue() != "" {
			for _, item := range c.filteredItems {
				targetIDs = append(targetIDs, item.FullID)
			}
		}
		c.enableAll(targetIDs)
		c.isDirty = true
		if targetIDs == nil {
			c.callbacks.OnEnableAll(c.allIDs)
		} else {
			c.callbacks.OnEnableAll(targetIDs)
		}
		c.refresh()

	case tui.MatchesKey(data, tui.KeyCtrl("x")):
		var targetIDs []string
		if c.searchInput.GetValue() != "" {
			for _, item := range c.filteredItems {
				targetIDs = append(targetIDs, item.FullID)
			}
		}
		c.clearAll(targetIDs)
		c.isDirty = true
		c.callbacks.OnClearAll()
		c.refresh()

	case tui.MatchesKey(data, tui.KeyCtrl("p")):
		if c.selectedIndex >= 0 && c.selectedIndex < len(c.filteredItems) {
			item := c.filteredItems[c.selectedIndex]
			provider := item.Model.Provider
			var providerIDs []string
			for _, id := range c.allIDs {
				if m, ok := c.modelsById[id]; ok && m.Provider == provider {
					providerIDs = append(providerIDs, id)
				}
			}
			allProviderEnabled := true
			for _, id := range providerIDs {
				if !c.isEnabled(id) {
					allProviderEnabled = false
					break
				}
			}
			if allProviderEnabled {
				c.clearAll(providerIDs)
			} else {
				c.enableAll(providerIDs)
			}
			c.isDirty = true
			c.callbacks.OnToggleProvider(provider, providerIDs, !allProviderEnabled)
			c.refresh()
		}

	case tui.MatchesKey(data, tui.KeyCtrl("s")):
		ids := c.enabledIDs
		if c.allEnabled {
			ids = append([]string(nil), c.allIDs...)
		}
		c.callbacks.OnPersist(ids)
		c.isDirty = false
		c.footerText.SetText(c.getFooterText())

	case tui.MatchesKey(data, tui.KeyCtrl("c")):
		if c.searchInput.GetValue() != "" {
			c.searchInput.SetValue("")
			c.refresh()
		} else {
			c.callbacks.OnCancel()
		}

	case tuicomp.MatchesEditorAction(data, tuicomp.ActSelectCancel):
		c.callbacks.OnCancel()

	default:
		c.searchInput.HandleInput(data)
		c.refresh()
	}
}

// GetSearchInput returns the search input for focus management.
func (c *ScopedModelsSelectorComponent) GetSearchInput() *tuicomp.Input {
	return c.searchInput
}
