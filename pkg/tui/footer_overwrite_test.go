package tui

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"testing"
)

// vtScreen is a minimal virtual-terminal emulator: it interprets the subset of
// ANSI control sequences the TUI's differential renderer emits and maintains a
// character grid. Crucially it models the real-terminal behaviour the renderer
// relies on: a linefeed (\n) on the bottom row scrolls the whole screen up by
// one, whereas cursor-down (CSI B) on the bottom row does NOT scroll.
//
// This lets a test see exactly what a user would see on screen, so we can
// assert that the footer (the last rendered lines) is never overwritten or
// scrolled away by the diff renderer.
type vtScreen struct {
	rows, cols int
	grid       [][]rune
	cr, cc     int // cursor row, col (0-indexed, within the screen)
}

func newVTScreen(cols, rows int) *vtScreen {
	s := &vtScreen{rows: rows, cols: cols}
	s.clear()
	return s
}

func (s *vtScreen) clear() {
	s.grid = make([][]rune, s.rows)
	for r := range s.grid {
		s.grid[r] = make([]rune, s.cols)
		for c := range s.grid[r] {
			s.grid[r][c] = ' '
		}
	}
	s.cr, s.cc = 0, 0
}

func (s *vtScreen) scrollUp() {
	copy(s.grid, s.grid[1:])
	last := make([]rune, s.cols)
	for c := range last {
		last[c] = ' '
	}
	s.grid[s.rows-1] = last
}

func (s *vtScreen) lineFeed() {
	if s.cr >= s.rows-1 {
		s.scrollUp()
	} else {
		s.cr++
	}
}

func (s *vtScreen) putRune(r rune) {
	if s.cr < 0 || s.cr >= s.rows {
		return
	}
	if s.cc < 0 {
		s.cc = 0
	}
	if s.cc >= s.cols {
		// Clamp: ignore overflow writes (renderer should not overflow).
		return
	}
	s.grid[s.cr][s.cc] = r
	s.cc++
}

// write feeds a chunk of terminal output into the emulator.
func (s *vtScreen) write(data string) {
	rs := []rune(data)
	i := 0
	n := len(rs)
	for i < n {
		ch := rs[i]
		if ch == '\x1b' {
			// OSC (\x1b]) — skip to BEL or ST. The renderer mostly uses CSI.
			if i+1 < n && rs[i+1] == ']' {
				j := i + 2
				for j < n && rs[j] != '\x07' {
					if rs[j] == '\x1b' && j+1 < n && rs[j+1] == '\\' {
						j++
						break
					}
					j++
				}
				i = j + 1
				continue
			}
			if i+1 < n && rs[i+1] == '[' {
				j := i + 2
				for j < n {
					c := rs[j]
					if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
						break
					}
					j++
				}
				if j >= n {
					break
				}
				cmd := rs[j]
				params := string(rs[i+2 : j])
				s.applyCSI(cmd, params)
				i = j + 1
				continue
			}
			// Lone ESC — skip it.
			i++
			continue
		}
		switch ch {
		case '\r':
			s.cc = 0
		case '\n':
			s.lineFeed()
		default:
			s.putRune(ch)
		}
		i++
	}
}

func (s *vtScreen) applyCSI(cmd rune, params string) {
	numParam := func(def int) int {
		p := strings.TrimLeft(params, "?")
		if p == "" {
			return def
		}
		// Take first ;-separated value.
		if idx := strings.IndexByte(p, ';'); idx >= 0 {
			p = p[:idx]
		}
		v, err := strconv.Atoi(p)
		if err != nil {
			return def
		}
		return v
	}
	switch cmd {
	case 'A': // up
		s.cr -= numParam(1)
		if s.cr < 0 {
			s.cr = 0
		}
	case 'B': // down (no scroll)
		s.cr += numParam(1)
		if s.cr >= s.rows {
			s.cr = s.rows - 1
		}
	case 'C': // forward
		s.cc += numParam(1)
	case 'D': // back
		s.cc -= numParam(1)
	case 'G': // column (1-indexed)
		s.cc = numParam(1) - 1
		if s.cc < 0 {
			s.cc = 0
		}
	case 'H', 'f': // cursor home/position
		s.cr, s.cc = 0, 0
	case 'J': // erase display (2J/3J → clear)
		s.clear()
	case 'K': // erase line
		mode := numParam(0)
		if s.cr >= 0 && s.cr < s.rows {
			switch mode {
			case 0: // cursor to end
				for c := s.cc; c < s.cols; c++ {
					s.grid[s.cr][c] = ' '
				}
			case 1: // start to cursor
				for c := 0; c <= s.cc && c < s.cols; c++ {
					s.grid[s.cr][c] = ' '
				}
			case 2: // whole line
				for c := 0; c < s.cols; c++ {
					s.grid[s.cr][c] = ' '
				}
			}
		}
	case 'm': // SGR — styling only, ignore
	default:
		// ignore other sequences (incl. ?2026h/l sync)
	}
}

// rowString returns a screen row trimmed of trailing spaces.
func (s *vtScreen) rowString(r int) string {
	if r < 0 || r >= s.rows {
		return ""
	}
	return strings.TrimRight(string(s.grid[r]), " ")
}

// stripANSI removes CSI/SGR sequences so we can compare visible text.
func stripANSIForTest(s string) string {
	var b strings.Builder
	rs := []rune(s)
	i := 0
	for i < len(rs) {
		if rs[i] == '\x1b' && i+1 < len(rs) && rs[i+1] == '[' {
			j := i + 2
			for j < len(rs) {
				c := rs[j]
				if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
					break
				}
				j++
			}
			i = j + 1
			continue
		}
		b.WriteRune(rs[i])
		i++
	}
	return strings.TrimRight(b.String(), " ")
}

