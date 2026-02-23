package tui_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/core"
	"github.com/kfet/fir/pkg/modes/interactive/components"
	"github.com/kfet/fir/pkg/tui"
)

// variableComp renders a variable number of lines, controlled by the test.
type variableComp struct {
	lines []string
}

func (v *variableComp) Render(_ int) []string { return v.lines }
func (v *variableComp) Invalidate()           {}

func make540Tree() []core.SessionListInfo {
	now := time.Now()
	s := make([]core.SessionListInfo, 540)
	for i := range s {
		s[i] = core.SessionListInfo{
			Path:         fmt.Sprintf("/sessions/s%04d.jsonl", i),
			ID:           fmt.Sprintf("s%04d", i),
			Name:         fmt.Sprintf("Session %04d", i),
			Cwd:          "/home/user/project",
			Modified:     now.Add(-time.Duration(i) * time.Hour),
			MessageCount: (i % 50) + 1,
		}
		if i > 0 && i%5 == 0 {
			s[i].ParentSessionPath = fmt.Sprintf("/sessions/s%04d.jsonl", i-1)
		}
	}
	return s
}

// diffFirstChanged returns the 0-based index of the first line that differs
// between two string slices, or -1 if they are equal.
func diffFirstChanged(before, after []string) int {
	maxLen := len(before)
	if len(after) > maxLen {
		maxLen = len(after)
	}
	for i := 0; i < maxLen; i++ {
		b, a := "", ""
		if i < len(before) {
			b = before[i]
		}
		if i < len(after) {
			a = after[i]
		}
		if b != a {
			return i
		}
	}
	return -1
}

// stripCursorMarker removes the TUI CursorMarker from a line so that
// render-output comparisons work (the marker is stripped by TUI internals
// before line diffing, but comp.Render returns it).
func stripCursorMarker(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.ReplaceAll(l, tui.CursorMarker, "")
	}
	return out
}

// TestSessionSelector_540_ExactWriteRows is the definitive regression test.
//
// It scrolls the session selector while occasionally changing the height of
// components above and below it, exactly as the real interactive mode does
// when the footer gains a status line or new chat messages arrive.
//
// For each scroll step it:
//  1. Snapshots the component output BEFORE the input
//  2. Applies the DOWN input and snapshots AFTER
//  3. Diffs to find the exact expectedFirst row (relative to the selector)
//  4. Renders and verifies that the first write lands at expectedFirst + aboveLines
//
// A 1-row cursor shift would write to expectedFirst+1 instead, which this
// test catches precisely.
func TestSessionSelector_540_ExactWriteRows(t *testing.T) {
	const termHeight = 40
	sessions := make540Tree()

	aboveVar := &variableComp{lines: make([]string, 20)}
	for i := range aboveVar.lines {
		aboveVar.lines[i] = fmt.Sprintf("msg %03d", i)
	}
	belowVar := &variableComp{lines: []string{"footer line 1", "footer line 2"}}

	comp := components.NewSessionSelectorComponent(
		sessions,
		components.SessionScopeAll,
		func() ([]core.SessionListInfo, error) { return sessions, nil },
		func(path string) {}, func() {},
	)

	term := tui.NewMockTerminal(80, termHeight)
	ui := tui.NewTUI(term)
	ui.AddChild(aboveVar)
	ui.AddChild(comp)
	ui.AddChild(belowVar)

	ui.RequestRender(true)
	ui.DoRender()
	term.ClearOutput()

	// Height variation schedule: simulates the real interactive mode where
	// footer status lines appear/disappear while the user scrolls.
	type heightChange struct {
		step      int
		newBelow  []string
		growAbove bool
	}
	schedule := []heightChange{
		{10, []string{"footer line 1", "footer line 2", "status: thinking…"}, false},
		{20, []string{"footer line 1", "footer line 2"}, false},
		{50, nil, true}, // new message arrives
		{80, []string{"footer line 1", "footer line 2", "status: done"}, false},
		{85, []string{"footer line 1", "footer line 2"}, false},
	}

	for step := 1; step <= 200; step++ {
		// Apply any scheduled height changes
		for _, hc := range schedule {
			if hc.step == step {
				if hc.newBelow != nil {
					belowVar.lines = hc.newBelow
				}
				if hc.growAbove {
					aboveVar.lines = append(aboveVar.lines, "new message")
				}
				ui.DoRender()
				term.ClearOutput()
			}
		}

		// Snapshot before
		beforeLines := stripCursorMarker(comp.Render(80))
		aboveLines := len(aboveVar.lines)

		hwBefore := ui.HardwareCursorRow()
		comp.HandleInput("\x1b[B") // DOWN

		// Snapshot after to find expected firstChanged
		afterLines := stripCursorMarker(comp.Render(80))
		firstChangedInSel := diffFirstChanged(beforeLines, afterLines)

		ui.DoRender()
		outputs := term.GetOutput()
		term.ClearOutput()
		if len(outputs) == 0 {
			continue
		}
		combined := strings.Join(outputs, "")

		if strings.Contains(combined, "\x1b[3J\x1b[2J") {
			t.Fatalf("step %d: unexpected full-render", step)
		}

		if firstChangedInSel == -1 {
			// No change detected in component output — nothing to verify
			continue
		}

		expectedFirst := aboveLines + firstChangedInSel

		writeRows, _ := parseWPs(combined, hwBefore)
		if len(writeRows) == 0 {
			continue
		}

		if writeRows[0] != expectedFirst {
			t.Errorf("step %d (idx=%d): first write at row %d, want %d "+
				"(aboveLines=%d firstChangedInSel=%d) — cursor shift",
				step, step, writeRows[0], expectedFirst, aboveLines, firstChangedInSel)
		}

		// Writes must be contiguous
		for j := 1; j < len(writeRows); j++ {
			if writeRows[j] != writeRows[j-1]+1 {
				t.Errorf("step %d: non-contiguous writes %v", step, writeRows)
				break
			}
		}
	}
}

