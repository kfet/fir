package components

import (
	"strings"
	"testing"
)

func slIdentity(s string) string { return s }

func testTheme() SelectListTheme {
	return SelectListTheme{
		SelectedPrefix: slIdentity,
		SelectedText:   slIdentity,
		Description:    slIdentity,
		ScrollInfo:     slIdentity,
		NoMatch:        slIdentity,
	}
}

func testItems() []SelectItem {
	return []SelectItem{
		{Value: "apple", Label: "Apple", Description: "A fruit"},
		{Value: "banana", Label: "Banana", Description: "Yellow fruit"},
		{Value: "cherry", Label: "Cherry"},
		{Value: "date", Label: "Date"},
		{Value: "elderberry", Label: "Elderberry"},
	}
}

func TestSelectList_Render_Basic(t *testing.T) {
	sl := NewSelectList(testItems(), 3, testTheme())
	lines := sl.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected lines")
	}
	// First item should be selected (has → prefix)
	if !strings.Contains(lines[0], "→") {
		t.Errorf("expected → in first line, got %q", lines[0])
	}
}

func TestSelectList_Render_NoItems(t *testing.T) {
	sl := NewSelectList(nil, 3, testTheme())
	lines := sl.Render(80)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "No matching") {
		t.Errorf("expected 'No matching' message, got %q", lines[0])
	}
}

func TestSelectList_Filter(t *testing.T) {
	sl := NewSelectList(testItems(), 5, testTheme())
	sl.SetFilter("b")
	item := sl.GetSelectedItem()
	if item == nil || item.Value != "banana" {
		t.Errorf("expected 'banana' after filter 'b', got %v", item)
	}
}

func TestSelectList_Filter_NoMatch(t *testing.T) {
	sl := NewSelectList(testItems(), 5, testTheme())
	sl.SetFilter("xyz")
	item := sl.GetSelectedItem()
	if item != nil {
		t.Errorf("expected nil for no match, got %v", item)
	}
	lines := sl.Render(80)
	if !strings.Contains(lines[0], "No matching") {
		t.Errorf("expected 'No matching' message")
	}
}

func TestSelectList_SetSelectedIndex(t *testing.T) {
	sl := NewSelectList(testItems(), 5, testTheme())
	sl.SetSelectedIndex(2)
	item := sl.GetSelectedItem()
	if item == nil || item.Value != "cherry" {
		t.Errorf("expected 'cherry', got %v", item)
	}
}

func TestSelectList_SetSelectedIndex_Clamped(t *testing.T) {
	sl := NewSelectList(testItems(), 5, testTheme())
	sl.SetSelectedIndex(100)
	item := sl.GetSelectedItem()
	if item == nil || item.Value != "elderberry" {
		t.Errorf("expected last item, got %v", item)
	}
	sl.SetSelectedIndex(-5)
	item = sl.GetSelectedItem()
	if item == nil || item.Value != "apple" {
		t.Errorf("expected first item, got %v", item)
	}
}

func TestSelectList_HandleInput_Down(t *testing.T) {
	sl := NewSelectList(testItems(), 5, testTheme())
	sl.HandleInput("\x1b[B") // down arrow
	item := sl.GetSelectedItem()
	if item == nil || item.Value != "banana" {
		t.Errorf("expected 'banana' after down, got %v", item)
	}
}

func TestSelectList_HandleInput_Up_Wrap(t *testing.T) {
	sl := NewSelectList(testItems(), 5, testTheme())
	sl.HandleInput("\x1b[A") // up arrow - wraps to last
	item := sl.GetSelectedItem()
	if item == nil || item.Value != "elderberry" {
		t.Errorf("expected 'elderberry' after up wrap, got %v", item)
	}
}

func TestSelectList_HandleInput_Down_Wrap(t *testing.T) {
	sl := NewSelectList(testItems(), 5, testTheme())
	sl.SetSelectedIndex(4)
	sl.HandleInput("\x1b[B") // down arrow - wraps to first
	item := sl.GetSelectedItem()
	if item == nil || item.Value != "apple" {
		t.Errorf("expected 'apple' after down wrap, got %v", item)
	}
}

func TestSelectList_HandleInput_Enter(t *testing.T) {
	sl := NewSelectList(testItems(), 5, testTheme())
	var selected *SelectItem
	sl.OnSelect = func(item SelectItem) {
		selected = &item
	}
	sl.HandleInput("\r") // enter
	if selected == nil || selected.Value != "apple" {
		t.Errorf("expected 'apple' selected on enter, got %v", selected)
	}
}

func TestSelectList_HandleInput_Escape(t *testing.T) {
	sl := NewSelectList(testItems(), 5, testTheme())
	cancelled := false
	sl.OnCancel = func() {
		cancelled = true
	}
	sl.HandleInput("\x1b") // escape
	if !cancelled {
		t.Error("expected cancel on escape")
	}
}

func TestSelectList_ScrollIndicator(t *testing.T) {
	sl := NewSelectList(testItems(), 3, testTheme())
	lines := sl.Render(80)
	// With 5 items and maxVisible=3, should have scroll indicator
	lastLine := lines[len(lines)-1]
	if !strings.Contains(lastLine, "/") {
		t.Errorf("expected scroll indicator (x/y), got %q", lastLine)
	}
}

func TestSelectList_Description(t *testing.T) {
	sl := NewSelectList(testItems(), 5, testTheme())
	lines := sl.Render(80) // Wide enough for descriptions
	// First item (Apple) has description "A fruit" and is selected
	if !strings.Contains(lines[0], "A fruit") {
		t.Errorf("expected description 'A fruit' in line, got %q", lines[0])
	}
}

func TestSelectList_NarrowWidth(t *testing.T) {
	sl := NewSelectList(testItems(), 5, testTheme())
	lines := sl.Render(30) // Narrow - should not show descriptions
	if len(lines) == 0 {
		t.Fatal("expected lines")
	}
	// Just check it doesn't panic
}

func TestSelectList_OnSelectionChange(t *testing.T) {
	sl := NewSelectList(testItems(), 5, testTheme())
	var changed *SelectItem
	sl.OnSelectionChange = func(item SelectItem) {
		changed = &item
	}
	sl.HandleInput("\x1b[B") // down arrow
	if changed == nil || changed.Value != "banana" {
		t.Errorf("expected 'banana' change notification, got %v", changed)
	}
}

func TestSelectList_Invalidate(t *testing.T) {
	sl := NewSelectList(testItems(), 5, testTheme())
	sl.Invalidate() // should not panic
}
