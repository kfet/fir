// Package ptydriver provides a Go-native terminal multiplexer for driving
// interactive processes. It replaces the tmux dependency for headless
// agent-to-agent orchestration while keeping tmux as an optional enhancement
// for human observation.
package ptydriver

import (
	"strings"
	"sync"
	"unicode/utf8"
)

// Screen is a minimal VT100 terminal emulator that tracks a grid of cells.
// It handles enough escape sequences to accurately capture the visible output
// of typical CLI programs (shells, REPLs, build tools).
type Screen struct {
	mu   sync.Mutex
	rows int
	cols int
	grid [][]rune // grid[row][col]
	cur  struct{ r, c int }

	// scrollback holds lines that scrolled off the top.
	scrollback []string
	maxScroll  int
}

// NewScreen creates a screen with the given dimensions.
func NewScreen(rows, cols int) *Screen {
	s := &Screen{
		rows:      rows,
		cols:      cols,
		maxScroll: 10000,
	}
	s.grid = s.makeGrid()
	return s
}

func (s *Screen) makeGrid() [][]rune {
	g := make([][]rune, s.rows)
	for i := range g {
		g[i] = make([]rune, s.cols)
		for j := range g[i] {
			g[i][j] = ' '
		}
	}
	return g
}

// Write implements io.Writer. It processes raw terminal output including
// VT100/ANSI escape sequences.
func (s *Screen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	n := len(p)
	i := 0
	for i < n {
		b := p[i]
		switch {
		case b == 0x1b: // ESC
			i = s.handleEscape(p, i)
		case b == '\n':
			s.linefeed()
			i++
		case b == '\r':
			s.cur.c = 0
			i++
		case b == '\b':
			if s.cur.c > 0 {
				s.cur.c--
			}
			i++
		case b == '\t':
			s.cur.c = (s.cur.c + 8) &^ 7
			if s.cur.c >= s.cols {
				s.cur.c = s.cols - 1
			}
			i++
		case b == '\a': // bell — ignore
			i++
		case b >= 0x20:
			r, size := utf8.DecodeRune(p[i:])
			if r == utf8.RuneError && size <= 1 {
				r = rune(b)
				size = 1
			}
			s.putRune(r)
			i += size
		default:
			i++ // ignore other control chars
		}
	}
	return n, nil
}

func (s *Screen) putRune(r rune) {
	if s.cur.c >= s.cols {
		s.cur.c = 0
		s.linefeed()
	}
	s.grid[s.cur.r][s.cur.c] = r
	s.cur.c++
}

func (s *Screen) linefeed() {
	if s.cur.r == s.rows-1 {
		// Scroll up: save top line to scrollback, shift grid up.
		s.scrollback = append(s.scrollback, s.rowString(0))
		if len(s.scrollback) > s.maxScroll {
			s.scrollback = s.scrollback[len(s.scrollback)-s.maxScroll:]
		}
		copy(s.grid, s.grid[1:])
		s.grid[s.rows-1] = make([]rune, s.cols)
		for j := range s.grid[s.rows-1] {
			s.grid[s.rows-1][j] = ' '
		}
	} else {
		s.cur.r++
	}
}

func (s *Screen) handleEscape(p []byte, i int) int {
	if i+1 >= len(p) {
		return i + 1
	}
	switch p[i+1] {
	case '[': // CSI
		return s.handleCSI(p, i+2)
	case ']': // OSC — skip until ST or BEL
		j := i + 2
		for j < len(p) {
			if p[j] == '\a' {
				return j + 1
			}
			if p[j] == 0x1b && j+1 < len(p) && p[j+1] == '\\' {
				return j + 2
			}
			j++
		}
		return j
	case '(', ')': // Character set designation — skip one more byte
		if i+2 < len(p) {
			return i + 3
		}
		return i + 2
	default:
		return i + 2
	}
}

