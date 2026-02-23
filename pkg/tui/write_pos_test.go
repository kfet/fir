package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// parseWritePositions returns the absolute content rows at which each
// \x1b[2K (erase-line) sequence occurs, given the initial cursor position.
// It also returns the final cursor row after all movements in the output.
//
// This is used to verify that the TUI's differential renderer writes each
// updated line at exactly the content row we expect.
func parseWritePositions(output string, initialCursorRow int) (eraseRows []int, finalRow int) {
	curRow := initialCursorRow

	// Split into tokens: escape sequences and plain text.
	// We use a simple state machine over the raw bytes.
	i := 0
	n := len(output)
	for i < n {
		if output[i] == '\x1b' && i+1 < n && output[i+1] == '[' {
			// Find end of CSI sequence: any ASCII letter terminates it.
			j := i + 2
			for j < n && !((output[j] >= 'A' && output[j] <= 'Z') || (output[j] >= 'a' && output[j] <= 'z')) {
				j++
			}
			if j >= n {
				break
			}
			cmd := output[j]
			params := output[i+2 : j]
			// Strip '?' prefix if present
			numStr := strings.TrimLeft(params, "?;")
			num := 1
			if numStr != "" {
				if v, err := strconv.Atoi(numStr); err == nil {
					num = v
				}
			}
			switch cmd {
			case 'A': // cursor up
				curRow -= num
			case 'B': // cursor down
				curRow += num
			case 'K': // erase in line (2K = entire line)
				if params == "2" || params == "" {
					eraseRows = append(eraseRows, curRow)
				}
			case 'H': // cursor home
				curRow = 0
			case 'J': // erase display (2J = full)
				curRow = 0
			}
			i = j + 1
			continue
		}
		if i+1 < n && output[i] == '\r' && output[i+1] == '\n' {
			curRow++
			i += 2
			continue
		}
		if output[i] == '\r' {
			i++
			continue
		}
		i++
	}
	return eraseRows, curRow
}

// ============================================================================
// Test: verify that differential renders write to the correct content rows
// ============================================================================

// scrollableComp holds lines and tracks which ones changed.
type scrollableComp struct {
	lines []string
}

func (s *scrollableComp) Render(_ int) []string { return append([]string{}, s.lines...) }
func (s *scrollableComp) Invalidate()           {}

// TestDoRender_CorrectWritePositions is the key regression test for the
// "session selector scrolling shifts the window" bug.
//
// It creates a TUI with:
//   - headerCount static lines (simulating chat messages + UI chrome above selector)
//   - selectorHeight changing lines (simulating the scrolling session list)
//
// Total > terminal height, so viewport is active.
//
// For each simulated scroll step, it verifies that the content rows written
// by the differential render are exactly those that changed (firstChanged..lastChanged).
// If the cursor is mispositioned, the write rows will be offset from expected,
// which matches the user-reported "window shifts down N lines" bug.
func TestDoRender_CorrectWritePositions(t *testing.T) {
	const headerCount = 40    // lines above the selector (chat messages)
	const selectorHeight = 23 // session selector height
	const termHeight = 24     // realistic small terminal
	const totalLines = headerCount + selectorHeight

	// First 8 selector lines are always stable.
	// Lines 8..14 ("items") and line 22 ("indicator") change each step.
	// This matches the real sessionList render pattern:
	//   header: 2 lines, items: 12 lines (8..19), path+indicator: 2 lines (20..21), padding: (22)
	// We simplify: items at selector lines [8..20], indicator at [22].
	buildSelector := func(step int) []string {
		sel := make([]string, selectorHeight)
		for i := range sel {
			switch {
			case i < 8:
				sel[i] = fmt.Sprintf("stable %d", i)
			case i < 21:
				sel[i] = fmt.Sprintf("  item %04d-%04d", step+i-8, step)
			case i == 21:
				sel[i] = fmt.Sprintf("> item %04d (selected)", step+13)
			case i == 22:
				sel[i] = fmt.Sprintf("  (%d/540)", step+1)
			}
		}
		return sel
	}

	header := &scrollableComp{lines: make([]string, headerCount)}
	for i := range header.lines {
		header.lines[i] = fmt.Sprintf("msg %03d", i)
	}

	sel0 := buildSelector(0)
	selector := &scrollableComp{lines: sel0}

	term := NewMockTerminal(80, termHeight)
	ui := NewTUI(term)
	ui.AddChild(header)
	ui.AddChild(selector)

	// Initial full render
	ui.RequestRender(true)
	ui.DoRender()

	// After full render: hardwareCursorRow is set by positionHardwareCursor.
	// For our test (no CursorMarker), hardwareCursorRow = len(lines)-1 = totalLines-1.
	expectedHWCursor := totalLines - 1

	term.ClearOutput()

	// Simulate 200 scroll steps
	for step := 1; step <= 200; step++ {
		prevSel := selector.lines
		newSel := buildSelector(step)
		selector.lines = newSel

		// Compute expected firstChanged and lastChanged in GLOBAL content rows
		firstChanged := -1
		lastChanged := -1
		for i := 0; i < selectorHeight; i++ {
			if prevSel[i] != newSel[i] {
				globalRow := headerCount + i
				if firstChanged == -1 {
					firstChanged = globalRow
				}
				lastChanged = globalRow
			}
		}

		if firstChanged == -1 {
			// No change – skip
			continue
		}

		ui.DoRender()

		outputs := term.GetOutput()
		term.ClearOutput()
		combined := strings.Join(outputs, "")

		// Must be a differential render (no clear-screen)
		if strings.Contains(combined, "\x1b[3J\x1b[2J") {
			t.Errorf("step %d: unexpected full render (clear-screen sequence emitted)", step)
			continue
		}

		// Parse write positions
		writeRows, finalRow := parseWritePositions(combined, expectedHWCursor)

		if len(writeRows) == 0 {
			t.Errorf("step %d: no erase+write sequences in differential render output", step)
			continue
		}

		// The first write must be at firstChanged and the last at lastChanged.
		if writeRows[0] != firstChanged {
			t.Errorf("step %d: first write at row %d, want %d (firstChanged=%d, lastChanged=%d, hwCursor=%d)",
				step, writeRows[0], firstChanged, firstChanged, lastChanged, expectedHWCursor)
		}
		if writeRows[len(writeRows)-1] != lastChanged {
			t.Errorf("step %d: last write at row %d, want %d",
				step, writeRows[len(writeRows)-1], lastChanged)
		}

		// All write rows must be consecutive from firstChanged to lastChanged.
		expectedRows := lastChanged - firstChanged + 1
		if len(writeRows) != expectedRows {
			t.Errorf("step %d: wrote %d rows, want %d (range [%d,%d])",
				step, len(writeRows), expectedRows, firstChanged, lastChanged)
		} else {
			for j, row := range writeRows {
				if row != firstChanged+j {
					t.Errorf("step %d: write[%d] at row %d, want %d",
						step, j, row, firstChanged+j)
				}
			}
		}

		// The final cursor row after this render becomes the hw cursor for next render.
		// The TUI ends at finalCursorRow = lastChanged, then positionHardwareCursor
		// (no CursorMarker) leaves cursor there.
		expectedHWCursor = finalRow
	}
}

