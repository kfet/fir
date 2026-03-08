package components

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/session"
	"github.com/kfet/fir/pkg/tui"
)

// make540Sessions creates 540 sessions that match the user-reported failure scenario.
// Some sessions are children of others to exercise the tree prefix rendering path.
func make540Sessions() []session.SessionListInfo {
	now := time.Now()
	s := make([]session.SessionListInfo, 540)
	for i := range s {
		s[i] = session.SessionListInfo{
			Path:         fmt.Sprintf("/sessions/s%04d.jsonl", i),
			ID:           fmt.Sprintf("s%04d", i),
			Cwd:          "/home/user/project",
			Name:         fmt.Sprintf("Session %04d", i),
			Modified:     now.Add(-time.Duration(i) * time.Hour),
			MessageCount: (i % 50) + 1,
		}
		// Give every 5th session a child to exercise tree prefixes
		if i > 0 && i%5 == 0 {
			s[i].ParentSessionPath = fmt.Sprintf("/sessions/s%04d.jsonl", i-1)
		}
	}
	return s
}

// cursorVisualLine returns the 0-based line index (within the sessionList output,
// after stripping ANSI) of the selected-item cursor line (starts with "> ").
// Returns -1 if not found (skipping line 0 which is the search-input prompt).
func cursorVisualLine(lines []string) int {
	for i, line := range lines {
		if i == 0 {
			continue // skip search-input line whose prompt is also "> "
		}
		plain := stripAnsi(line)
		if strings.HasPrefix(plain, "> ") {
			return i
		}
	}
	return -1
}

// expectedCursorLine computes where the cursor SHOULD appear (0-based line in
// the sessionList render output) for a given selectedIndex and session count.
//
// Layout:
//
//	line 0 : search input
//	line 1 : empty separator
//	lines 2…: items (and optional path), then optional scroll indicator, then padding
//
// The window is centered on selectedIndex:
//
//	maxStart = max(0, n - maxVisible)
//	start    = max(0, min(selectedIndex - maxVisible/2, maxStart))
//
// The cursor line within items = (selectedIndex - start), so the render line
// is 2 + (selectedIndex - start).
func expectedCursorLine(selectedIndex, n, maxVisible int) int {
	maxStart := n - maxVisible
	if maxStart < 0 {
		maxStart = 0
	}
	start := selectedIndex - maxVisible/2
	if start > maxStart {
		start = maxStart
	}
	if start < 0 {
		start = 0
	}
	return 2 + (selectedIndex - start)
}

// TestSessionList_540_CursorPosition verifies that the cursor is always at the
// correct visual line for every scroll position in a 540-session list.
// This reproduces the user-reported bug where items 73–82 had wrong cursor placement.
func TestSessionList_540_CursorPosition(t *testing.T) {
	sessions := make540Sessions()
	const n = 540
	const maxVisible = 20

	for _, showPath := range []bool{false, true} {
		t.Run(fmt.Sprintf("showPath=%v", showPath), func(t *testing.T) {
			sl := newSessionList()
			sl.showPath = showPath
			sl.SetSessions(sessions)

			for idx := 0; idx < n; idx++ {
				sl.selectedIndex = idx
				lines := sl.Render(80)

				want := expectedCursorLine(idx, n, maxVisible)
				got := cursorVisualLine(lines)

				if got != want {
					t.Errorf("idx=%d: cursor at render line %d, want %d", idx, got, want)
					// Print context around the failure
					for i, l := range lines {
						t.Logf("  [%02d] %s", i, stripAnsi(l))
					}
					if t.Failed() && idx > 5 {
						t.FailNow() // stop at first failure, output is enough
					}
				}
			}
		})
	}
}

// TestSessionList_540_WindowBoundaries verifies that the TOP and BOTTOM items
// in the visible window correspond exactly to the expected window [start, end).
func TestSessionList_540_WindowBoundaries(t *testing.T) {
	sessions := make540Sessions()
	const n = 540
	const maxVisible = 20

	sl := newSessionList()
	sl.SetSessions(sessions)

	for idx := 0; idx < n; idx++ {
		sl.selectedIndex = idx

		// Expected window
		maxStart := n - maxVisible
		if maxStart < 0 {
			maxStart = 0
		}
		start := idx - maxVisible/2
		if start > maxStart {
			start = maxStart
		}
		if start < 0 {
			start = 0
		}
		end := start + maxVisible
		if end > n {
			end = n
		}

		lines := sl.Render(80)

		// Item at window top should appear at render line 2.
		// Item at window bottom should appear at render line 2+(end-start-1) or offset by path.
		topItem := fmt.Sprintf("Session %04d", start)
		bottomItem := fmt.Sprintf("Session %04d", end-1)

		foundTop := strings.Contains(stripAnsi(lines[2]), topItem)
		if !foundTop {
			t.Errorf("idx=%d: top of window should be %q, render line 2 = %q",
				idx, topItem, stripAnsi(lines[2]))
		}

		// Bottom item: find the last line that contains a "Session NNNN" string
		foundBottom := false
		for _, line := range lines {
			if strings.Contains(stripAnsi(line), bottomItem) {
				foundBottom = true
				break
			}
		}
		if !foundBottom {
			t.Errorf("idx=%d: bottom item %q not found in rendered output", idx, bottomItem)
		}
	}
}

// TestSessionSelector_540_NoDifferentialFullRedraws verifies that scrolling
// through 540 sessions never triggers a full TUI redraw (which would visually
// shift the entire viewport).
func TestSessionSelector_540_NoDifferentialFullRedraws(t *testing.T) {
	sessions := make540Sessions()

	term := tui.NewMockTerminal(80, 60)
	ui := tui.NewTUI(term)
	comp := NewSessionSelectorComponent(
		sessions, SessionScopeAll,
		func() ([]session.SessionListInfo, error) { return sessions, nil },
		func(path string) {}, func() {},
	)
	ui.AddChild(comp)

	ui.RequestRender(true)
	ui.DoRender()
	afterInit := ui.FullRedraws()

	for i := 0; i < 539; i++ {
		comp.HandleInput("\x1b[B")
		ui.DoRender()
	}
	for i := 0; i < 539; i++ {
		comp.HandleInput("\x1b[A")
		ui.DoRender()
	}

	if extra := ui.FullRedraws() - afterInit; extra != 0 {
		t.Errorf("expected 0 full redraws during 540-session scroll, got %d", extra)
	}
}

// TestSessionList_540_ScrollIndicator verifies the scroll indicator text is
// correct at every position: "(idx+1/540)".
func TestSessionList_540_ScrollIndicator(t *testing.T) {
	sessions := make540Sessions()
	sl := newSessionList()
	sl.SetSessions(sessions)

	for idx := 0; idx < 540; idx++ {
		sl.selectedIndex = idx
		lines := sl.Render(80)

		want := fmt.Sprintf("(%d/540)", idx+1)
		found := false
		for _, line := range lines {
			if strings.Contains(stripAnsi(line), want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("idx=%d: scroll indicator %q not found", idx, want)
		}
	}
}
