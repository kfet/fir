package components

import (
	"testing"
)

func TestThemeSelectorComponent_Render(t *testing.T) {
	comp := NewThemeSelectorComponent("default", nil, func(string) {}, func() {}, func(string) {})
	lines := comp.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected rendered output")
	}
}

func TestThemeSelectorComponent_GetSelectList(t *testing.T) {
	comp := NewThemeSelectorComponent("default", nil, func(string) {}, func() {}, func(string) {})
	sl := comp.GetSelectList()
	if sl == nil {
		t.Fatal("expected non-nil SelectList")
	}
}
