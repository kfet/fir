package components

import (
	"strings"
	"testing"
)

func TestDaxParseImage(t *testing.T) {
	pixels := daxParseImage()

	if len(pixels) != daxHeight {
		t.Fatalf("expected %d rows, got %d", daxHeight, len(pixels))
	}
	for y, row := range pixels {
		if len(row) != daxWidth {
			t.Errorf("row %d: expected %d cols, got %d", y, daxWidth, len(row))
		}
	}

	// Check pixel values are valid RGB (0-255)
	for y, row := range pixels {
		for x, px := range row {
			for c := 0; c < 3; c++ {
				if px[c] < 0 || px[c] > 255 {
					t.Errorf("pixel (%d,%d) channel %d out of range: %d", x, y, c, px[c])
				}
			}
		}
	}
}

func TestDaxBuildImage(t *testing.T) {
	image := daxBuildImage()

	// Half-block: height/2 lines
	expectedLines := daxHeight / 2
	if len(image) != expectedLines {
		t.Fatalf("expected %d image lines, got %d", expectedLines, len(image))
	}

	// Each line should contain ANSI escape codes and end with reset
	for i, line := range image {
		if !strings.Contains(line, "\x1b[") {
			t.Errorf("line %d: expected ANSI codes", i)
		}
		if !strings.HasSuffix(line, daxReset) {
			t.Errorf("line %d: expected reset suffix", i)
		}
	}
}

func TestDaxRGB(t *testing.T) {
	fg := daxRGB(100, 200, 50, false)
	if !strings.Contains(fg, "38;2;100;200;50") {
		t.Errorf("unexpected fg: %q", fg)
	}

	bg := daxRGB(100, 200, 50, true)
	if !strings.Contains(bg, "48;2;100;200;50") {
		t.Errorf("unexpected bg: %q", bg)
	}
}

func TestDaxnutsComponent_Render(t *testing.T) {
	// Create without TUI (nil) to test render
	d := &DaxnutsComponent{
		image:      daxBuildImage(),
		maxTicks:   25,
		tick:       25, // finished
		done:       make(chan struct{}),
		cachedTick: -1,
	}

	lines := d.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected render output")
	}

	// Should contain attribution text when animation is complete
	foundDaxnuts := false
	foundOpenCode := false
	for _, line := range lines {
		if strings.Contains(line, "daxnuts") {
			foundDaxnuts = true
		}
		if strings.Contains(line, "OpenCode") {
			foundOpenCode = true
		}
	}
	if !foundDaxnuts {
		t.Error("expected 'daxnuts' in output")
	}
	if !foundOpenCode {
		t.Error("expected 'OpenCode' in output")
	}
}

func TestDaxnutsComponent_RenderPartial(t *testing.T) {
	// Test render during animation (tick 0)
	d := &DaxnutsComponent{
		image:      daxBuildImage(),
		maxTicks:   25,
		tick:       0,
		done:       make(chan struct{}),
		cachedTick: -1,
	}

	lines := d.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected render output")
	}

	// Text should NOT be visible at tick 0
	for _, line := range lines {
		if strings.Contains(line, "daxnuts") {
			t.Error("text should not be visible at tick 0")
		}
	}
}

func TestDaxnutsComponent_Dispose(t *testing.T) {
	d := &DaxnutsComponent{
		image:      daxBuildImage(),
		maxTicks:   25,
		done:       make(chan struct{}),
		cachedTick: -1,
	}

	// Should not panic on double dispose
	d.Dispose()
	d.Dispose()
}

func TestDaxnutsComponent_Invalidate(t *testing.T) {
	d := &DaxnutsComponent{
		image:      daxBuildImage(),
		maxTicks:   25,
		tick:       25,
		done:       make(chan struct{}),
		cachedTick: -1,
	}

	d.Render(80)
	d.Invalidate()
	if d.cachedWidth != 0 {
		t.Error("expected cachedWidth to be 0 after invalidate")
	}
}
