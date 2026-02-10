// Ported from: packages/tui/src/components/spacer.ts
// Upstream hash: 1caadb2e
package components

// Spacer renders empty lines.
type Spacer struct {
	lines int
}

// NewSpacer creates a Spacer with the given number of empty lines.
func NewSpacer(lines int) *Spacer {
	if lines < 1 {
		lines = 1
	}
	return &Spacer{lines: lines}
}

// SetLines changes the number of empty lines.
func (s *Spacer) SetLines(lines int) {
	s.lines = lines
}

// Invalidate is a no-op for Spacer.
func (s *Spacer) Invalidate() {}

// Render returns empty lines for the given width.
func (s *Spacer) Render(width int) []string {
	result := make([]string, s.lines)
	return result
}