func (s *Screen) handleCSI(p []byte, start int) int {
	// Parse numeric params and final byte.
	j := start
	for j < len(p) && ((p[j] >= '0' && p[j] <= '9') || p[j] == ';' || p[j] == '?') {
		j++
	}
	if j >= len(p) {
		return j
	}
	paramStr := string(p[start:j])
	final := p[j]

	params := parseCSIParams(paramStr)

	switch final {
	case 'A': // Cursor up
		n := max(1, paramDefault(params, 0, 1))
		s.cur.r = max(0, s.cur.r-n)
	case 'B': // Cursor down
		n := max(1, paramDefault(params, 0, 1))
		s.cur.r = min(s.rows-1, s.cur.r+n)
	case 'C': // Cursor forward
		n := max(1, paramDefault(params, 0, 1))
		s.cur.c = min(s.cols-1, s.cur.c+n)
	case 'D': // Cursor backward
		n := max(1, paramDefault(params, 0, 1))
		s.cur.c = max(0, s.cur.c-n)
	case 'H', 'f': // Cursor position
		row := paramDefault(params, 0, 1) - 1
		col := paramDefault(params, 1, 1) - 1
		s.cur.r = clamp(row, 0, s.rows-1)
		s.cur.c = clamp(col, 0, s.cols-1)
	case 'J': // Erase in display
		n := paramDefault(params, 0, 0)
		switch n {
		case 0: // Erase below
			s.clearRange(s.cur.r, s.cur.c, s.rows-1, s.cols-1)
		case 1: // Erase above
			s.clearRange(0, 0, s.cur.r, s.cur.c)
		case 2, 3: // Erase all
			s.grid = s.makeGrid()
			s.cur.r, s.cur.c = 0, 0
		}
	case 'K': // Erase in line
		n := paramDefault(params, 0, 0)
		switch n {
		case 0: // Erase to right
			for c := s.cur.c; c < s.cols; c++ {
				s.grid[s.cur.r][c] = ' '
			}
		case 1: // Erase to left
			for c := 0; c <= s.cur.c; c++ {
				s.grid[s.cur.r][c] = ' '
			}
		case 2: // Erase line
			for c := 0; c < s.cols; c++ {
				s.grid[s.cur.r][c] = ' '
			}
		}
	case 'm': // SGR (colors/attributes) — ignore, we just track text
	case 'r': // Set scrolling region — ignore for now
	case 'h', 'l': // Mode set/reset — ignore
	case 'G': // Cursor horizontal absolute
		col := paramDefault(params, 0, 1) - 1
		s.cur.c = clamp(col, 0, s.cols-1)
	case 'd': // Cursor vertical absolute
		row := paramDefault(params, 0, 1) - 1
		s.cur.r = clamp(row, 0, s.rows-1)
	case 'L': // Insert lines
		n := max(1, paramDefault(params, 0, 1))
		for range n {
			if s.cur.r < s.rows-1 {
				copy(s.grid[s.cur.r+1:], s.grid[s.cur.r:s.rows-1])
			}
			s.grid[s.cur.r] = make([]rune, s.cols)
			for c := range s.grid[s.cur.r] {
				s.grid[s.cur.r][c] = ' '
			}
		}
	case 'M': // Delete lines
		n := max(1, paramDefault(params, 0, 1))
		for range n {
			if s.cur.r < s.rows-1 {
				copy(s.grid[s.cur.r:], s.grid[s.cur.r+1:])
			}
			s.grid[s.rows-1] = make([]rune, s.cols)
			for c := range s.grid[s.rows-1] {
				s.grid[s.rows-1][c] = ' '
			}
		}
	}
	return j + 1
}

func (s *Screen) clearRange(r1, c1, r2, c2 int) {
	for r := r1; r <= r2 && r < s.rows; r++ {
		startC, endC := 0, s.cols-1
		if r == r1 {
			startC = c1
		}
		if r == r2 {
			endC = c2
		}
		for c := startC; c <= endC && c < s.cols; c++ {
			s.grid[r][c] = ' '
		}
	}
}

func (s *Screen) rowString(r int) string {
	return strings.TrimRight(string(s.grid[r]), " ")
}

// Capture returns the last n lines of output (scrollback + visible screen),
// similar to tmux capture-pane. Lines are joined with newlines.
func (s *Screen) Capture(lines int) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Collect visible lines (skip trailing empty lines).
	var visible []string
	lastNonEmpty := -1
	for r := 0; r < s.rows; r++ {
		line := s.rowString(r)
		visible = append(visible, line)
		if line != "" {
			lastNonEmpty = r
		}
	}
	visible = visible[:lastNonEmpty+1]

	// Combine scrollback + visible.
	all := make([]string, 0, len(s.scrollback)+len(visible))
	all = append(all, s.scrollback...)
	all = append(all, visible...)

	if lines > 0 && lines < len(all) {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}

// CaptureVisible returns only the visible screen content.
func (s *Screen) CaptureVisible() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var lines []string
	lastNonEmpty := -1
	for r := 0; r < s.rows; r++ {
		line := s.rowString(r)
		lines = append(lines, line)
		if line != "" {
			lastNonEmpty = r
		}
	}
	if lastNonEmpty < 0 {
		return ""
	}
	return strings.Join(lines[:lastNonEmpty+1], "\n")
}

// Helper functions

func parseCSIParams(s string) []int {
	if s == "" || s[0] == '?' {
		return nil
	}
	parts := strings.Split(s, ";")
	params := make([]int, len(parts))
	for i, p := range parts {
		v := 0
		for _, ch := range p {
			if ch >= '0' && ch <= '9' {
				v = v*10 + int(ch-'0')
			}
		}
		params[i] = v
	}
	return params
}

func paramDefault(params []int, idx, def int) int {
	if idx < len(params) && params[idx] > 0 {
		return params[idx]
	}
	return def
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
