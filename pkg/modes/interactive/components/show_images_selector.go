// Ported from: packages/coding-agent/src/modes/interactive/components/show-images-selector.ts
// Upstream hash: 1caadb2e
package components

import (
	"github.com/kfet/tau/pkg/modes/interactive/theme"
	"github.com/kfet/tau/pkg/tui"
	tuicomp "github.com/kfet/tau/pkg/tui/components"
)

// ShowImagesSelectorComponent renders a show images selector with borders.
type ShowImagesSelectorComponent struct {
	tui.Container
	selectList *tuicomp.SelectList
}

// NewShowImagesSelectorComponent creates a new ShowImagesSelectorComponent.
func NewShowImagesSelectorComponent(currentValue bool, onSelect func(show bool), onCancel func()) *ShowImagesSelectorComponent {
	items := []tuicomp.SelectItem{
		{Value: "yes", Label: "Yes", Description: "Show images inline in terminal"},
		{Value: "no", Label: "No", Description: "Show text placeholder instead"},
	}

	s := &ShowImagesSelectorComponent{}

	// Add top border
	s.AddChild(NewDynamicBorder(nil))

	// Create selector
	s.selectList = tuicomp.NewSelectList(items, 5, theme.GetSelectListTheme())

	// Preselect current value
	if currentValue {
		s.selectList.SetSelectedIndex(0)
	} else {
		s.selectList.SetSelectedIndex(1)
	}

	s.selectList.OnSelect = func(item tuicomp.SelectItem) {
		onSelect(item.Value == "yes")
	}
	s.selectList.OnCancel = onCancel

	s.AddChild(s.selectList)

	// Add bottom border
	s.AddChild(NewDynamicBorder(nil))

	return s
}

// GetSelectList returns the underlying SelectList.
func (s *ShowImagesSelectorComponent) GetSelectList() *tuicomp.SelectList {
	return s.selectList
}
