package components_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/modes/interactive/components"
	"github.com/kfet/fir/pkg/session/store"
	"github.com/kfet/tui"
)

// TestSessionSelector_TabThenScroll is the definitive reproduction test.
//
// It exactly mirrors the user's workflow:
//  1. Open /resume in SessionScopeCurrent (showPath=false)
//  2. Press Tab → switch to SessionScopeAll (showPath=true, path line appears)
//  3. Force-full-render triggered by OnRequestRedraw
//  4. Press DOWN 73 times, checking screen state after every press
//
// For each step the test verifies:
//   - The selector top border is at the same screen row as after the full render
//   - The cursor line ("> " prefix) is at the expected screen row
//   - The path line ("    /") appears immediately BELOW the cursor line
//   - The indicator "(N/540)" is at the expected screen row
func TestSessionSelector_TabThenScroll(t *testing.T) {
	const msgCount = 30
	const termW = 80
	const termH = 50 // large enough to show 31-line selector + some messages above
	sessions := make540Sessions2()

	msgs := make([]string, msgCount)
	for i := range msgs {
		msgs[i] = fmt.Sprintf("msg %03d", i)
	}

	// We need the full render flag to be set when RequestRender(true) is called.
	forceFullOnNextRender := false

	term := tui.NewMockTerminal(termW, termH)
	sc := newScreen(termW, termH)
	ui := tui.NewTUI(term, false)
	ui.AddChild(&rawComp{msgs})

	// Start in current-folder scope (showPath=false, few sessions).
	currentSessions := sessions[:20] // only 20 sessions in "current folder"
	comp := components.NewSessionSelectorComponent(
		currentSessions,
		components.SessionScopeCurrent,
		func() ([]store.SessionListInfo, error) { return sessions, nil },
		func(string) {}, func() {},
	)
	// Wire OnRequestRedraw exactly as mode.go does.
	comp.OnRequestRedraw = func() {
		ui.RequestRender(true)
		forceFullOnNextRender = true
	}
	ui.AddChild(comp)

	// Initial full render (showPath=false, 20 sessions).
	ui.RequestRender(true)
	ui.DoRender()
	for _, w := range term.GetOutput() {
		sc.apply(w)
	}
	term.ClearOutput()

	t.Logf("After initial render (scope=current, showPath=false):")
	for r, line := range sc.dump() {
		if strings.TrimSpace(stripAnsiS(line)) != "" {
			t.Logf("  [%02d] %s", r, stripAnsiS(line))
		}
	}

	// ── Press Tab ──────────────────────────────────────────────────────────
	comp.HandleInput("\t")
	// OnRequestRedraw was called → RequestRender(true) was queued
	if !forceFullOnNextRender {
		t.Fatal("expected OnRequestRedraw to be called on Tab press")
	}

	// Full render now (scope=all, showPath=true, 540 sessions).
	ui.DoRender()
	for _, w := range term.GetOutput() {
		sc.apply(w)
	}
	term.ClearOutput()
	forceFullOnNextRender = false

	t.Logf("\nAfter Tab (scope=all, showPath=true, 540 sessions):")
	for r, line := range sc.dump() {
		if strings.TrimSpace(stripAnsiS(line)) != "" {
			t.Logf("  [%02d] %s", r, stripAnsiS(line))
		}
	}

	// Record invariants from this baseline render.
	baseTopRow := selectorTopRow(sc)
	if baseTopRow == -1 {
		t.Fatalf("cannot find selector top border after Tab")
	}
	t.Logf("\nSelector top border at screen row %d", baseTopRow)

	// Expected layout relative to baseTopRow:
	//   +0: top border (─────)
	//   +1: spacer
	//   +2: title
	//   +3: hint
	//   +4: spacer
	//   +5: search input ">"
	//   +6: empty separator
	//   +7: first item in window   (session-list line 2)
	//   ...
	//   +7+6 = +13: cursor line   (position 6 in window = middle region)
	//   +14:        path line
	//   +15..+18:   remaining items
	//   +19:        indicator
	//   +20:        padding / empty
	//   +21: spacer
	//   +22: bottom border
	// All offsets are relative to baseTopRow.
	// Selector layout: border(+0), spacer(+1), title(+2), hint(+3), spacer(+4),
	//   sessionList_line0(+5)…sessionList_line23(+28), spacer(+29), border(+30).
	// In the middle region with showPath=true and maxVisible=20:
	//   sessionList line 12 = cursor  → screen row = baseTopRow + 5 + 12 = baseTopRow+17
	//   sessionList line 13 = path    → screen row = baseTopRow + 18
	//   sessionList line 23 = indicator→ screen row = baseTopRow + 5 + 23 = baseTopRow+28
	expectedCursorRow := baseTopRow + 5 + 12    // = baseTopRow+17
	expectedPathRow := expectedCursorRow + 1    // = baseTopRow+18
	expectedIndicatorRow := baseTopRow + 5 + 23 // = baseTopRow+28

	// Verify the baseline after Tab at index 0 (top dead zone, cursor at pos 0).
	// In the top dead zone cursor is at pos 0 (not 10), adjust accordingly.
	// We'll verify layout properly once we're in the middle region (step >= 11).

	// ── Scroll DOWN 73 times ──────────────────────────────────────────────
	for step := 1; step <= 73; step++ {
		prevDump := sc.dump()
		comp.HandleInput("\x1b[B")
		ui.DoRender()
		for _, w := range term.GetOutput() {
			sc.apply(w)
		}
		term.ClearOutput()

		// Only check invariants in the middle region (index >= 10, i.e. step >= 11)
		if step < 11 {
			continue
		}

		dump := sc.dump()

		// 1. Selector top border must not move.
		topRow := selectorTopRow(sc)
		if topRow != baseTopRow {
			t.Errorf("step %d: selector top border moved from row %d to row %d",
				step, baseTopRow, topRow)
			dumpBefore(t, step, prevDump)
			dumpAfter(t, step, dump)
			if step > 10 {
				t.FailNow()
			}
			baseTopRow = topRow
			continue
		}

		// 2. Cursor line must be at expectedCursorRow.
		cursorRow := findCursorRow(dump)
		if cursorRow != expectedCursorRow {
			t.Errorf("step %d: cursor (> ) at screen row %d, want %d",
				step, cursorRow, expectedCursorRow)
			dumpBefore(t, step, prevDump)
			dumpAfter(t, step, dump)
			if step > 10 {
				t.FailNow()
			}
			continue
		}

		// 3. Path line must be immediately below cursor.
		pathRow := findPathRow(dump, cursorRow)
		if pathRow != expectedPathRow {
			t.Errorf("step %d: path line at screen row %d, want %d (cursor at %d)",
				step, pathRow, expectedPathRow, cursorRow)
			dumpBefore(t, step, prevDump)
			dumpAfter(t, step, dump)
			if step > 10 {
				t.FailNow()
			}
		}

		// 4. Indicator must be at expectedIndicatorRow.
		indRow := findIndicatorRow(dump, step+1)
		if indRow != expectedIndicatorRow {
			t.Errorf("step %d: indicator at screen row %d, want %d",
				step, indRow, expectedIndicatorRow)
		}
	}

}

