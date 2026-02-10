// Ported from: packages/tui/src/tui.ts
// Upstream hash: 1caadb2e
package tui

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Component interface for all TUI components.
type Component interface {
	Render(width int) []string
	Invalidate()
}

// InputHandler is a component that can receive keyboard input.
type InputHandler interface {
	HandleInput(data string)
}

// Focusable is a component that can receive focus and show a hardware cursor.
type Focusable interface {
	SetFocused(focused bool)
}

// IsFocusable checks if a component implements Focusable.
func IsFocusable(c Component) bool {
	_, ok := c.(Focusable)
	return ok
}

// OverlayAnchor defines anchor positions for overlays.
type OverlayAnchor string

const (
	AnchorCenter      OverlayAnchor = "center"
	AnchorTopLeft     OverlayAnchor = "top-left"
	AnchorTopRight    OverlayAnchor = "top-right"
	AnchorBottomLeft  OverlayAnchor = "bottom-left"
	AnchorBottomRight OverlayAnchor = "bottom-right"
	AnchorTopCenter   OverlayAnchor = "top-center"
	AnchorBottomCenter OverlayAnchor = "bottom-center"
	AnchorLeftCenter  OverlayAnchor = "left-center"
	AnchorRightCenter OverlayAnchor = "right-center"
)

// OverlayMargin defines margins for overlays.
type OverlayMargin struct {
	Top, Right, Bottom, Left int
}

// SizeValue is either an absolute number or a percentage string like "50%".
type SizeValue struct {
	Absolute   int
	Percent    float64
	IsPercent  bool
}

// NewAbsoluteSize creates an absolute SizeValue.
func NewAbsoluteSize(n int) SizeValue {
	return SizeValue{Absolute: n}
}

// NewPercentSize creates a percentage SizeValue.
func NewPercentSize(p float64) SizeValue {
	return SizeValue{Percent: p, IsPercent: true}
}

func (s SizeValue) resolve(reference int) int {
	if s.IsPercent {
		return int(math.Floor(float64(reference) * s.Percent / 100.0))
	}
	return s.Absolute
}

// OverlayOptions controls overlay positioning and sizing.
type OverlayOptions struct {
	Width     *SizeValue
	MinWidth  int
	MaxHeight *SizeValue
	Anchor    OverlayAnchor
	OffsetX   int
	OffsetY   int
	Row       *SizeValue
	Col       *SizeValue
	Margin    OverlayMargin
	Visible   func(termWidth, termHeight int) bool
}

// OverlayHandle controls an overlay's visibility.
type OverlayHandle struct {
	mu     sync.Mutex
	hidden bool
	tui    *TUI
	entry  *overlayEntry
}

func (h *OverlayHandle) Hide() {
	h.tui.removeOverlay(h.entry)
}

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

func (h *OverlayHandle) IsHidden() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.entry.hidden
}

type overlayEntry struct {
	component Component
	options   *OverlayOptions
	preFocus  Component
	hidden    bool
}

// Container holds child components.
type Container struct {
	Children []Component
}

func (c *Container) AddChild(component Component) {
	c.Children = append(c.Children, component)
}

func (c *Container) RemoveChild(component Component) {
	for i, child := range c.Children {
		if child == component {
			c.Children = append(c.Children[:i], c.Children[i+1:]...)
			return
		}
	}
}

func (c *Container) Clear() {
	c.Children = nil
}

func (c *Container) Invalidate() {
	for _, child := range c.Children {
		child.Invalidate()
	}
}

func (c *Container) Render(width int) []string {
	var lines []string
	for _, child := range c.Children {
		lines = append(lines, child.Render(width)...)
	}
	return lines
}

// CursorMarker is a zero-width APC sequence for hardware cursor positioning.
// Components emit this at the cursor position when focused.
// TUI finds and strips this marker, then positions the hardware cursor there.
const CursorMarker = "\x1b_pi:c\x07"

// segmentReset resets all ANSI styles and hyperlinks.
const segmentReset = "\x1b[0m\x1b]8;;\x07"

