// Ported from: packages/coding-agent/src/modes/interactive/components/theme-selector.ts
// Upstream hash: 1caadb2e
package components

import (
	"github.com/kfet/pi-go/pkg/modes/interactive/theme"
	"github.com/kfet/pi-go/pkg/tui"
	tuicomp "github.com/kfet/pi-go/pkg/tui/components"
)

// ThemeSelectorComponent renders a theme selector.
type ThemeSelectorComponent struct {
	tui.Container
	selectList *tuicomp.SelectList
}

// NewThemeSelectorComponent creates a new ThemeSelectorComponent.
// searchDirs are directories to search for theme files.
func NewThemeSelectorComponent(
	currentTheme string,
	searchDirs []string,
	onSelect func(themeName string),
	onCancel func(),
	onPreview func(themeName string),
) *ThemeSelectorComponent {
	themes := theme.GetAvailableThemes(searchDirs)
	items := make([]tuicomp.SelectItem, len(themes))
	for i, name := range themes {
		desc := ""
		if name == currentTheme {
			desc = "(current)"
		}
		items[i] = tuicomp.SelectItem{
			Value:       name,
			Label:       name,
			Description: desc,
		}
	}

	s := &ThemeSelectorComponent{}

	// Add top border
	s.AddChild(NewDynamicBorder(nil))

	// Create selector
	s.selectList = tuicomp.NewSelectList(items, 10, theme.GetSelectListTheme())

	// Preselect current theme
	for i, name := range themes {
		if name == currentTheme {
			s.selectList.SetSelectedIndex(i)
			break
		}
	}

	s.selectList.OnSelect = func(item tuicomp.SelectItem) {
		onSelect(item.Value)
	}
	s.selectList.OnCancel = onCancel
	s.selectList.OnSelectionChange = func(item tuicomp.SelectItem) {
		onPreview(item.Value)
	}

	s.AddChild(s.selectList)

	// Add bottom border
	s.AddChild(NewDynamicBorder(nil))

	return s
}

// GetSelectList returns the underlying SelectList.
func (s *ThemeSelectorComponent) GetSelectList() *tuicomp.SelectList {
	return s.selectList
}