// TestDoRender_HeightResizeForcesFullRedraw reproduces the "TUI overwrites the
// status bar" bug: the differential renderer forces a full redraw on width
// changes but NOT on height changes. In tmux, vertical pane resizes change the
// row count constantly; without a full redraw the diff path runs against stale
// viewport geometry and the footer (last lines) ends up overwritten or
// scrolled off.
func TestDoRender_HeightResizeForcesFullRedraw(t *testing.T) {
	const cols = 80

	term := NewMockTerminal(cols, 24)
	ui := NewTUI(term, false)

	body := &scrollableComp{}
	editor := &scrollableComp{}
	footer := &scrollableComp{}
	ui.AddChild(body)
	ui.AddChild(editor)
	ui.AddChild(footer)

	body.lines = make([]string, 30)
	for i := range body.lines {
		body.lines[i] = fmt.Sprintf("body %03d", i)
	}
	editor.lines = []string{"╭───╮", "│ > " + CursorMarker + " │"}
	footer.lines = []string{"~/proj (main)", "up 1.2k  42.0%/200k  model-x"}

	ui.RequestRender(true)
	ui.DoRender()
	term.ClearOutput()

	// Shrink the pane height (tmux vertical resize).
	const newRows = 12
	term.mu.Lock()
	term.rows = newRows
	term.mu.Unlock()

	// Resize triggers a non-forced render in production.
	ui.RequestRender(false)
	ui.DoRender()

	combined := strings.Join(term.GetOutput(), "")

	// After a height change the renderer must repaint the whole screen.
	if !strings.Contains(combined, "\x1b[2J") {
		t.Errorf("height resize did not trigger a full clear+redraw; footer can be left overwritten")
	}

	scr := newVTScreen(cols, newRows)
	scr.write(combined)
	all := append([]string{}, body.lines...)
	all = append(all, editor.lines...)
	all = append(all, footer.lines...)
	for fi := 0; fi < 2; fi++ {
		contentRow := len(all) - 2 + fi
		screenRow := contentRow - (len(all) - newRows)
		want := stripANSIForTest(all[contentRow])
		got := scr.rowString(screenRow)
		if want != got {
			t.Errorf("footer row %d not visible after height resize\n  want %q\n   got %q", fi, want, got)
		}
	}
}

// TestDoRender_FooterNeverOverwritten fuzzes content-height oscillations and
// line mutations against the virtual terminal, asserting that the bottom of the
// visible screen always matches the bottom of the current content (the footer).
//
// This is a general invariant guard for the differential render path: across
// hundreds of content-height changes (content growing past and shrinking back
// within the terminal height) and footer mutations, the footer's lines must
// always remain visible at the bottom of the screen. It complements
// TestDoRender_HeightResizeForcesFullRedraw, which covers terminal-height
// resizes specifically; this test holds the terminal height fixed and exercises
// the diff path's viewport/scroll bookkeeping under content churn.
func TestDoRender_FooterNeverOverwritten(t *testing.T) {
	const cols = 80
	const rows = 20

	term := NewMockTerminal(cols, rows)
	ui := NewTUI(term, false)

	body := &scrollableComp{}
	editor := &scrollableComp{}
	footer := &scrollableComp{}
	ui.AddChild(body)
	ui.AddChild(editor)
	ui.AddChild(footer)

	// The editor carries the hardware cursor (CursorMarker), so after every
	// render the cursor is positioned UP into the editor region, leaving the
	// footer below it — exactly as in the real interactive layout.
	editor.lines = []string{
		"╭──────────────────────────────────────╮",
		"│ > " + CursorMarker + "                                   │",
	}

	buildBody := func(h, salt int) []string {
		out := make([]string, h)
		for i := range out {
			out[i] = fmt.Sprintf("body %03d s%d", i, salt)
		}
		return out
	}
	footerLines := func(salt int) []string {
		return []string{
			fmt.Sprintf("~/proj (main) cost $%d.000", salt),
			fmt.Sprintf("up 1.2k down 3.4k  42.0%%/200k  model-x s%d", salt),
		}
	}

	scr := newVTScreen(cols, rows)
	rng := rand.New(rand.NewSource(1))

	apply := func() []string {
		all := append([]string{}, body.lines...)
		all = append(all, editor.lines...)
		all = append(all, footer.lines...)
		return all
	}

	// Seed full render.
	body.lines = buildBody(30, 0)
	footer.lines = footerLines(0)
	ui.RequestRender(true)
	ui.DoRender()
	for _, chunk := range term.GetOutput() {
		scr.write(chunk)
	}
	term.ClearOutput()

	for step := 1; step <= 400; step++ {
		// Randomly resize body height (oscillate around the terminal height) and
		// mutate the footer occasionally.
		h := 5 + rng.Intn(40) // 5..44
		body.lines = buildBody(h, step)
		if rng.Intn(3) == 0 {
			footer.lines = footerLines(step)
		}

		ui.DoRender()
		for _, chunk := range term.GetOutput() {
			scr.write(chunk)
		}
		term.ClearOutput()

		all := apply()
		// Expected visible region = last `rows` content lines.
		top := 0
		if len(all) > rows {
			top = len(all) - rows
		}
		// Footer occupies the final two content lines; they must be on screen.
		for fi := 0; fi < 2; fi++ {
			contentRow := len(all) - 2 + fi
			screenRow := contentRow - top
			if screenRow < 0 || screenRow >= rows {
				continue
			}
			want := stripANSIForTest(all[contentRow])
			got := scr.rowString(screenRow)
			if want != got {
				t.Fatalf("step %d: footer row %d corrupted on screen\n  want %q\n   got %q\n(content lines=%d, height=%d, screenRow=%d)",
					step, fi, want, got, len(all), rows, screenRow)
			}
		}
	}
}
