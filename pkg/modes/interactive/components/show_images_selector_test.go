package components

import (
	"testing"
)

func TestShowImagesSelectorComponent_Render(t *testing.T) {
	selected := false
	cancelled := false
	comp := NewShowImagesSelectorComponent(true, func(show bool) {
		selected = true
	}, func() {
		cancelled = true
	})
	lines := comp.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected rendered output")
	}
	_ = selected
	_ = cancelled
}

func TestShowImagesSelectorComponent_GetSelectList(t *testing.T) {
	comp := NewShowImagesSelectorComponent(false, func(bool) {}, func() {})
	sl := comp.GetSelectList()
	if sl == nil {
		t.Fatal("expected non-nil SelectList")
	}
}
