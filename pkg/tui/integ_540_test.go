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

// rawComp is a simple static Component for testing.
type rawComp struct{ lines []string }

func (r *rawComp) Render(_ int) []string { return r.lines }
func (r *rawComp) Invalidate()           {}

// parseWPs extracts the global content rows where \x1b[2K (erase line)
// occurred, given the initial hardware-cursor row.
// Uses the correct CSI terminator: any ASCII letter.
func parseWPs(output string, initialCursorRow int) (eraseRows []int, finalRow int) {
	curRow := initialCursorRow
	i := 0
	n := len(output)
	for i < n {
		if output[i] == '\x1b' && i+1 < n && output[i+1] == '[' {
			j := i + 2
			for j < n && !((output[j] >= 'A' && output[j] <= 'Z') || (output[j] >= 'a' && output[j] <= 'z')) {
				j++
			}
			if j >= n {
				break
			}
			cmd := output[j]
			params := output[i+2 : j]
			numStr := strings.TrimLeft(params, "?;")
			num := 1
			if numStr != "" {
				if _, err := fmt.Sscanf(numStr, "%d", &num); err != nil {
					num = 1
				}
			}
			switch cmd {
			case 'A':
				curRow -= num
			case 'B':
				curRow += num
			case 'K':
				if params == "2" || params == "" {
					eraseRows = append(eraseRows, curRow)
				}
			case 'H', 'J':
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

func make540WithTree() []core.SessionListInfo {
	now := time.Now()
	s := make([]core.SessionListInfo, 540)
	for i := range s {
		s[i] = core.SessionListInfo{
			Path:         fmt.Sprintf("/sessions/s%04d.jsonl", i),
			ID:           fmt.Sprintf("s%04d", i),
			Cwd:          "/home/user/project",
			Name:         fmt.Sprintf("Session %04d", i),
			Modified:     now.Add(-time.Duration(i) * time.Hour),
			MessageCount: (i % 50) + 1,
		}
		if i > 0 && i%10 == 0 {
			s[i].ParentSessionPath = fmt.Sprintf("/sessions/s%04d.jsonl", i-1)
		}
	}
	return s
}

// TestSessionSelector_540_WritePositions verifies that scrolling through a
// 540-session list writes content to the correct terminal rows at every step.
//
// This is the integration-level regression test for the "(73/540) window-
// shift" bug, where the visible window shifted incorrectly during scrolling.
// The test places the selector below 40 static "message" lines in a 40-row
// terminal so the TUI viewport is active (content > terminal height).
func TestSessionSelector_540_WritePositions(t *testing.T) {
	const msgLines = 40
	const termHeight = 40
	sessions := make540WithTree()

	msgs := make([]string, msgLines)
	for i := range msgs {
		msgs[i] = fmt.Sprintf("chat message %03d", i)
	}

	term := tui.NewMockTerminal(80, termHeight)
	ui := tui.NewTUI(term)
	ui.AddChild(&rawComp{msgs})

	comp := components.NewSessionSelectorComponent(
		sessions,
		components.SessionScopeAll,
		func() ([]core.SessionListInfo, error) { return sessions, nil },
		func(path string) {}, func() {},
	)
	ui.AddChild(comp)

	ui.RequestRender(true)
	ui.DoRender()

	selHeight := len(comp.Render(80))
	totalLines := msgLines + selHeight

	term.ClearOutput()

	for step := 1; step < len(sessions); step++ {
		// Snapshot the hw cursor BEFORE this render.
		hwBefore := ui.HardwareCursorRow()

		comp.HandleInput("\x1b[B") // DOWN
		ui.DoRender()

		outputs := term.GetOutput()
		term.ClearOutput()

		if len(outputs) == 0 {
			continue
		}
		combined := strings.Join(outputs, "")

		// Must be differential (no clear-screen)
		if strings.Contains(combined, "\x1b[3J\x1b[2J") {
			t.Fatalf("step %d: unexpected full-render (clear-screen) sequence", step)
		}

		writeRows, _ := parseWPs(combined, hwBefore)

		if len(writeRows) > 0 {
			// Every write must land within the selector's row range.
			for _, wr := range writeRows {
				if wr < msgLines || wr >= totalLines {
					t.Errorf("step %d (idx=%d): write at global row %d is outside "+
						"selector range [%d, %d) — window-shift bug",
						step, step, wr, msgLines, totalLines)
				}
			}

			// Writes must be contiguous (always a range firstChanged..lastChanged)
			for j := 1; j < len(writeRows); j++ {
				if writeRows[j] != writeRows[j-1]+1 {
					t.Errorf("step %d: non-contiguous write rows %v — cursor misalignment",
						step, writeRows)
					break
				}
			}
		}
	}
}
