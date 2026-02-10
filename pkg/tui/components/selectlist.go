// Ported from: packages/tui/src/components/select-list.ts
// Upstream hash: 1caadb2e
package components

import (
	"strconv"
	"strings"

	"github.com/kfet/pi-go/pkg/tui"
)

// SelectItem represents a selectable item in the list.
type SelectItem struct {
	Value       string
	Label       string
	Description string
}

// SelectListTheme provides styling functions for the select list.
type SelectListTheme struct {
	SelectedPrefix func(string) string
	SelectedText   func(string) string
	Description    func(string) string
	ScrollInfo     func(string) string
	NoMatch        func(string) string
}

// SelectList is a scrollable select list component.
type SelectList struct {
	items         []SelectItem
	filteredItems []SelectItem
	selectedIndex int
	maxVisible    int
	theme         SelectListTheme

	OnSelect          func(item SelectItem)
	OnCancel          func()
	OnSelectionChange func(item SelectItem)
}

// NewSelectList creates a new SelectList.
func NewSelectList(items []SelectItem, maxVisible int, theme SelectListTheme) *SelectList {
	filtered := make([]SelectItem, len(items))
	copy(filtered, items)
	return &SelectList{
		items:         items,
		filteredItems: filtered,
		maxVisible:    maxVisible,
		theme:         theme,
	}
}

func normalizeToSingleLine(text string) string {
	// Replace \r\n and \n with space, then trim
	result := strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(text)
	return strings.TrimSpace(result)
}

// SetFilter filters items by prefix match on value.
func (s *SelectList) SetFilter(filter string) {
	filterLower := strings.ToLower(filter)
	s.filteredItems = nil
	for _, item := range s.items {
		if strings.HasPrefix(strings.ToLower(item.Value), filterLower) {
			s.filteredItems = append(s.filteredItems, item)
		}
	}
	s.selectedIndex = 0
}

// SetSelectedIndex sets the selected index, clamped to valid range.
func (s *SelectList) SetSelectedIndex(index int) {
	if index < 0 {
		index = 0
	}
	if index >= len(s.filteredItems) {
		index = len(s.filteredItems) - 1
	}
	if index < 0 {
		index = 0
	}
	s.selectedIndex = index
}

// Invalidate is a no-op for SelectList.
func (s *SelectList) Invalidate() {}

