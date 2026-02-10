package components

import "testing"

func TestSpacer_Default(t *testing.T) {
	s := NewSpacer(1)
	lines := s.Render(80)
	if len(lines) != 1 {
		t.Errorf("expected 1 line, got %d", len(lines))
	}
	if lines[0] != "" {
		t.Errorf("expected empty line, got %q", lines[0])
	}
}

func TestSpacer_MultipleLines(t *testing.T) {
	s := NewSpacer(3)
	lines := s.Render(80)
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(lines))
	}
	for i, line := range lines {
		if line != "" {
			t.Errorf("line %d: expected empty, got %q", i, line)
		}
	}
}

func TestSpacer_SetLines(t *testing.T) {
	s := NewSpacer(1)
	s.SetLines(5)
	lines := s.Render(80)
	if len(lines) != 5 {
		t.Errorf("expected 5 lines, got %d", len(lines))
	}
}

func TestSpacer_Invalidate(t *testing.T) {
	s := NewSpacer(1)
	s.Invalidate() // should not panic
}

func TestSpacer_ZeroClampedToOne(t *testing.T) {
	s := NewSpacer(0)
	lines := s.Render(80)
	if len(lines) != 1 {
		t.Errorf("expected 1 line (clamped from 0), got %d", len(lines))
	}
}
