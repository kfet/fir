package tui

import (
	"strings"
	"testing"
)

// simpleComponent renders fixed lines.
type simpleComponent struct {
	lines []string
}

func (s *simpleComponent) Render(width int) []string {
	return s.lines
}
func (s *simpleComponent) Invalidate() {}

// interactiveComponent handles input and renders it.
type interactiveComponent struct {
	text string
}

func (c *interactiveComponent) Render(width int) []string {
	return []string{c.text}
}
func (c *interactiveComponent) Invalidate() {}
func (c *interactiveComponent) HandleInput(data string) {
	c.text += data
}

func TestContainer_Empty(t *testing.T) {
	c := &Container{}
	lines := c.Render(80)
	if len(lines) != 0 {
		t.Errorf("expected no lines, got %d", len(lines))
	}
}

func TestContainer_AddChild(t *testing.T) {
	c := &Container{}
	c.AddChild(&simpleComponent{lines: []string{"hello"}})
	c.AddChild(&simpleComponent{lines: []string{"world"}})
	lines := c.Render(80)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if lines[0] != "hello" || lines[1] != "world" {
		t.Errorf("unexpected lines: %v", lines)
	}
}

func TestContainer_RemoveChild(t *testing.T) {
	c := &Container{}
	child := &simpleComponent{lines: []string{"remove me"}}
	c.AddChild(child)
	c.AddChild(&simpleComponent{lines: []string{"keep me"}})
	c.RemoveChild(child)
	lines := c.Render(80)
	if len(lines) != 1 || lines[0] != "keep me" {
		t.Errorf("unexpected after remove: %v", lines)
	}
}

func TestContainer_Clear(t *testing.T) {
	c := &Container{}
	c.AddChild(&simpleComponent{lines: []string{"a"}})
	c.AddChild(&simpleComponent{lines: []string{"b"}})
	c.Clear()
	if len(c.Render(80)) != 0 {
		t.Error("expected empty after clear")
	}
}

func TestTUI_NewTUI(t *testing.T) {
	term := NewMockTerminal(80, 24)
	ui := NewTUI(term, false)
	if ui.Terminal != term {
		t.Error("expected terminal to be set")
	}
	if ui.GetShowHardwareCursor() {
		t.Error("expected hardware cursor hidden by default")
	}
}

func TestTUI_SetFocus(t *testing.T) {
	term := NewMockTerminal(80, 24)
	ui := NewTUI(term, false)
	comp := &interactiveComponent{text: ""}
	ui.SetFocus(comp)
	// After input, focused component should receive it
	ui.handleInput("x")
	if comp.text != "x" {
		t.Errorf("expected 'x', got %q", comp.text)
	}
}

func TestTUI_DoRender_Basic(t *testing.T) {
	term := NewMockTerminal(80, 24)
	ui := NewTUI(term, false)
	ui.AddChild(&simpleComponent{lines: []string{"line1", "line2"}})
	ui.DoRender()
	output := strings.Join(term.GetOutput(), "")
	if !strings.Contains(output, "line1") || !strings.Contains(output, "line2") {
		t.Errorf("expected output to contain lines, got %q", output)
	}
}

func TestTUI_DoRender_DiffUpdate(t *testing.T) {
	term := NewMockTerminal(80, 24)
	ui := NewTUI(term, false)
	comp := &simpleComponent{lines: []string{"line1", "line2"}}
	ui.AddChild(comp)
	ui.DoRender()
	term.ClearOutput()

	// Change second line
	comp.lines = []string{"line1", "line2-changed"}
	ui.DoRender()

	output := strings.Join(term.GetOutput(), "")
	if !strings.Contains(output, "line2-changed") {
		t.Errorf("expected diff update with 'line2-changed', got %q", output)
	}
}

func TestTUI_DoRender_NoChange(t *testing.T) {
	term := NewMockTerminal(80, 24)
	ui := NewTUI(term, false)
	ui.AddChild(&simpleComponent{lines: []string{"stable"}})
	ui.DoRender()
	term.ClearOutput()

	// Render again with no changes
	ui.DoRender()
	output := term.GetOutput()
	// Should have minimal output (no content writes, maybe cursor positioning)
	totalLen := 0
	for _, s := range output {
		totalLen += len(s)
	}
	if totalLen > 100 {
		t.Errorf("expected minimal output for no change, got %d bytes", totalLen)
	}
}

