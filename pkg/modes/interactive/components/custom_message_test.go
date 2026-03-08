package components

import (
	"strings"
	"testing"

	fmsg "github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/modes/interactive/theme"
	"github.com/kfet/fir/pkg/tui"
)

func TestCustomMessageComponent_DefaultRendering(t *testing.T) {
	msg := &fmsg.CustomMessage{
		Role:       "custom",
		CustomType: "myext",
		Content:    "Hello from extension",
		Display:    true,
	}

	comp := NewCustomMessageComponent(msg, nil, nil)
	lines := comp.Render(60)

	if len(lines) == 0 {
		t.Fatal("expected rendered output")
	}

	output := strings.Join(lines, "\n")
	if !strings.Contains(output, "[myext]") {
		t.Error("expected '[myext]' label")
	}
	if !strings.Contains(output, "Hello from extension") {
		t.Error("expected message content")
	}
}

func TestCustomMessageComponent_ArrayContent(t *testing.T) {
	msg := &fmsg.CustomMessage{
		Role:       "custom",
		CustomType: "ext",
		Content: []any{
			map[string]any{"type": "text", "text": "Part one"},
			map[string]any{"type": "text", "text": "Part two"},
		},
		Display: true,
	}

	comp := NewCustomMessageComponent(msg, nil, nil)
	lines := comp.Render(60)

	output := strings.Join(lines, "\n")
	if !strings.Contains(output, "Part one") {
		t.Error("expected 'Part one' in output")
	}
	if !strings.Contains(output, "Part two") {
		t.Error("expected 'Part two' in output")
	}
}

func TestCustomMessageComponent_CustomRenderer(t *testing.T) {
	msg := &fmsg.CustomMessage{
		Role:       "custom",
		CustomType: "special",
		Content:    "content",
		Display:    true,
	}

	renderer := func(m *fmsg.CustomMessage, expanded bool, theme *theme.Theme) tui.Component {
		return &mockComponent{lines: []string{"custom rendered"}}
	}

	comp := NewCustomMessageComponent(msg, renderer, nil)
	lines := comp.Render(60)

	output := strings.Join(lines, "\n")
	if !strings.Contains(output, "custom rendered") {
		t.Error("expected custom renderer output")
	}
}

func TestCustomMessageComponent_CustomRendererNil(t *testing.T) {
	msg := &fmsg.CustomMessage{
		Role:       "custom",
		CustomType: "fallback",
		Content:    "Fallback content",
		Display:    true,
	}

	renderer := func(m *fmsg.CustomMessage, expanded bool, theme *theme.Theme) tui.Component {
		return nil // signal to fall through to default
	}

	comp := NewCustomMessageComponent(msg, renderer, nil)
	lines := comp.Render(60)

	output := strings.Join(lines, "\n")
	if !strings.Contains(output, "[fallback]") {
		t.Error("expected default rendering with label")
	}
	if !strings.Contains(output, "Fallback content") {
		t.Error("expected fallback content")
	}
}

// mockComponent is a simple test component.
type mockComponent struct {
	lines []string
}

func (m *mockComponent) Render(width int) []string { return m.lines }
func (m *mockComponent) Invalidate()               {}
