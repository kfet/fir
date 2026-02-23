package tui_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kfet/fir/pkg/core"
	"github.com/kfet/fir/pkg/modes/interactive/components"
	"github.com/kfet/fir/pkg/tui"
)

// ─────────────────────────────────────────────────────────────────────────────
// Screen emulator
// ─────────────────────────────────────────────────────────────────────────────

// screen maintains a 2-D grid of runes and a cursor position (col, row),
// both 0-indexed.  It applies the escape sequences that the TUI emits.
type screen struct {
	cols, rows int
	cur        [2]int // [col, row]
	cells      [][]rune
}

func newScreen(cols, rows int) *screen {
	cells := make([][]rune, rows)
	for i := range cells {
		cells[i] = make([]rune, cols)
		for j := range cells[i] {
			cells[i][j] = ' '
		}
	}
	return &screen{cols: cols, rows: rows, cells: cells}
}

var ansiRe = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[A-Za-z]|\][^\x07]*\x07|[^[])`)

// apply processes a raw terminal write string, updating the grid.
func (s *screen) apply(raw string) {
	for len(raw) > 0 {
		loc := ansiRe.FindStringIndex(raw)
		if loc == nil {
			// no more escape sequences — write plain characters
			s.writeText(raw)
			break
		}
		if loc[0] > 0 {
			s.writeText(raw[:loc[0]])
		}
		seq := raw[loc[0]:loc[1]]
		s.handleSeq(seq)
		raw = raw[loc[1]:]
	}
}

func (s *screen) writeText(t string) {
	for _, r := range t {
		switch r {
		case '\r':
			s.cur[0] = 0
		case '\n':
			s.cur[1]++
			if s.cur[1] >= s.rows {
				// scroll up
				s.cells = append(s.cells[1:], make([]rune, s.cols))
				for j := range s.cells[s.rows-1] {
					s.cells[s.rows-1][j] = ' '
				}
				s.cur[1] = s.rows - 1
			}
		default:
			if s.cur[0] < s.cols && s.cur[1] < s.rows && s.cur[1] >= 0 {
				s.cells[s.cur[1]][s.cur[0]] = r
			}
			s.cur[0]++
		}
	}
}

func (s *screen) handleSeq(seq string) {
	if len(seq) < 2 {
		return
	}
	if seq[1] != '[' {
		return // OSC or other — ignore
	}
	// CSI: \x1b[<params><cmd>
	cmd := seq[len(seq)-1]
	params := seq[2 : len(seq)-1]
	num := 1
	if params != "" && params != "?" {
		p := strings.TrimLeft(params, "?;")
		if p != "" {
			fmt.Sscanf(p, "%d", &num)
		}
	}
	switch cmd {
	case 'A': // cursor up
		s.cur[1] -= num
		if s.cur[1] < 0 {
			s.cur[1] = 0
		}
	case 'B': // cursor down
		s.cur[1] += num
		if s.cur[1] >= s.rows {
			s.cur[1] = s.rows - 1
		}
	case 'G': // cursor to column (1-based)
		s.cur[0] = num - 1
		if s.cur[0] < 0 {
			s.cur[0] = 0
		}
	case 'H': // cursor home
		s.cur[0] = 0
		s.cur[1] = 0
	case 'K': // erase in line
		if num == 2 || params == "2" || params == "" {
			for j := range s.cells[s.cur[1]] {
				s.cells[s.cur[1]][j] = ' '
			}
		}
	case 'J': // erase in display — treat as clear screen
		for i := range s.cells {
			for j := range s.cells[i] {
				s.cells[i][j] = ' '
			}
		}
		s.cur = [2]int{0, 0}
	}
}

// row returns the screen row as a trimmed string (no trailing spaces).
func (s *screen) row(r int) string {
	if r < 0 || r >= s.rows {
		return ""
	}
	return strings.TrimRight(string(s.cells[r]), " ")
}

// dump returns the whole screen as a slice of strings.
func (s *screen) dump() []string {
	out := make([]string, s.rows)
	for i := range s.cells {
		out[i] = strings.TrimRight(string(s.cells[i]), " ")
	}
	return out
}

// stripAnsiS removes all ANSI escape sequences from a string.
func stripAnsiS(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func make540Sessions2() []core.SessionListInfo {
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

// runeWidth counts printable rune width (naive: all 1).
func runeWidth(s string) int {
	return utf8.RuneCountInString(s)
}

// selectorTopRow scans the screen rows for the first row that looks like
// the session-selector top border: a line of ─ (U+2500) characters.
func selectorTopRow(sc *screen) int {
	for r := 0; r < sc.rows; r++ {
		row := sc.row(r)
		plain := stripAnsiS(row)
		// The DynamicBorder renders a line of ─ (U+2500) characters.
		if strings.HasPrefix(plain, "─") && strings.Count(plain, "─") >= 10 {
			return r
		}
	}
	return -1
}

// ─────────────────────────────────────────────────────────────────────────────
// THE ACTUAL BUG REPRODUCTION TEST
// ─────────────────────────────────────────────────────────────────────────────

// TestSessionSelector_ScreenShift is the bug-reproduction test.
//
// It creates a real session selector with 540 sessions below 20 lines of static
// "messages" in a terminal of height 30, then scrolls through all 540 items
// while checking that the selector's top border is ALWAYS at the same screen row.
//
// If the border moves, the test prints the before/after screen dumps so the
// exact visual artefact is visible.
func TestSessionSelector_ScreenShift(t *testing.T) {
	const msgCount = 20
	const termW = 80
	const termH = 55 // large enough to show all 20 msgs + 31-line selector
	sessions := make540Sessions2()

	// Static messages above the selector.
	msgs := make([]string, msgCount)
	for i := range msgs {
		msgs[i] = fmt.Sprintf("chat message %03d", i)
	}

	term := tui.NewMockTerminal(termW, termH)
	sc := newScreen(termW, termH)

	ui := tui.NewTUI(term)
	ui.AddChild(&rawComp{msgs})

	comp := components.NewSessionSelectorComponent(
		sessions,
		components.SessionScopeAll,
		func() ([]core.SessionListInfo, error) { return sessions, nil },
		func(string) {}, func() {},
	)
	ui.AddChild(comp)

	// Initial full render.
	ui.RequestRender(true)
	ui.DoRender()
	for _, w := range term.GetOutput() {
		sc.apply(w)
	}
	term.ClearOutput()

	// Record where the selector top border is.
	baseTopRow := selectorTopRow(sc)
	if baseTopRow == -1 {
		t.Fatalf("could not find session-selector top border after initial render\nScreen:\n%s",
			strings.Join(sc.dump(), "\n"))
	}
	t.Logf("initial selector top border at screen row %d", baseTopRow)

	// Scroll through all 540 sessions, checking stability every step.
	for step := 1; step < len(sessions); step++ {
		prevDump := sc.dump()

		comp.HandleInput("\x1b[B") // DOWN arrow
		ui.DoRender()

		for _, w := range term.GetOutput() {
			sc.apply(w)
		}
		term.ClearOutput()

		topRow := selectorTopRow(sc)
		if topRow != baseTopRow {
			t.Errorf(
				"step %d (idx %d): selector top border shifted from row %d to row %d",
				step, step, baseTopRow, topRow,
			)
			t.Logf("─── screen BEFORE step %d ───", step)
			for r, line := range prevDump {
				t.Logf("[%02d] %s", r, stripAnsiS(line))
			}
			t.Logf("─── screen AFTER step %d ───", step)
			for r, line := range sc.dump() {
				t.Logf("[%02d] %s", r, stripAnsiS(line))
			}
			if step > 5 {
				t.FailNow()
			}
			// Recalibrate so subsequent steps are relative to new position.
			baseTopRow = topRow
		}
	}
}

// TestSessionSelector_ScreenShift_SmallTerminal runs the same test with a
// smaller terminal (24 rows) to check that the bug also manifests there.
func TestSessionSelector_ScreenShift_SmallTerminal(t *testing.T) {
	const msgCount = 10
	const termW = 80
	const termH = 45 // smaller than ScreenShift but still shows 10 msgs + 31-line selector
	sessions := make540Sessions2()

	msgs := make([]string, msgCount)
	for i := range msgs {
		msgs[i] = fmt.Sprintf("msg %03d", i)
	}

	term := tui.NewMockTerminal(termW, termH)
	sc := newScreen(termW, termH)

	ui := tui.NewTUI(term)
	ui.AddChild(&rawComp{msgs})

	comp := components.NewSessionSelectorComponent(
		sessions,
		components.SessionScopeAll,
		func() ([]core.SessionListInfo, error) { return sessions, nil },
		func(string) {}, func() {},
	)
	ui.AddChild(comp)

	ui.RequestRender(true)
	ui.DoRender()
	for _, w := range term.GetOutput() {
		sc.apply(w)
	}
	term.ClearOutput()

	baseTopRow := selectorTopRow(sc)
	if baseTopRow == -1 {
		t.Fatalf("could not find selector top border\nScreen:\n%s",
			strings.Join(sc.dump(), "\n"))
	}
	t.Logf("initial selector top border at screen row %d", baseTopRow)

	for step := 1; step < len(sessions); step++ {
		prevDump := sc.dump()
		comp.HandleInput("\x1b[B")
		ui.DoRender()
		for _, w := range term.GetOutput() {
			sc.apply(w)
		}
		term.ClearOutput()

		topRow := selectorTopRow(sc)
		if topRow != baseTopRow {
			t.Errorf("step %d: selector top border shifted %d→%d",
				step, baseTopRow, topRow)
			t.Logf("─── BEFORE ───")
			for r, line := range prevDump {
				t.Logf("[%02d] %s", r, stripAnsiS(line))
			}
			t.Logf("─── AFTER ───")
			for r, line := range sc.dump() {
				t.Logf("[%02d] %s", r, stripAnsiS(line))
			}
			if step > 5 {
				t.FailNow()
			}
			baseTopRow = topRow
		}
	}
}

// TestSessionSelector_NewlineInName_ScreenStability verifies that sessions
// with embedded newlines in their names do NOT cause the selector to drift
// down the screen — that was the root cause of the reported "items 73-83
// shift down one line" bug.
func TestSessionSelector_NewlineInName_ScreenStability(t *testing.T) {
	const msgCount = 20
	const termW = 80
	const termH = 55 // large enough to show all 20 msgs + 31-line selector
	now := time.Now()

	sessions := make([]core.SessionListInfo, 200)
	for i := range sessions {
		sessions[i] = core.SessionListInfo{
			Path:     fmt.Sprintf("/s/%04d.jsonl", i),
			ID:       fmt.Sprintf("s%04d", i),
			Cwd:      "/project",
			Name:     fmt.Sprintf("Session %04d", i),
			Modified: now.Add(-time.Duration(i) * time.Hour),
		}
	}
	// Inject newlines into sessions 70-80 — the exact range the user reported.
	for i := 70; i <= 80; i++ {
		sessions[i].Name = fmt.Sprintf("Session %04d\nwith newline", i)
	}

	msgs := make([]string, msgCount)
	for i := range msgs {
		msgs[i] = fmt.Sprintf("msg %03d", i)
	}

	term := tui.NewMockTerminal(termW, termH)
	sc := newScreen(termW, termH)
	ui := tui.NewTUI(term)
	ui.AddChild(&rawComp{msgs})
	comp := components.NewSessionSelectorComponent(
		sessions, components.SessionScopeAll,
		func() ([]core.SessionListInfo, error) { return sessions, nil },
		func(string) {}, func() {},
	)
	ui.AddChild(comp)

	ui.RequestRender(true)
	ui.DoRender()
	for _, w := range term.GetOutput() {
		sc.apply(w)
	}
	term.ClearOutput()

	baseTopRow := selectorTopRow(sc)
	if baseTopRow == -1 {
		t.Fatalf("cannot find selector border after initial render")
	}
	t.Logf("initial selector top border at screen row %d", baseTopRow)

	for step := 1; step < len(sessions); step++ {
		comp.HandleInput("\x1b[B")
		ui.DoRender()
		for _, w := range term.GetOutput() {
			sc.apply(w)
		}
		term.ClearOutput()

		topRow := selectorTopRow(sc)
		if topRow != baseTopRow {
			t.Errorf("step %d (idx=%d): selector drifted from row %d to row %d — newline-in-name bug",
				step, step, baseTopRow, topRow)
			baseTopRow = topRow
		}
	}
}

func TestScreen_Apply(t *testing.T) {
	sc := newScreen(20, 5)
	// Write two lines
	sc.apply("hello\r\nworld")
	if got := sc.row(0); got != "hello" {
		t.Errorf("row 0: want 'hello', got %q", got)
	}
	if got := sc.row(1); got != "world" {
		t.Errorf("row 1: want 'world', got %q", got)
	}
	// Cursor up + erase + rewrite
	sc.apply("\x1b[1A\r\x1b[2Kbye")
	if got := sc.row(0); got != "bye" {
		t.Errorf("after up+erase+write, row 0: want 'bye', got %q", got)
	}
	// Cursor down
	sc.apply("\x1b[1B\r\x1b[2Kzap")
	if got := sc.row(1); got != "zap" {
		t.Errorf("after down+erase+write, row 1: want 'zap', got %q", got)
	}
	_ = runeWidth
}

// TestSessionSelector_UnicodeSeparators_ScreenStability verifies that session
// names containing Unicode line/paragraph separators (U+2028, U+2029) — which
// some terminals treat as newlines — do not cause the selector to drift.
// These are NOT caught by the original < 0x20 filter and were the remaining
// source of the "later session (83)" shift after the ASCII-newline fix.
func TestSessionSelector_UnicodeSeparators_ScreenStability(t *testing.T) {
	const termW = 80
	const termH = 55 // large enough to show 20 msgs + 31-line selector
	now := time.Now()

	sessions := make([]core.SessionListInfo, 200)
	for i := range sessions {
		sessions[i] = core.SessionListInfo{
			Path:     fmt.Sprintf("/s/%04d.jsonl", i),
			ID:       fmt.Sprintf("s%04d", i),
			Cwd:      "/project",
			Name:     fmt.Sprintf("Session %04d", i),
			Modified: now.Add(-time.Duration(i) * time.Hour),
		}
	}
	// Inject each problematic character into a range of sessions.
	// U+2028 (Line Separator): sessions 75-80.
	for i := 75; i <= 80; i++ {
		sessions[i].Name = fmt.Sprintf("Session %04d\u2028suffix", i)
	}
	// U+2029 (Paragraph Separator): sessions 85-90.
	for i := 85; i <= 90; i++ {
		sessions[i].Name = fmt.Sprintf("Session %04d\u2029suffix", i)
	}
	// DEL (0x7F): sessions 90-95.
	for i := 90; i <= 95; i++ {
		sessions[i].Name = fmt.Sprintf("Session %04d\x7fsuffix", i)
	}
	// C1 CSI (0x9B = U+009B): sessions 95-100.
	for i := 95; i <= 100; i++ {
		sessions[i].Name = fmt.Sprintf("Session %04d\u009bsuffix", i)
	}

	msgs := make([]string, 20)
	for i := range msgs {
		msgs[i] = fmt.Sprintf("msg %03d", i)
	}

	term := tui.NewMockTerminal(termW, termH)
	sc := newScreen(termW, termH)
	ui := tui.NewTUI(term)
	ui.AddChild(&rawComp{msgs})
	comp := components.NewSessionSelectorComponent(
		sessions, components.SessionScopeAll,
		func() ([]core.SessionListInfo, error) { return sessions, nil },
		func(string) {}, func() {},
	)
	ui.AddChild(comp)

	ui.RequestRender(true)
	ui.DoRender()
	for _, w := range term.GetOutput() {
		sc.apply(w)
	}
	term.ClearOutput()

	baseTopRow := selectorTopRow(sc)
	if baseTopRow == -1 {
		t.Fatalf("cannot find selector border after initial render")
	}

	for step := 1; step < len(sessions); step++ {
		comp.HandleInput("\x1b[B")
		ui.DoRender()
		for _, w := range term.GetOutput() {
			sc.apply(w)
		}
		term.ClearOutput()

		topRow := selectorTopRow(sc)
		if topRow != baseTopRow {
			t.Errorf("step %d (idx=%d): selector drifted %d→%d — unicode-separator sanitization bug",
				step, step, baseTopRow, topRow)
			baseTopRow = topRow
		}
	}
}

// TestSessionSelector_CwdWithSeparators_ScreenStability verifies that the
// path line (Cwd) is also sanitized. A Cwd containing U+2028 or a newline
// would cause the path line to wrap, growing the selector beyond 23 lines.
func TestSessionSelector_CwdWithSeparators_ScreenStability(t *testing.T) {
	const termW = 80
	const termH = 55 // large enough to show 20 msgs + 31-line selector
	now := time.Now()

	sessions := make([]core.SessionListInfo, 200)
	for i := range sessions {
		sessions[i] = core.SessionListInfo{
			Path:     fmt.Sprintf("/s/%04d.jsonl", i),
			ID:       fmt.Sprintf("s%04d", i),
			Cwd:      "/project",
			Name:     fmt.Sprintf("Session %04d", i),
			Modified: now.Add(-time.Duration(i) * time.Hour),
		}
	}
	// Inject bad Cwd values around the reported range.
	for i := 75; i <= 85; i++ {
		sessions[i].Cwd = fmt.Sprintf("/home/user/proj\nect-%04d", i)
	}
	for i := 86; i <= 95; i++ {
		sessions[i].Cwd = fmt.Sprintf("/home/user/proj\u2028ect-%04d", i)
	}

	msgs := make([]string, 20)
	for i := range msgs {
		msgs[i] = fmt.Sprintf("msg %03d", i)
	}

	term := tui.NewMockTerminal(termW, termH)
	sc := newScreen(termW, termH)
	ui := tui.NewTUI(term)
	ui.AddChild(&rawComp{msgs})
	comp := components.NewSessionSelectorComponent(
		sessions, components.SessionScopeAll,
		func() ([]core.SessionListInfo, error) { return sessions, nil },
		func(string) {}, func() {},
	)
	ui.AddChild(comp)

	ui.RequestRender(true)
	ui.DoRender()
	for _, w := range term.GetOutput() {
		sc.apply(w)
	}
	term.ClearOutput()

	baseTopRow := selectorTopRow(sc)
	if baseTopRow == -1 {
		t.Fatalf("cannot find selector border after initial render")
	}

	for step := 1; step < len(sessions); step++ {
		comp.HandleInput("\x1b[B")
		ui.DoRender()
		for _, w := range term.GetOutput() {
			sc.apply(w)
		}
		term.ClearOutput()

		topRow := selectorTopRow(sc)
		if topRow != baseTopRow {
			t.Errorf("step %d (idx=%d): selector drifted %d→%d — cwd sanitization bug",
				step, step, baseTopRow, topRow)
			baseTopRow = topRow
		}
	}
}
