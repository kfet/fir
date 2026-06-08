// Ported from: packages/coding-agent/src/modes/interactive/components/thinking-selector.ts
// Upstream hash: 1caadb2e
package components

import (
	"github.com/kfet/agent"
	"github.com/kfet/fir/pkg/modes/interactive/theme"
	"github.com/kfet/fir/pkg/tui"
	tuicomp "github.com/kfet/fir/pkg/tui/components"
)

// levelDescriptions maps thinking levels to human-readable descriptions.
var levelDescriptions = map[agent.ThinkingLevel]string{
	agent.ThinkingOff:     "No reasoning",
	agent.ThinkingMinimal: "Very brief reasoning (~1k tokens)",
	agent.ThinkingLow:     "Light reasoning (~2k tokens)",
	agent.ThinkingMedium:  "Moderate reasoning (~8k tokens)",
	agent.ThinkingHigh:    "Deep reasoning (~16k tokens)",
	agent.ThinkingXHigh:   "Maximum reasoning (~32k tokens)",
}

// ThinkingSelectorComponent renders a thinking level selector with borders.
type ThinkingSelectorComponent struct {
	tui.Container
	selectList *tuicomp.SelectList
}

// NewThinkingSelectorComponent creates a new ThinkingSelectorComponent.
func NewThinkingSelectorComponent(
	currentLevel agent.ThinkingLevel,
	availableLevels []agent.ThinkingLevel,
	onSelect func(level agent.ThinkingLevel),
	onCancel func(),
) *ThinkingSelectorComponent {
	items := make([]tuicomp.SelectItem, len(availableLevels))
	for i, level := range availableLevels {
		desc := levelDescriptions[level]
		items[i] = tuicomp.SelectItem{
			Value:       string(level),
			Label:       string(level),
			Description: desc,
		}
	}

	s := &ThinkingSelectorComponent{}

	// Add top border
	s.AddChild(NewDynamicBorder(nil))

	// Create selector
	s.selectList = tuicomp.NewSelectList(items, len(items), theme.GetSelectListTheme())

	// Preselect current level
	for i, level := range availableLevels {
		if level == currentLevel {
			s.selectList.SetSelectedIndex(i)
			break
		}
	}

	s.selectList.OnSelect = func(item tuicomp.SelectItem) {
		onSelect(agent.ThinkingLevel(item.Value))
	}
	s.selectList.OnCancel = onCancel

	s.AddChild(s.selectList)

	// Add bottom border
	s.AddChild(NewDynamicBorder(nil))

	return s
}

// GetSelectList returns the underlying SelectList.
func (s *ThinkingSelectorComponent) GetSelectList() *tuicomp.SelectList {
	return s.selectList
}
