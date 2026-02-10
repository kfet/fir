package components

import (
	"strings"
	"testing"
)

func TestUserMessageSelectorComponent_Render(t *testing.T) {
	messages := []UserMessageItem{
		{ID: "1", Text: "First message"},
		{ID: "2", Text: "Second message"},
		{ID: "3", Text: "Third message"},
	}

	comp := NewUserMessageSelectorComponent(messages, func(string) {}, func() {})
	lines := comp.Render(80)

	if len(lines) == 0 {
		t.Fatal("expected rendered output")
	}

	output := strings.Join(lines, "\n")
	if !strings.Contains(output, "Branch from Message") {
		t.Error("expected header text")
	}
}

func TestUserMessageList_Navigation(t *testing.T) {
	messages := []UserMessageItem{
		{ID: "1", Text: "First"},
		{ID: "2", Text: "Second"},
		{ID: "3", Text: "Third"},
	}

	list := NewUserMessageList(messages)

	// Should start at last message
	if list.selectedIndex != 2 {
		t.Errorf("selectedIndex = %d, want 2", list.selectedIndex)
	}

	// Up arrow
	list.HandleInput("\x1b[A") // up
	if list.selectedIndex != 1 {
		t.Errorf("after up: selectedIndex = %d, want 1", list.selectedIndex)
	}

	// Down arrow
	list.HandleInput("\x1b[B") // down
	if list.selectedIndex != 2 {
		t.Errorf("after down: selectedIndex = %d, want 2", list.selectedIndex)
	}

	// Wrap around down
	list.HandleInput("\x1b[B") // down wraps to 0
	if list.selectedIndex != 0 {
		t.Errorf("after wrap down: selectedIndex = %d, want 0", list.selectedIndex)
	}

	// Wrap around up
	list.HandleInput("\x1b[A") // up wraps to 2
	if list.selectedIndex != 2 {
		t.Errorf("after wrap up: selectedIndex = %d, want 2", list.selectedIndex)
	}
}

func TestUserMessageList_Select(t *testing.T) {
	messages := []UserMessageItem{
		{ID: "entry-1", Text: "Hello"},
	}

	var selectedID string
	list := NewUserMessageList(messages)
	list.OnSelect = func(id string) { selectedID = id }

	list.HandleInput("\r") // enter
	if selectedID != "entry-1" {
		t.Errorf("selectedID = %q, want %q", selectedID, "entry-1")
	}
}

func TestUserMessageList_Cancel(t *testing.T) {
	messages := []UserMessageItem{
		{ID: "1", Text: "Hello"},
	}

	cancelled := false
	list := NewUserMessageList(messages)
	list.OnCancel = func() { cancelled = true }

	list.HandleInput("\x1b") // escape
	if !cancelled {
		t.Error("expected cancel callback")
	}
}

func TestUserMessageList_Empty(t *testing.T) {
	list := NewUserMessageList(nil)

	lines := list.Render(60)
	output := strings.Join(lines, "\n")
	if !strings.Contains(output, "No user messages") {
		t.Error("expected 'No user messages' for empty list")
	}
}