// Render renders the select list to lines.
func (s *SelectList) Render(width int) []string {
	var lines []string

	if len(s.filteredItems) == 0 {
		lines = append(lines, s.theme.NoMatch("  No matching commands"))
		return lines
	}

	// Calculate visible range with scrolling
	startIndex := s.selectedIndex - s.maxVisible/2
	maxStart := len(s.filteredItems) - s.maxVisible
	if startIndex > maxStart {
		startIndex = maxStart
	}
	if startIndex < 0 {
		startIndex = 0
	}
	endIndex := startIndex + s.maxVisible
	if endIndex > len(s.filteredItems) {
		endIndex = len(s.filteredItems)
	}

	for i := startIndex; i < endIndex; i++ {
		item := s.filteredItems[i]
		isSelected := i == s.selectedIndex
		desc := ""
		if item.Description != "" {
			desc = normalizeToSingleLine(item.Description)
		}

		displayValue := item.Label
		if displayValue == "" {
			displayValue = item.Value
		}

		var line string
		if isSelected {
			prefixWidth := 2 // "→ "
			if desc != "" && width > 40 {
				maxValueWidth := 30
				if width-prefixWidth-4 < maxValueWidth {
					maxValueWidth = width - prefixWidth - 4
				}
				truncatedValue := tui.TruncateToWidth(displayValue, maxValueWidth, "", false)
				spacingLen := 32 - len(truncatedValue)
				if spacingLen < 1 {
					spacingLen = 1
				}
				spacing := strings.Repeat(" ", spacingLen)

				descriptionStart := prefixWidth + len(truncatedValue) + spacingLen
				remainingWidth := width - descriptionStart - 2

				if remainingWidth > 10 {
					truncatedDesc := tui.TruncateToWidth(desc, remainingWidth, "", false)
					line = s.theme.SelectedText("→ " + truncatedValue + spacing + truncatedDesc)
				} else {
					maxWidth := width - prefixWidth - 2
					line = s.theme.SelectedText("→ " + tui.TruncateToWidth(displayValue, maxWidth, "", false))
				}
			} else {
				maxWidth := width - prefixWidth - 2
				line = s.theme.SelectedText("→ " + tui.TruncateToWidth(displayValue, maxWidth, "", false))
			}
		} else {
			prefix := "  "
			if desc != "" && width > 40 {
				maxValueWidth := 30
				if width-len(prefix)-4 < maxValueWidth {
					maxValueWidth = width - len(prefix) - 4
				}
				truncatedValue := tui.TruncateToWidth(displayValue, maxValueWidth, "", false)
				spacingLen := 32 - len(truncatedValue)
				if spacingLen < 1 {
					spacingLen = 1
				}
				spacing := strings.Repeat(" ", spacingLen)

				descriptionStart := len(prefix) + len(truncatedValue) + spacingLen
				remainingWidth := width - descriptionStart - 2

				if remainingWidth > 10 {
					truncatedDesc := tui.TruncateToWidth(desc, remainingWidth, "", false)
					descText := s.theme.Description(spacing + truncatedDesc)
					line = prefix + truncatedValue + descText
				} else {
					maxWidth := width - len(prefix) - 2
					line = prefix + tui.TruncateToWidth(displayValue, maxWidth, "", false)
				}
			} else {
				maxWidth := width - len(prefix) - 2
				line = prefix + tui.TruncateToWidth(displayValue, maxWidth, "", false)
			}
		}

		lines = append(lines, line)
	}

	// Add scroll indicators if needed
	if startIndex > 0 || endIndex < len(s.filteredItems) {
		scrollText := tui.TruncateToWidth(
			"  ("+itoa(s.selectedIndex+1)+"/"+itoa(len(s.filteredItems))+")",
			width-2, "...", false,
		)
		lines = append(lines, s.theme.ScrollInfo(scrollText))
	}

	return lines
}

// HandleInput processes keyboard input for navigation/selection.
func (s *SelectList) HandleInput(keyData string) {
	if tui.MatchesKey(keyData, "up") {
		if s.selectedIndex == 0 {
			s.selectedIndex = len(s.filteredItems) - 1
		} else {
			s.selectedIndex--
		}
		s.notifySelectionChange()
	} else if tui.MatchesKey(keyData, "down") {
		if s.selectedIndex == len(s.filteredItems)-1 {
			s.selectedIndex = 0
		} else {
			s.selectedIndex++
		}
		s.notifySelectionChange()
	} else if tui.MatchesKey(keyData, "enter") {
		if s.selectedIndex >= 0 && s.selectedIndex < len(s.filteredItems) && s.OnSelect != nil {
			s.OnSelect(s.filteredItems[s.selectedIndex])
		}
	} else if tui.MatchesKey(keyData, "escape") || tui.MatchesKey(keyData, tui.KeyCtrl("c")) {
		if s.OnCancel != nil {
			s.OnCancel()
		}
	}
}

func (s *SelectList) notifySelectionChange() {
	if s.selectedIndex >= 0 && s.selectedIndex < len(s.filteredItems) && s.OnSelectionChange != nil {
		s.OnSelectionChange(s.filteredItems[s.selectedIndex])
	}
}

// GetSelectedItem returns the currently selected item, or nil.
func (s *SelectList) GetSelectedItem() *SelectItem {
	if s.selectedIndex >= 0 && s.selectedIndex < len(s.filteredItems) {
		item := s.filteredItems[s.selectedIndex]
		return &item
	}
	return nil
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
