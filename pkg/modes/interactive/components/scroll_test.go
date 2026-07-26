package components

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/session/store"
	"github.com/kfet/tui"
)

// ansiRe matches all ANSI escape sequences for plain-text comparison.
var ansiRe = regexp.MustCompile(`\x1b(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~])`)

// stripAnsi removes all ANSI escape sequences for plain-text comparison.
func stripAnsi(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// makeNSessions returns n SessionListInfo values for testing.
func makeNSessions(n int) []store.SessionListInfo {
	now := time.Now()
	s := make([]store.SessionListInfo, n)
	for i := range s {
		s[i] = store.SessionListInfo{
			Path:         fmt.Sprintf("/sessions/s%03d.jsonl", i),
			ID:           fmt.Sprintf("s%03d", i),
			Cwd:          "/home/user/project",
			Name:         fmt.Sprintf("Session %03d", i),
			Modified:     now.Add(-time.Duration(i) * time.Hour),
			MessageCount: i + 1,
		}
	}
	return s
}

// TestSessionListHeight_StabilityAcrossCounts verifies that sessionList.Render()
// always returns exactly targetHeight (16) lines regardless of session count,
// selected index, or showPath setting. This is the key invariant that prevents
// the TUI differential renderer from scrolling the terminal when the session
// list is scrolled — the bug the user observed with ~32 sessions.
func TestSessionListHeight_StabilityAcrossCounts(t *testing.T) {
	const targetHeight = 24
	for n := 1; n <= 40; n++ {
		sessions := makeNSessions(n)
		for _, showPath := range []bool{false, true} {
			sl := newSessionList()
			sl.showPath = showPath
			sl.SetSessions(sessions)
			for idx := 0; idx < n; idx++ {
				sl.selectedIndex = idx
				got := len(sl.Render(80))
				if got != targetHeight {
					t.Errorf("n=%d showPath=%v idx=%d: Render() returned %d lines, want %d",
						n, showPath, idx, got, targetHeight)
				}
			}
		}
	}
}

// TestSessionListHeight_StabilityAfterSearch verifies that filtering (search)
// does not change the rendered height.
func TestSessionListHeight_StabilityAfterSearch(t *testing.T) {
	const targetHeight = 24
	sessions := makeNSessions(32)
	sl := newSessionList()
	sl.SetSessions(sessions)

	baseH := len(sl.Render(80))
	if baseH != targetHeight {
		t.Fatalf("initial height %d, want %d", baseH, targetHeight)
	}

	// Narrow the filter to just 5 matches, widen again
	for _, filter := range []string{"Session 00", "Session 0", "Session", "xyz", ""} {
		sl.applyFilter(filter)
		if h := len(sl.Render(80)); h != targetHeight {
			t.Errorf("filter=%q: height=%d, want %d", filter, h, targetHeight)
		}
	}
}

// TestSessionListWindow_SelectedAlwaysVisible verifies that the selected
// item is always present in the rendered output for all n and selectedIndex
// values up to n=50. This catches any off-by-one in the visible window
// calculation, particularly at the centering boundary and at list end.
func TestSessionListWindow_SelectedAlwaysVisible(t *testing.T) {
	for n := 1; n <= 50; n++ {
		sessions := makeNSessions(n)
		sl := newSessionList()
		sl.SetSessions(sessions)
		for idx := 0; idx < n; idx++ {
			sl.selectedIndex = idx
			lines := sl.Render(80)

			// The first line is the search input ("> ...") — skip it.
			// The selected item must appear with cursor "> " prefix after it.
			target := fmt.Sprintf("Session %03d", idx)
			found := false
			for _, line := range lines[1:] {
				plain := stripAnsi(line)
				if strings.HasPrefix(plain, "> ") && strings.Contains(plain, target) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("n=%d idx=%d: selected item %q not visible with cursor", n, idx, target)
				for i, l := range lines {
					t.Logf("  [%02d] %s", i, stripAnsi(l))
				}
			}
		}
	}
}

// TestSessionListWindow_FullWindowSize verifies that the visible window
// always shows exactly min(n, maxVisible) items (no partial windows).
func TestSessionListWindow_FullWindowSize(t *testing.T) {
	const maxVisible = 20
	for n := 1; n <= 50; n++ {
		sessions := makeNSessions(n)
		sl := newSessionList()
		sl.SetSessions(sessions)

		expectedItems := n
		if expectedItems > maxVisible {
			expectedItems = maxVisible
		}

		for idx := 0; idx < n; idx++ {
			sl.selectedIndex = idx
			lines := sl.Render(80)

			// Count item lines: non-empty lines after the header (line 0=search, line 1=empty)
			// that start with "  " or "> " and contain session content.
			itemCount := 0
			for _, line := range lines[2:] {
				plain := stripAnsi(line)
				if strings.HasPrefix(plain, "> Session") || strings.HasPrefix(plain, "  Session") {
					itemCount++
				}
			}
			if itemCount != expectedItems {
				t.Errorf("n=%d idx=%d: visible items=%d, want=%d",
					n, idx, itemCount, expectedItems)
			}
		}
	}
}

// TestSessionList_Scroll32_Content verifies exact scroll behavior with 32
// sessions: selected item visible at every scroll position, scroll indicator
// correct, height stable. Exercises the full sliding→locked-bottom transition.
func TestSessionList_Scroll32_Content(t *testing.T) {
	const n = 32
	sessions := makeNSessions(n)

	for _, showPath := range []bool{false, true} {
		t.Run(fmt.Sprintf("showPath=%v", showPath), func(t *testing.T) {
			sl := newSessionList()
			sl.showPath = showPath
			sl.SetSessions(sessions)

			baseH := len(sl.Render(80))

			for idx := 0; idx < n; idx++ {
				sl.selectedIndex = idx
				lines := sl.Render(80)

				// Height stable
				if len(lines) != baseH {
					t.Errorf("idx=%d: height %d → %d", idx, baseH, len(lines))
				}

				// Selected item visible with cursor
				target := fmt.Sprintf("Session %03d", idx)
				cursorFound := false
				for _, line := range lines[1:] {
					plain := stripAnsi(line)
					if strings.HasPrefix(plain, "> ") && strings.Contains(plain, target) {
						cursorFound = true
						break
					}
				}
				if !cursorFound {
					t.Errorf("idx=%d: selected %q not visible", idx, target)
				}

				// Exactly one cursor line (the selected item, excluding search input)
				cursorCount := 0
				for _, line := range lines[1:] {
					plain := stripAnsi(line)
					if strings.HasPrefix(plain, "> Session") {
						cursorCount++
					}
				}
				if cursorCount != 1 {
					t.Errorf("idx=%d: expected 1 cursor line, got %d", idx, cursorCount)
				}

				// Scroll indicator: visible iff window doesn't show all sessions
				const maxVisible = 20
				maxStart := max(0, n-maxVisible)
				start := max(0, min(idx-maxVisible/2, maxStart))
				end := min(start+maxVisible, n)
				wantIndicator := start > 0 || end < n

				indicatorFound := false
				for _, line := range lines {
					plain := stripAnsi(line)
					if strings.HasPrefix(plain, "  (") && strings.Contains(plain, "/") {
						indicatorFound = true
						break
					}
				}
				if indicatorFound != wantIndicator {
					t.Errorf("idx=%d: scroll indicator wantVisible=%v got=%v (window=[%d,%d))",
						idx, wantIndicator, indicatorFound, start, end)
				}
			}
		})
	}
}

// TestSessionSelector_NoDifferentialFullRedraws verifies that scrolling
// through a 32-session list doesn't trigger unexpected full redraws in the
// TUI differential renderer (which would cause visual flicker).
func TestSessionSelector_NoDifferentialFullRedraws(t *testing.T) {
	sessions := makeNSessions(32)

	term := tui.NewMockTerminal(80, 60)
	ui := tui.NewTUI(term, false)
	comp := NewSessionSelectorComponent(
		sessions, SessionScopeCurrent, nil,
		func(path string) {}, func() {},
	)
	ui.AddChild(comp)

	// Force initial full render.
	ui.RequestRender(true)
	ui.DoRender()
	afterInit := ui.FullRedraws()

	// Scroll all the way down then back up.
	for i := 0; i < 31; i++ {
		comp.HandleInput("\x1b[B") // DOWN
		ui.DoRender()
	}
	for i := 0; i < 31; i++ {
		comp.HandleInput("\x1b[A") // UP
		ui.DoRender()
	}

	extra := ui.FullRedraws() - afterInit
	if extra != 0 {
		t.Errorf("expected 0 full redraws during scroll, got %d", extra)
	}
}

// TestSessionSelector_ScopeToggleHeightStable verifies that switching scope
// (Tab key) — which changes showPath and session count — never changes the
// rendered height, even after mid-list scrolling.
func TestSessionSelector_ScopeToggleHeightStable(t *testing.T) {
	currentSessions := makeNSessions(5)
	allSessions := makeNSessions(32)

	comp := NewSessionSelectorComponent(
		currentSessions, SessionScopeCurrent,
		func() ([]store.SessionListInfo, error) { return allSessions, nil },
		func(path string) {}, func() {},
	)

	baseH := len(comp.Render(80))

	// Scroll, toggle, scroll, toggle back — height must never change.
	steps := []struct {
		input string
		label string
	}{
		{"\x1b[B", "down"},
		{"\x1b[B", "down"},
		{"\x1b[B", "down"},
		{"\t", "tab→all"},
		{"\x1b[B", "down(all)"},
		{"\x1b[B", "down(all)"},
		{"\x1b[B", "down(all)"},
		{"\x1b[B", "down(all)"},
		{"\x1b[B", "down(all)"},
		{"\x1b[B", "down(all)"},
		{"\t", "tab→current"},
		{"\x1b[A", "up"},
	}
	for _, step := range steps {
		comp.HandleInput(step.input)
		if h := len(comp.Render(80)); h != baseH {
			t.Errorf("after %s: height %d → %d", step.label, baseH, h)
		}
	}
}
