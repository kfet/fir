package components

import (
	"strings"
	"testing"
)

func TestParseDaxImage(t *testing.T) {
	pixels := daxParseImage()

	// Verify dimensions
	if len(pixels) != daxHeight {
		t.Fatalf("expected %d rows, got %d", daxHeight, len(pixels))
	}
	if len(pixels[0]) != daxWidth {
		t.Fatalf("expected %d columns, got %d", daxWidth, len(pixels[0]))
	}

	// Check first pixel (from hex: bb ba b8)
	if pixels[0][0] != [3]int{0xbb, 0xba, 0xb8} {
		t.Errorf("first pixel = %v, want [0xbb, 0xba, 0xb8]", pixels[0][0])
	}
}

func TestBuildDaxImage(t *testing.T) {
	lines := daxBuildImage()

	// Half-block rendering: 32 rows / 2 = 16 lines
	if len(lines) != daxHeight/2 {
		t.Fatalf("expected %d image lines, got %d", daxHeight/2, len(lines))
	}

	// Each line should contain half-block characters
	for i, line := range lines {
		if !strings.Contains(line, "▄") {
			t.Errorf("line %d should contain '▄' characters", i)
		}
		// Each line should end with ANSI reset
		if !strings.HasSuffix(line, daxReset) {
			t.Errorf("line %d should end with ANSI reset", i)
		}
	}
}

func TestRgbAnsi(t *testing.T) {
	fg := daxRGB(255, 128, 0, false)
	if fg != "\x1b[38;2;255;128;0m" {
		t.Errorf("fg ANSI = %q, want \\x1b[38;2;255;128;0m", fg)
	}

	bg := daxRGB(0, 0, 0, true)
	if bg != "\x1b[48;2;0;0;0m" {
		t.Errorf("bg ANSI = %q, want \\x1b[48;2;0;0;0m", bg)
	}
}

func TestDaxnutsComponentRender(t *testing.T) {
	// Test rendering without a real TUI (pass nil — we won't call startAnimation)
	d := &DaxnutsComponent{
		image:      daxBuildImage(),
		maxTicks:   25,
		tick:       0,
		cachedTick: -1,
		done:       make(chan struct{}),
	}

	lines := d.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected non-empty render output")
	}

	// At tick 0, text should not be shown yet (textPhase <= 0)
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "Powered by daxnuts") {
		t.Error("text should not be visible at tick 0")
	}

	// At maxTicks, everything should be visible
	d.tick = d.maxTicks
	d.cachedWidth = 0 // invalidate cache
	lines = d.Render(80)
	joined = strings.Join(lines, "\n")
	if !strings.Contains(joined, "daxnuts") {
		t.Error("text should be visible at maxTicks")
	}
}

func TestDaxnutsComponentInvalidate(t *testing.T) {
	d := &DaxnutsComponent{
		image:       daxBuildImage(),
		maxTicks:    25,
		cachedWidth: 80,
		cachedTick:  5,
		done:        make(chan struct{}),
	}
	d.Invalidate()
	if d.cachedWidth != 0 {
		t.Error("Invalidate should reset cachedWidth to 0")
	}
}
