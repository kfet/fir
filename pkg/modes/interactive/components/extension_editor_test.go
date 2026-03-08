package components

import (
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/tui"
)

func TestExtensionEditorComponent_Render(t *testing.T) {
	kb := tui.NewKeybindingsManagerInMemory(nil)
	comp := NewExtensionEditorComponent(nil, kb, "Edit content", "prefilled", func(string) {}, func() {})
	lines := comp.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected rendered output")
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Edit content") {
		t.Errorf("expected title in output, got %q", joined)
	}
}
