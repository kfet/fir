package components

import (
	"strings"
	"testing"
)

func TestArminGetPixel(t *testing.T) {
	// Top-left corner: 0xff means all bits set (LSB first → all background).
	// Pixel(0,0): byte=0xff, bit 0=1 → !0=false → background
	if arminGetPixel(0, 0) {
		t.Error("expected pixel (0,0) to be background (false)")
	}
}

func TestArminGetChar(t *testing.T) {
	ch := arminGetChar(0, 0)
	// Should be one of "█", "▀", "▄", " "
	valid := ch == "█" || ch == "▀" || ch == "▄" || ch == " "
	if !valid {
		t.Errorf("arminGetChar(0,0) = %q, expected one of block chars or space", ch)
	}
}

func TestArminBuildFinalGrid(t *testing.T) {
	grid := arminBuildFinalGrid()
	if len(grid) != arminDisplayHeight {
		t.Fatalf("expected %d rows, got %d", arminDisplayHeight, len(grid))
	}
	for i, row := range grid {
		if len(row) != arminWidth {
			t.Fatalf("row %d: expected %d columns, got %d", i, arminWidth, len(row))
		}
	}
}

func TestArminComponentRender(t *testing.T) {
	// Create component without TUI for testing
	a := &ArminComponent{
		effect:      effectScanline,
		finalGrid:   arminBuildFinalGrid(),
		effectState: make(map[string]interface{}),
		cachedVer:   -1,
		stopCh:      make(chan struct{}),
	}
	a.currentGrid = a.createEmptyGrid()

	lines := a.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected non-empty render output")
	}

	// Should have displayHeight + 1 lines (grid + "ARMIN SAYS HI")
	if len(lines) != arminDisplayHeight+1 {
		t.Errorf("expected %d lines, got %d", arminDisplayHeight+1, len(lines))
	}

	// Last line should contain "ARMIN SAYS HI"
	last := lines[len(lines)-1]
	if !strings.Contains(last, "ARMIN SAYS HI") {
		t.Errorf("last line should contain 'ARMIN SAYS HI', got %q", last)
	}
}

func TestArminComponentInvalidate(t *testing.T) {
	a := &ArminComponent{
		cachedWidth: 80,
		stopCh:      make(chan struct{}),
	}
	a.Invalidate()
	if a.cachedWidth != 0 {
		t.Error("Invalidate should reset cachedWidth to 0")
	}
}

func TestArminEffectTypewriter(t *testing.T) {
	a := &ArminComponent{
		effect:      effectTypewriter,
		finalGrid:   arminBuildFinalGrid(),
		effectState: map[string]interface{}{"pos": 0},
		stopCh:      make(chan struct{}),
	}
	a.currentGrid = a.createEmptyGrid()

	// Tick once - should fill 3 pixels
	done := a.tickTypewriter()
	if done {
		t.Error("typewriter should not be done after one tick")
	}
	pos := a.effectState["pos"].(int)
	if pos != 3 {
		t.Errorf("expected pos=3 after one tick, got %d", pos)
	}

	// Run until done
	for !done {
		done = a.tickTypewriter()
	}

	// Grid should match final
	for row := 0; row < arminDisplayHeight; row++ {
		for x := 0; x < arminWidth; x++ {
			if a.currentGrid[row][x] != a.finalGrid[row][x] {
				t.Fatalf("pixel (%d,%d) doesn't match final grid", x, row)
			}
		}
	}
}

func TestArminEffectScanline(t *testing.T) {
	a := &ArminComponent{
		effect:      effectScanline,
		finalGrid:   arminBuildFinalGrid(),
		effectState: map[string]interface{}{"row": 0},
		stopCh:      make(chan struct{}),
	}
	a.currentGrid = a.createEmptyGrid()

	for i := 0; i < arminDisplayHeight; i++ {
		done := a.tickScanline()
		if i < arminDisplayHeight-1 && done {
			t.Fatalf("scanline should not be done at row %d", i)
		}
	}
	done := a.tickScanline()
	if !done {
		t.Error("scanline should be done after all rows")
	}
}

func TestArminEffectCRT(t *testing.T) {
	a := &ArminComponent{
		effect:      effectCRT,
		finalGrid:   arminBuildFinalGrid(),
		effectState: map[string]interface{}{"expansion": 0},
		stopCh:      make(chan struct{}),
	}
	a.currentGrid = a.createEmptyGrid()

	done := false
	for !done {
		done = a.tickCRT()
	}

	// Center rows should now be populated
	mid := arminDisplayHeight / 2
	if a.currentGrid[mid][0] != a.finalGrid[mid][0] {
		t.Error("center row should match final grid after CRT effect completes")
	}
}

func TestShuffledPositions(t *testing.T) {
	positions := shuffledPositions()
	expected := arminDisplayHeight * arminWidth
	if len(positions) != expected {
		t.Fatalf("expected %d positions, got %d", expected, len(positions))
	}

	// All positions should be valid
	for _, pos := range positions {
		if pos[0] < 0 || pos[0] >= arminDisplayHeight || pos[1] < 0 || pos[1] >= arminWidth {
			t.Fatalf("invalid position: %v", pos)
		}
	}
}