// TUI manages terminal UI with differential rendering.
type TUI struct {
	Container
	Terminal           Terminal
	OnDebug            func()

	mu                 sync.Mutex
	previousLines      []string
	previousWidth      int
	focusedComponent   Component
	renderRequested    bool
	cursorRow          int
	hardwareCursorRow  int
	showHardwareCursor bool
	clearOnShrink      bool
	maxLinesRendered   int
	previousViewportTop int
	fullRedrawCount    int
	stopped            bool
	overlayStack       []*overlayEntry
	renderCh           chan struct{}
}

// NewTUI creates a new TUI with the given terminal.
func NewTUI(terminal Terminal, showHardwareCursor ...bool) *TUI {
	show := false
	if len(showHardwareCursor) > 0 {
		show = showHardwareCursor[0]
	}
	t := &TUI{
		Terminal:           terminal,
		showHardwareCursor: show,
		renderCh:           make(chan struct{}, 1),
	}
	return t
}

func (t *TUI) FullRedraws() int {
	return t.fullRedrawCount
}

func (t *TUI) GetShowHardwareCursor() bool {
	return t.showHardwareCursor
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

func (t *TUI) SetClearOnShrink(enabled bool) {
	t.clearOnShrink = enabled
}

func (t *TUI) SetFocus(component Component) {
	if f, ok := t.focusedComponent.(Focusable); ok {
		f.SetFocused(false)
	}
	t.focusedComponent = component
	if f, ok := component.(Focusable); ok {
		f.SetFocused(true)
	}
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

func (t *TUI) removeOverlay(entry *overlayEntry) {
	for i, e := range t.overlayStack {
		if e == entry {
			t.overlayStack = append(t.overlayStack[:i], t.overlayStack[i+1:]...)
			if t.focusedComponent == entry.component {
				if top := t.getTopmostVisibleOverlay(); top != nil {
					t.SetFocus(top.component)
				} else {
					t.SetFocus(entry.preFocus)
				}
			}
			if len(t.overlayStack) == 0 {
				t.Terminal.HideCursor()
			}
			t.RequestRender(false)
			return
		}
	}
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

func (t *TUI) isOverlayVisible(entry *overlayEntry) bool {
	if entry.hidden {
		return false
	}
	if entry.options != nil && entry.options.Visible != nil {
		return entry.options.Visible(t.Terminal.Columns(), t.Terminal.Rows())
	}
	return true
}

func (t *TUI) getTopmostVisibleOverlay() *overlayEntry {
	for i := len(t.overlayStack) - 1; i >= 0; i-- {
		if t.isOverlayVisible(t.overlayStack[i]) {
			return t.overlayStack[i]
		}
	}
	return nil
}

func (t *TUI) Start() {
	t.stopped = false
	t.Terminal.Start(
		func(data string) { t.handleInput(data) },
		func() { t.RequestRender(false) },
	)
	t.Terminal.HideCursor()
	t.RequestRender(false)
}

func (t *TUI) Stop() {
	t.stopped = true
	if len(t.previousLines) > 0 {
		targetRow := len(t.previousLines)
		lineDiff := targetRow - t.hardwareCursorRow
		if lineDiff > 0 {
			t.Terminal.Write(fmt.Sprintf("\x1b[%dB", lineDiff))
		} else if lineDiff < 0 {
			t.Terminal.Write(fmt.Sprintf("\x1b[%dA", -lineDiff))
		}
		t.Terminal.Write("\r\n")
	}
	t.Terminal.ShowCursor()
	t.Terminal.Stop()
}

// RenderAdapter wraps a TUI to implement the components.RenderRequester interface
// (RequestRender with no args).
type RenderAdapter struct {
	TUI *TUI
}

// RequestRender implements the RenderRequester interface.
func (a *RenderAdapter) RequestRender() {
	if a.TUI != nil {
		a.TUI.RequestRender(false)
	}
}

// AsRenderRequester returns a RenderAdapter suitable for passing to Loader/CancellableLoader.
func (t *TUI) AsRenderRequester() *RenderAdapter {
	return &RenderAdapter{TUI: t}
}

func (t *TUI) RequestRender(force bool) {
	if force {
		t.previousLines = nil
		t.previousWidth = -1
		t.cursorRow = 0
		t.hardwareCursorRow = 0
		t.maxLinesRendered = 0
		t.previousViewportTop = 0
	}
	// Non-blocking signal
	select {
	case t.renderCh <- struct{}{}:
	default:
	}
}

func (t *TUI) handleInput(data string) {
	if MatchesKey(data, KeyCtrlShift("d")) && t.OnDebug != nil {
		t.OnDebug()
		return
	}

	focusedOverlay := t.findFocusedOverlay()
	if focusedOverlay != nil && !t.isOverlayVisible(focusedOverlay) {
		if top := t.getTopmostVisibleOverlay(); top != nil {
			t.SetFocus(top.component)
		} else {
			t.SetFocus(focusedOverlay.preFocus)
		}
	}

	if handler, ok := t.focusedComponent.(InputHandler); ok {
		if IsKeyRelease(data) {
			return
		}
		handler.HandleInput(data)
		t.RequestRender(false)
	}
}

func (t *TUI) findFocusedOverlay() *overlayEntry {
	for _, o := range t.overlayStack {
		if o.component == t.focusedComponent {
			return o
		}
	}
	return nil
}

// DoRender performs a single render cycle. Call from your event loop.
func (t *TUI) DoRender() {
	if t.stopped {
		return
	}
	width := t.Terminal.Columns()
	height := t.Terminal.Rows()
	viewportTop := max(0, t.maxLinesRendered-height)
	prevViewportTop := t.previousViewportTop
	hardwareCursorRow := t.hardwareCursorRow

	computeLineDiff := func(targetRow int) int {
		currentScreenRow := hardwareCursorRow - prevViewportTop
		targetScreenRow := targetRow - viewportTop
		return targetScreenRow - currentScreenRow
	}

	newLines := t.Container.Render(width)

	if len(t.overlayStack) > 0 {
		newLines = t.compositeOverlays(newLines, width, height)
	}

	cursorPos := t.extractCursorPosition(newLines, height)

	t.applyLineResets(newLines)

	widthChanged := t.previousWidth != 0 && t.previousWidth != width

	fullRender := func(clear bool) {
		t.fullRedrawCount++
		var buf strings.Builder
		buf.WriteString("\x1b[?2026h")
		if clear {
			buf.WriteString("\x1b[3J\x1b[2J\x1b[H")
		}
		for i, line := range newLines {
			if i > 0 {
				buf.WriteString("\r\n")
			}
			buf.WriteString(line)
		}
		buf.WriteString("\x1b[?2026l")
		t.Terminal.Write(buf.String())
		t.cursorRow = max(0, len(newLines)-1)
		t.hardwareCursorRow = t.cursorRow
		if clear {
			t.maxLinesRendered = len(newLines)
		} else {
			t.maxLinesRendered = max(t.maxLinesRendered, len(newLines))
		}
		t.previousViewportTop = max(0, t.maxLinesRendered-height)
		t.positionHardwareCursor(cursorPos, len(newLines))
		t.previousLines = newLines
		t.previousWidth = width
	}

	if len(t.previousLines) == 0 && !widthChanged {
		fullRender(false)
		return
	}

	if widthChanged {
		fullRender(true)
		return
	}

	if t.clearOnShrink && len(newLines) < t.maxLinesRendered && len(t.overlayStack) == 0 {
		fullRender(true)
		return
	}

	// Find changed lines
	firstChanged := -1
	lastChanged := -1
	maxLines := max(len(newLines), len(t.previousLines))
	for i := 0; i < maxLines; i++ {
		oldLine := ""
		if i < len(t.previousLines) {
			oldLine = t.previousLines[i]
		}
		newLine := ""
		if i < len(newLines) {
			newLine = newLines[i]
		}
		if oldLine != newLine {
			if firstChanged == -1 {
				firstChanged = i
			}
			lastChanged = i
		}
	}
	appendedLines := len(newLines) > len(t.previousLines)
	if appendedLines {
		if firstChanged == -1 {
			firstChanged = len(t.previousLines)
		}
		lastChanged = len(newLines) - 1
	}
	appendStart := appendedLines && firstChanged == len(t.previousLines) && firstChanged > 0

	if firstChanged == -1 {
		t.positionHardwareCursor(cursorPos, len(newLines))
		t.previousViewportTop = max(0, t.maxLinesRendered-height)
		return
	}

	if firstChanged >= len(newLines) {
		if len(t.previousLines) > len(newLines) {
			var buf strings.Builder
			buf.WriteString("\x1b[?2026h")
			targetRow := max(0, len(newLines)-1)
			lineDiff := computeLineDiff(targetRow)
			if lineDiff > 0 {
				fmt.Fprintf(&buf, "\x1b[%dB", lineDiff)
			} else if lineDiff < 0 {
				fmt.Fprintf(&buf, "\x1b[%dA", -lineDiff)
			}
			buf.WriteString("\r")
			extraLines := len(t.previousLines) - len(newLines)
			if extraLines > height {
				fullRender(true)
				return
			}
			if extraLines > 0 {
				buf.WriteString("\x1b[1B")
			}
			for i := 0; i < extraLines; i++ {
				buf.WriteString("\r\x1b[2K")
				if i < extraLines-1 {
					buf.WriteString("\x1b[1B")
				}
			}
			if extraLines > 0 {
				fmt.Fprintf(&buf, "\x1b[%dA", extraLines)
			}
			buf.WriteString("\x1b[?2026l")
			t.Terminal.Write(buf.String())
			t.cursorRow = targetRow
			t.hardwareCursorRow = targetRow
		}
		t.positionHardwareCursor(cursorPos, len(newLines))
		t.previousLines = newLines
		t.previousWidth = width
		t.previousViewportTop = max(0, t.maxLinesRendered-height)
		return
	}

	previousContentViewportTop := max(0, len(t.previousLines)-height)
	if firstChanged < previousContentViewportTop {
		fullRender(true)
		return
	}

	var buf strings.Builder
	buf.WriteString("\x1b[?2026h")
	prevViewportBottom := prevViewportTop + height - 1
	moveTargetRow := firstChanged
	if appendStart {
		moveTargetRow = firstChanged - 1
	}
	if moveTargetRow > prevViewportBottom {
		currentScreenRow := max(0, min(height-1, hardwareCursorRow-prevViewportTop))
		moveToBottom := height - 1 - currentScreenRow
		if moveToBottom > 0 {
			fmt.Fprintf(&buf, "\x1b[%dB", moveToBottom)
		}
		scroll := moveTargetRow - prevViewportBottom
		buf.WriteString(strings.Repeat("\r\n", scroll))
		prevViewportTop += scroll
		viewportTop += scroll
		hardwareCursorRow = moveTargetRow
	}

	lineDiff := computeLineDiff(moveTargetRow)
	if lineDiff > 0 {
		fmt.Fprintf(&buf, "\x1b[%dB", lineDiff)
	} else if lineDiff < 0 {
		fmt.Fprintf(&buf, "\x1b[%dA", -lineDiff)
	}

	if appendStart {
		buf.WriteString("\r\n")
	} else {
		buf.WriteString("\r")
	}

	renderEnd := min(lastChanged, len(newLines)-1)
	for i := firstChanged; i <= renderEnd; i++ {
		if i > firstChanged {
			buf.WriteString("\r\n")
		}
		buf.WriteString("\x1b[2K")
		buf.WriteString(newLines[i])
	}

	finalCursorRow := renderEnd

	if len(t.previousLines) > len(newLines) {
		if renderEnd < len(newLines)-1 {
			moveDown := len(newLines) - 1 - renderEnd
			fmt.Fprintf(&buf, "\x1b[%dB", moveDown)
			finalCursorRow = len(newLines) - 1
		}
		extraLines := len(t.previousLines) - len(newLines)
		for i := len(newLines); i < len(t.previousLines); i++ {
			buf.WriteString("\r\n\x1b[2K")
		}
		fmt.Fprintf(&buf, "\x1b[%dA", extraLines)
	}

	buf.WriteString("\x1b[?2026l")
	t.Terminal.Write(buf.String())

	t.cursorRow = max(0, len(newLines)-1)
	t.hardwareCursorRow = finalCursorRow
	t.maxLinesRendered = max(t.maxLinesRendered, len(newLines))
	t.previousViewportTop = max(0, t.maxLinesRendered-height)
	t.positionHardwareCursor(cursorPos, len(newLines))
	t.previousLines = newLines
	t.previousWidth = width
}

func (t *TUI) applyLineResets(lines []string) {
	for i, line := range lines {
		if !IsImageLine(line) {
			lines[i] = line + segmentReset
		}
	}
}

func (t *TUI) extractCursorPosition(lines []string, height int) *cursorPosition {
	viewportTop := max(0, len(lines)-height)
	for row := len(lines) - 1; row >= viewportTop; row-- {
		idx := strings.Index(lines[row], CursorMarker)
		if idx != -1 {
			beforeMarker := lines[row][:idx]
			col := VisibleWidth(beforeMarker)
			lines[row] = lines[row][:idx] + lines[row][idx+len(CursorMarker):]
			return &cursorPosition{row: row, col: col}
		}
	}
	return nil
}

type cursorPosition struct {
	row, col int
}

func (t *TUI) positionHardwareCursor(pos *cursorPosition, totalLines int) {
	if pos == nil || totalLines <= 0 {
		t.Terminal.HideCursor()
		return
	}
	targetRow := max(0, min(pos.row, totalLines-1))
	targetCol := max(0, pos.col)
	rowDelta := targetRow - t.hardwareCursorRow
	var buf strings.Builder
	if rowDelta > 0 {
		fmt.Fprintf(&buf, "\x1b[%dB", rowDelta)
	} else if rowDelta < 0 {
		fmt.Fprintf(&buf, "\x1b[%dA", -rowDelta)
	}
	fmt.Fprintf(&buf, "\x1b[%dG", targetCol+1)
	if buf.Len() > 0 {
		t.Terminal.Write(buf.String())
	}
	t.hardwareCursorRow = targetRow
	if t.showHardwareCursor {
		t.Terminal.ShowCursor()
	} else {
		t.Terminal.HideCursor()
	}
}

func (t *TUI) compositeOverlays(lines []string, termWidth, termHeight int) []string {
	if len(t.overlayStack) == 0 {
		return lines
	}
	result := make([]string, len(lines))
	copy(result, lines)

	type renderedOverlay struct {
		lines []string
		row   int
		col   int
		w     int
	}
	var rendered []renderedOverlay
	minLinesNeeded := len(result)

	for _, entry := range t.overlayStack {
		if !t.isOverlayVisible(entry) {
			continue
		}
		layout0 := t.resolveOverlayLayout(entry.options, 0, termWidth, termHeight)
		overlayLines := entry.component.Render(layout0.width)
		if layout0.maxHeight > 0 && len(overlayLines) > layout0.maxHeight {
			overlayLines = overlayLines[:layout0.maxHeight]
		}
		layout := t.resolveOverlayLayout(entry.options, len(overlayLines), termWidth, termHeight)
		rendered = append(rendered, renderedOverlay{lines: overlayLines, row: layout.row, col: layout.col, w: layout.width})
		if needed := layout.row + len(overlayLines); needed > minLinesNeeded {
			minLinesNeeded = needed
		}
	}

	workingHeight := max(t.maxLinesRendered, minLinesNeeded)
	for len(result) < workingHeight {
		result = append(result, "")
	}

	for _, r := range rendered {
		viewportStart := max(0, workingHeight-termHeight)
		for i, overlayLine := range r.lines {
			idx := viewportStart + r.row + i
			if idx >= 0 && idx < len(result) {
				oWidth := VisibleWidth(overlayLine)
				truncated := overlayLine
				if oWidth > r.w {
					truncated = SliceByColumn(overlayLine, 0, r.w, true)
				}
				result[idx] = t.compositeLineAt(result[idx], truncated, r.col, r.w, termWidth)
			}
		}
	}

	return result
}

func (t *TUI) compositeLineAt(baseLine, overlayLine string, startCol, overlayWidth, totalWidth int) string {
	if IsImageLine(baseLine) {
		return baseLine
	}

	afterStart := startCol + overlayWidth
	base := ExtractSegments(baseLine, startCol, afterStart, totalWidth-afterStart, true)
	overlayText, overlayW := SliceWithWidth(overlayLine, 0, overlayWidth, true)

	beforePad := max(0, startCol-base.BeforeWidth)
	overlayPad := max(0, overlayWidth-overlayW)
	actualBeforeWidth := max(startCol, base.BeforeWidth)
	actualOverlayWidth := max(overlayWidth, overlayW)
	afterTarget := max(0, totalWidth-actualBeforeWidth-actualOverlayWidth)
	afterPad := max(0, afterTarget-base.AfterWidth)

	r := segmentReset
	result := base.Before + strings.Repeat(" ", beforePad) + r +
		overlayText + strings.Repeat(" ", overlayPad) + r +
		base.After + strings.Repeat(" ", afterPad)

	resultWidth := VisibleWidth(result)
	if resultWidth <= totalWidth {
		return result
	}
	return SliceByColumn(result, 0, totalWidth, true)
}

type overlayLayout struct {
	width     int
	row       int
	col       int
	maxHeight int
}

func (t *TUI) resolveOverlayLayout(options *OverlayOptions, overlayHeight, termWidth, termHeight int) overlayLayout {
	if options == nil {
		options = &OverlayOptions{}
	}

	marginTop := max(0, options.Margin.Top)
	marginRight := max(0, options.Margin.Right)
	marginBottom := max(0, options.Margin.Bottom)
	marginLeft := max(0, options.Margin.Left)

	availWidth := max(1, termWidth-marginLeft-marginRight)
	availHeight := max(1, termHeight-marginTop-marginBottom)

	width := min(80, availWidth)
	if options.Width != nil {
		width = options.Width.resolve(termWidth)
	}
	if options.MinWidth > 0 && width < options.MinWidth {
		width = options.MinWidth
	}
	width = max(1, min(width, availWidth))

	maxHeight := 0
	if options.MaxHeight != nil {
		maxHeight = max(1, min(options.MaxHeight.resolve(termHeight), availHeight))
	}

	effectiveHeight := overlayHeight
	if maxHeight > 0 && effectiveHeight > maxHeight {
		effectiveHeight = maxHeight
	}

	anchor := options.Anchor
	if anchor == "" {
		anchor = AnchorCenter
	}

	var row, col int
	if options.Row != nil {
		if options.Row.IsPercent {
			maxRow := max(0, availHeight-effectiveHeight)
			row = marginTop + int(math.Floor(float64(maxRow)*options.Row.Percent/100.0))
		} else {
			row = options.Row.Absolute
		}
	} else {
		row = resolveAnchorRow(anchor, effectiveHeight, availHeight, marginTop)
	}

	if options.Col != nil {
		if options.Col.IsPercent {
			maxCol := max(0, availWidth-width)
			col = marginLeft + int(math.Floor(float64(maxCol)*options.Col.Percent/100.0))
		} else {
			col = options.Col.Absolute
		}
	} else {
		col = resolveAnchorCol(anchor, width, availWidth, marginLeft)
	}

	row += options.OffsetY
	col += options.OffsetX

	row = max(marginTop, min(row, termHeight-marginBottom-effectiveHeight))
	col = max(marginLeft, min(col, termWidth-marginRight-width))

	return overlayLayout{width: width, row: row, col: col, maxHeight: maxHeight}
}

func resolveAnchorRow(anchor OverlayAnchor, height, availHeight, marginTop int) int {
	switch anchor {
	case AnchorTopLeft, AnchorTopCenter, AnchorTopRight:
		return marginTop
	case AnchorBottomLeft, AnchorBottomCenter, AnchorBottomRight:
		return marginTop + availHeight - height
	default: // center, left-center, right-center
		return marginTop + (availHeight-height)/2
	}
}

func resolveAnchorCol(anchor OverlayAnchor, width, availWidth, marginLeft int) int {
	switch anchor {
	case AnchorTopLeft, AnchorLeftCenter, AnchorBottomLeft:
		return marginLeft
	case AnchorTopRight, AnchorRightCenter, AnchorBottomRight:
		return marginLeft + availWidth - width
	default: // center, top-center, bottom-center
		return marginLeft + (availWidth-width)/2
	}
}

// percentRe matches percentage strings like "50%".
var percentRe = regexp.MustCompile(`^(\d+(?:\.\d+)?)%$`)

// ParseSizeValue parses a string like "50%" or "80" into a SizeValue.
func ParseSizeValue(s string) (SizeValue, bool) {
	if m := percentRe.FindStringSubmatch(s); m != nil {
		p, err := strconv.ParseFloat(m[1], 64)
		if err == nil {
			return NewPercentSize(p), true
		}
	}
	if n, err := strconv.Atoi(s); err == nil {
		return NewAbsoluteSize(n), true
	}
	return SizeValue{}, false
}