// TestSessionSelector_540_HeightChangeBeforeScroll verifies that a height
// change (footer grows by 1) followed immediately by scrolling does not
// shift the write position.
func TestSessionSelector_540_HeightChangeBeforeScroll(t *testing.T) {
	const termHeight = 40
	sessions := make540Tree()

	above := &variableComp{lines: make([]string, 20)}
	for i := range above.lines {
		above.lines[i] = fmt.Sprintf("msg %03d", i)
	}
	below := &variableComp{lines: []string{"footer 1", "footer 2"}}

	comp := components.NewSessionSelectorComponent(
		sessions,
		components.SessionScopeAll,
		func() ([]core.SessionListInfo, error) { return sessions, nil },
		func(path string) {}, func() {},
	)

	term := tui.NewMockTerminal(80, termHeight)
	ui := tui.NewTUI(term)
	ui.AddChild(above)
	ui.AddChild(comp)
	ui.AddChild(below)

	ui.RequestRender(true)
	ui.DoRender()
	term.ClearOutput()

	// Scroll 50 steps to get into the sliding window region
	for i := 0; i < 50; i++ {
		comp.HandleInput("\x1b[B")
		ui.DoRender()
		term.ClearOutput()
	}

	// Footer grows by 1 line
	below.lines = []string{"footer 1", "footer 2", "status"}
	ui.DoRender()
	term.ClearOutput()

	// Scroll 20 steps after footer grow
	checkScrollSteps(t, ui, comp, above, term, 51, 70, "after footer grow")

	// Footer shrinks back
	below.lines = []string{"footer 1", "footer 2"}
	ui.DoRender()
	term.ClearOutput()

	// Scroll 20 more steps after footer shrink
	checkScrollSteps(t, ui, comp, above, term, 71, 90, "after footer shrink")
}

func checkScrollSteps(
	t *testing.T,
	ui *tui.TUI,
	comp *components.SessionSelectorComponent,
	above *variableComp,
	term *tui.MockTerminal,
	startStep, endStep int,
	label string,
) {
	t.Helper()
	for step := startStep; step <= endStep; step++ {
		beforeLines := stripCursorMarker(comp.Render(80))
		aboveLines := len(above.lines)
		hwBefore := ui.HardwareCursorRow()

		comp.HandleInput("\x1b[B")
		afterLines := stripCursorMarker(comp.Render(80))
		firstChangedInSel := diffFirstChanged(beforeLines, afterLines)

		ui.DoRender()
		outputs := term.GetOutput()
		term.ClearOutput()
		combined := strings.Join(outputs, "")

		if firstChangedInSel == -1 {
			continue
		}
		expectedFirst := aboveLines + firstChangedInSel

		writeRows, _ := parseWPs(combined, hwBefore)
		if len(writeRows) == 0 {
			continue
		}
		if writeRows[0] != expectedFirst {
			t.Errorf("%s step %d: first write at row %d, want %d — cursor shift",
				label, step, writeRows[0], expectedFirst)
		}
	}
}