func TestTUI_Overlay_Basic(t *testing.T) {
	term := NewMockTerminal(80, 24)
	ui := NewTUI(term, false)
	ui.AddChild(&simpleComponent{lines: []string{"background"}})

	overlay := &simpleComponent{lines: []string{"overlay"}}
	handle := ui.ShowOverlay(overlay, nil)
	if !ui.HasOverlay() {
		t.Error("expected overlay")
	}

	handle.Hide()
	if ui.HasOverlay() {
		t.Error("expected no overlay after hide")
	}
}

func TestTUI_Overlay_SetHidden(t *testing.T) {
	term := NewMockTerminal(80, 24)
	ui := NewTUI(term, false)
	overlay := &simpleComponent{lines: []string{"overlay"}}
	handle := ui.ShowOverlay(overlay, nil)

	handle.SetHidden(true)
	if !handle.IsHidden() {
		t.Error("expected hidden")
	}
	if ui.HasOverlay() {
		t.Error("expected no visible overlay when hidden")
	}

	handle.SetHidden(false)
	if handle.IsHidden() {
		t.Error("expected not hidden")
	}
	if !ui.HasOverlay() {
		t.Error("expected visible overlay")
	}

	handle.Hide()
}

func TestTUI_ShowHardwareCursor(t *testing.T) {
	term := NewMockTerminal(80, 24)
	ui := NewTUI(term, false)
	ui.SetShowHardwareCursor(true)
	if !ui.GetShowHardwareCursor() {
		t.Error("expected hardware cursor enabled")
	}
	ui.SetShowHardwareCursor(false)
	if ui.GetShowHardwareCursor() {
		t.Error("expected hardware cursor disabled")
	}
}

func TestTUI_RequestRender_Force(t *testing.T) {
	term := NewMockTerminal(80, 24)
	ui := NewTUI(term, false)
	ui.AddChild(&simpleComponent{lines: []string{"test"}})
	ui.DoRender()

	before := ui.FullRedraws()
	ui.RequestRender(true)
	ui.DoRender()
	after := ui.FullRedraws()

	if after <= before {
		t.Error("expected forced render to increment fullRedraws")
	}
}

func TestTUI_HideOverlay(t *testing.T) {
	term := NewMockTerminal(80, 24)
	ui := NewTUI(term, false)
	ui.ShowOverlay(&simpleComponent{lines: []string{"o1"}}, nil)
	ui.ShowOverlay(&simpleComponent{lines: []string{"o2"}}, nil)
	if !ui.HasOverlay() {
		t.Error("expected overlays")
	}
	ui.HideOverlay() // removes top
	if !ui.HasOverlay() {
		t.Error("expected one overlay remaining")
	}
	ui.HideOverlay() // removes last
	if ui.HasOverlay() {
		t.Error("expected no overlays")
	}
}

func TestParseSizeValue(t *testing.T) {
	sv, ok := ParseSizeValue("50%")
	if !ok || !sv.IsPercent || sv.Percent != 50 {
		t.Errorf("expected 50%%, got %v", sv)
	}

	sv, ok = ParseSizeValue("80")
	if !ok || sv.IsPercent || sv.Absolute != 80 {
		t.Errorf("expected 80, got %v", sv)
	}

	_, ok = ParseSizeValue("abc")
	if ok {
		t.Error("expected parse failure for 'abc'")
	}
}

func TestResolveAnchorRow(t *testing.T) {
	row := resolveAnchorRow(AnchorCenter, 10, 24, 0)
	if row != 7 { // (24-10)/2 = 7
		t.Errorf("expected 7, got %d", row)
	}

	row = resolveAnchorRow(AnchorTopLeft, 10, 24, 2)
	if row != 2 {
		t.Errorf("expected 2, got %d", row)
	}

	row = resolveAnchorRow(AnchorBottomRight, 10, 24, 0)
	if row != 14 { // 0 + 24 - 10
		t.Errorf("expected 14, got %d", row)
	}
}

func TestResolveAnchorCol(t *testing.T) {
	col := resolveAnchorCol(AnchorCenter, 40, 80, 0)
	if col != 20 { // (80-40)/2 = 20
		t.Errorf("expected 20, got %d", col)
	}
}