// TestDoRender_ViewportBoundary verifies that differential renders never
// attempt to write above the viewport top, which would silently corrupt the
// terminal by clamping cursor movement and writing at wrong rows.
func TestDoRender_ViewportBoundary(t *testing.T) {
	const contentLines = 100
	const termHeight = 24
	const viewportTop = contentLines - termHeight // = 76

	comp := &scrollableComp{lines: make([]string, contentLines)}
	for i := range comp.lines {
		comp.lines[i] = fmt.Sprintf("line %03d", i)
	}

	term := NewMockTerminal(80, termHeight)
	ui := NewTUI(term)
	ui.AddChild(comp)

	ui.RequestRender(true)
	ui.DoRender()
	initHW := contentLines - 1 // hardwareCursorRow after full render (no CursorMarker)
	term.ClearOutput()

	// Change only lines WITHIN the visible viewport (rows 76..99)
	for step := 0; step < 50; step++ {
		row := viewportTop + (step % termHeight)
		comp.lines[row] = fmt.Sprintf("changed-%d", step)
		ui.DoRender()

		outputs := term.GetOutput()
		term.ClearOutput()
		combined := strings.Join(outputs, "")

		writeRows, finalRow := parseWritePositions(combined, initHW)

		if len(writeRows) > 0 {
			// Every write must be at or below viewportTop
			for _, wr := range writeRows {
				if wr < viewportTop {
					t.Errorf("step %d: write at row %d which is above viewportTop=%d",
						step, wr, viewportTop)
				}
			}
			initHW = finalRow
		}
	}
}

// TestDoRender_RegexParsing is a unit test for parseWritePositions.
func TestDoRender_RegexParsing(t *testing.T) {
	var ansiErase = regexp.MustCompile(`\x1b\[2K`)

	// Sanity check that our regex finds \x1b[2K
	s := "hello\x1b[2Kworld\x1b[2K"
	matches := ansiErase.FindAllString(s, -1)
	if len(matches) != 2 {
		t.Errorf("expected 2 matches, got %d", len(matches))
	}

	// Test cursor movement parsing
	output := "\x1b[?2026h\x1b[5B\r\x1b[2Kcontent1\r\n\x1b[2Kcontent2\x1b[?2026l"
	rows, final := parseWritePositions(output, 10)
	// After \x1b[5B from row 10: curRow=15; then \x1b[2K: erase row 15
	// then \r\n: curRow=16; then \x1b[2K: erase row 16
	if len(rows) != 2 {
		t.Errorf("expected 2 write rows, got %d: %v", len(rows), rows)
	} else {
		if rows[0] != 15 {
			t.Errorf("first write row: want 15, got %d", rows[0])
		}
		if rows[1] != 16 {
			t.Errorf("second write row: want 16, got %d", rows[1])
		}
	}
	_ = final
}
