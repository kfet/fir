package components

import (
	"testing"
)

func TestArminBuildFinalGrid(t *testing.T) {
	grid := arminBuildFinalGrid()

	if len(grid) != arminDisplayHeight {
		t.Fatalf("expected %d rows, got %d", arminDisplayHeight, len(grid))
	}
	for i, row := range grid {
		if len(row) != arminWidth {
			t.Errorf("row %d: expected %d cols, got %d", i, arminWidth, len(row))
		}
	}

	// Grid should contain some non-space characters (the image)
	hasContent := false
	for _, row := range grid {
		for _, ch := range row {
			if ch != " " {
				hasContent = true
				break
			}
		}
	}
	if !hasContent {
		t.Error("expected grid to contain non-space characters")
	}
}

func TestArminGetPixel(t *testing.T) {
	// Spot check: pixel at (0,0) should be valid
	_ = arminGetPixel(0, 0)
	_ = arminGetPixel(arminWidth-1, arminHeight-1)

	// Out of bounds should return false
	if arminGetPixel(0, arminHeight) {
		t.Error("expected false for out-of-bounds pixel")
	}
}

func TestArminGetChar(t *testing.T) {
	ch := arminGetChar(0, 0)
	valid := ch == "█" || ch == "▀" || ch == "▄" || ch == " "
	if !valid {
		t.Errorf("unexpected character: %q", ch)
	}
}

func TestArminComponent_Render(t *testing.T) {
	a := &ArminComponent{
		finalGrid:   arminBuildFinalGrid(),
		currentGrid: arminBuildFinalGrid(),
		effectState: make(map[string]interface{}),
		stopCh:      make(chan struct{}),
		cachedVer:   -1,
	}

	lines := a.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected render output")
	}
}

func TestArminComponent_Dispose(t *testing.T) {
	a := &ArminComponent{
		finalGrid:   arminBuildFinalGrid(),
		effectState: make(map[string]interface{}),
		stopCh:      make(chan struct{}),
		cachedVer:   -1,
	}

	// Should not panic on double dispose
	a.Dispose()
	a.Dispose()
}

func TestArminComponent_Invalidate(t *testing.T) {
	a := &ArminComponent{
		finalGrid:   arminBuildFinalGrid(),
		currentGrid: arminBuildFinalGrid(),
		effectState: make(map[string]interface{}),
		stopCh:      make(chan struct{}),
		cachedVer:   -1,
	}

	a.Render(80)
	a.Invalidate()
	if a.cachedWidth != 0 {
		t.Error("expected cachedWidth to be 0 after invalidate")
	}
}