func TestExtractCursorPosition(t *testing.T) {
	term := NewMockTerminal(80, 24)
	ui := NewTUI(term, false)

	lines := []string{
		"no marker here",
		"before" + CursorMarker + "after",
		"last line",
	}

	pos := ui.extractCursorPosition(lines, 24)
	if pos == nil {
		t.Fatal("expected cursor position")
	}
	if pos.row != 1 {
		t.Errorf("expected row 1, got %d", pos.row)
	}
	if pos.col != 6 { // "before" is 6 chars
		t.Errorf("expected col 6, got %d", pos.col)
	}
	// Marker should be stripped
	if strings.Contains(lines[1], CursorMarker) {
		t.Error("marker should be stripped from line")
	}
}

func TestCompositeLineAt(t *testing.T) {
	term := NewMockTerminal(80, 24)
	ui := NewTUI(term, false)

	base := "hello world this is base"
	overlay := "OVER"
	result := ui.compositeLineAt(base, overlay, 6, 4, 80)

	// Should contain the overlay text
	if !strings.Contains(result, "OVER") {
		t.Errorf("expected 'OVER' in result, got %q", result)
	}
	// Should contain 'hello ' at start
	if !strings.HasPrefix(result, "hello ") {
		t.Errorf("expected 'hello ' prefix, got %q", result)
	}
}

func TestTUI_HandleInput_BufferedBackspaces(t *testing.T) {
	// Regression test: when renderMu is held during a render, the OS stdin
	// buffer can accumulate multiple backspaces.  The next Read returns all of
	// them as one string (e.g. "\x7f\x7f\x7f").  Before the SplitKeySequences
	// fix, MatchesKey required an exact single-sequence match and silently
	// dropped all but the first backspace, causing visible "lag".
	term := NewMockTerminal(80, 24)
	ui := NewTUI(term, false)

	received := []string{}
	comp := &trackingInputHandler{received: &received}
	ui.SetFocus(comp)

	// Simulate three backspaces arriving as one buffered read.
	ui.handleInput("\x7f\x7f\x7f")

	if len(received) != 3 {
		t.Fatalf("expected 3 separate HandleInput calls, got %d: %v", len(received), received)
	}
	for i, s := range received {
		if s != "\x7f" {
			t.Errorf("call[%d]: expected backspace (\\x7f), got %q", i, s)
		}
	}
}

// trackingInputHandler records every HandleInput call.
type trackingInputHandler struct {
	received *[]string
}

func (h *trackingInputHandler) Render(width int) []string { return []string{""} }
func (h *trackingInputHandler) Invalidate()               {}
func (h *trackingInputHandler) HandleInput(data string)   { *h.received = append(*h.received, data) }

// Test helpers — moved here from tui.go; only used by tests.

func (h *OverlayHandle) SetHidden(hidden bool) {
	h.mu.Lock()
	if h.entry.hidden == hidden {
		h.mu.Unlock()
		return
	}
	h.entry.hidden = hidden
	h.mu.Unlock()
	h.tui.RequestRender(false)
}

func (t *TUI) SetShowHardwareCursor(enabled bool) {
	if t.showHardwareCursor == enabled {
		return
	}
	t.showHardwareCursor = enabled
	if !enabled {
		t.Terminal.HideCursor()
	}
	t.RequestRender(false)
}

func (t *TUI) ShowOverlay(component Component, options *OverlayOptions) *OverlayHandle {
	entry := &overlayEntry{
		component: component,
		options:   options,
		preFocus:  t.focusedComponent,
	}
	t.overlayStack = append(t.overlayStack, entry)
	if t.isOverlayVisible(entry) {
		t.SetFocus(component)
	}
	t.Terminal.HideCursor()
	t.RequestRender(false)
	return &OverlayHandle{tui: t, entry: entry}
}

func (t *TUI) HideOverlay() {
	if len(t.overlayStack) == 0 {
		return
	}
	overlay := t.overlayStack[len(t.overlayStack)-1]
	t.overlayStack = t.overlayStack[:len(t.overlayStack)-1]
	if top := t.getTopmostVisibleOverlay(); top != nil {
		t.SetFocus(top.component)
	} else {
		t.SetFocus(overlay.preFocus)
	}
	if len(t.overlayStack) == 0 {
		t.Terminal.HideCursor()
	}
	t.RequestRender(false)
}

func (t *TUI) HasOverlay() bool {
	for _, o := range t.overlayStack {
		if t.isOverlayVisible(o) {
			return true
		}
	}
	return false
}