// ─── helpers ─────────────────────────────────────────────────────────────────

func findCursorRow(dump []string) int {
	for r, line := range dump {
		plain := stripAnsiS(line)
		if strings.HasPrefix(plain, "> ") {
			return r
		}
	}
	return -1
}

func findPathRow(dump []string, afterRow int) int {
	for r := afterRow + 1; r < len(dump); r++ {
		plain := stripAnsiS(dump[r])
		if strings.HasPrefix(plain, "    ") && strings.Contains(plain, "/") {
			return r
		}
	}
	return -1
}

func findIndicatorRow(dump []string, idx int) int {
	needle := fmt.Sprintf("(%d/540)", idx)
	for r, line := range dump {
		if strings.Contains(stripAnsiS(line), needle) {
			return r
		}
	}
	return -1
}

func dumpBefore(t *testing.T, step int, d []string) {
	t.Helper()
	t.Logf("── screen BEFORE step %d ──", step)
	for r, line := range d {
		if strings.TrimSpace(stripAnsiS(line)) != "" {
			t.Logf("  [%02d] %s", r, stripAnsiS(line))
		}
	}
}

func dumpAfter(t *testing.T, step int, d []string) {
	t.Helper()
	t.Logf("── screen AFTER step %d ──", step)
	for r, line := range d {
		if strings.TrimSpace(stripAnsiS(line)) != "" {
			t.Logf("  [%02d] %s", r, stripAnsiS(line))
		}
	}
}
