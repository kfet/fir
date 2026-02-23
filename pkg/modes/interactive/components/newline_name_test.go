package components

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/core"
)

// TestSessionList_NewlineInName verifies that a session whose Name or
// FirstMessage contains a newline does NOT produce a multi-line entry in
// sessionList.Render(), which would corrupt hardwareCursorRow tracking.
//
// This is the repro test for the reported bug: "around items 73-83, the
// visible window shifts down one line." If sessions with embedded newlines
// scroll into view, the TUI writes too many terminal rows, offset-ing all
// subsequent cursor movements by +1 per corrupted line.
func TestSessionList_NewlineInName(t *testing.T) {
	now := time.Now()

	sessions := make([]core.SessionListInfo, 20)
	for i := range sessions {
		sessions[i] = core.SessionListInfo{
			Path:     fmt.Sprintf("/s/%04d.jsonl", i),
			ID:       fmt.Sprintf("s%04d", i),
			Cwd:      "/home/user/project",
			Name:     fmt.Sprintf("Session %04d", i),
			Modified: now.Add(-time.Duration(i) * time.Hour),
		}
	}
	// Inject a newline into sessions 5 and 8 — simulates a real session
	// whose Name was read from a multi-line first message.
	sessions[5].Name = "Session with\nnewline in name"
	sessions[8].Name = "Another\r\nembedded CRLF"

	sl := newSessionList()
	sl.showPath = true
	sl.SetSessions(sessions)

	for idx := 0; idx < len(sessions); idx++ {
		sl.selectedIndex = idx
		lines := sl.Render(80)
		if len(lines) != 24 {
			t.Errorf("idx=%d: Render() returned %d lines (want 24)", idx, len(lines))
			for i, l := range lines {
				t.Logf("  [%02d] %q", i, stripAnsi(l))
			}
			continue
		}
		for i, line := range lines {
			clean := stripAnsi(line)
			if strings.ContainsAny(clean, "\n\r") {
				t.Errorf("idx=%d: line[%d] contains raw newline/CR: %q", idx, i, clean)
			}
		}
	}
}
