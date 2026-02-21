// Ported from: packages/coding-agent/src/modes/interactive/components/armin.ts
// Upstream hash: 1caadb2e
package components

import (
	"math/rand"
	"strings"
	"time"

	"github.com/kfet/fir/pkg/modes/interactive/theme"
	"github.com/kfet/fir/pkg/tui"
)

// XBM image constants for the Armin easter egg.
const (
	arminWidth      = 31
	arminHeight     = 36
	arminBytesPerRow = (arminWidth + 7) / 8 // ceil(31/8) = 4
	arminDisplayHeight = arminHeight / 2     // half-block rendering: 18 rows
)

// arminBits is the XBM image data: 31×36 pixels, LSB first, 1=background, 0=foreground.
var arminBits = []byte{
	0xff, 0xff, 0xff, 0x7f, 0xff, 0xf0, 0xff, 0x7f, 0xff, 0xed, 0xff, 0x7f,
	0xff, 0xdb, 0xff, 0x7f, 0xff, 0xb7, 0xff, 0x7f, 0xff, 0x77, 0xfe, 0x7f,
	0x3f, 0xf8, 0xfe, 0x7f, 0xdf, 0xff, 0xfe, 0x7f, 0xdf, 0x3f, 0xfc, 0x7f,
	0x9f, 0xc3, 0xfb, 0x7f, 0x6f, 0xfc, 0xf4, 0x7f, 0xf7, 0x0f, 0xf7, 0x7f,
	0xf7, 0xff, 0xf7, 0x7f, 0xf7, 0xff, 0xe3, 0x7f, 0xf7, 0x07, 0xe8, 0x7f,
	0xef, 0xf8, 0x67, 0x70, 0x0f, 0xff, 0xbb, 0x6f, 0xf1, 0x00, 0xd0, 0x5b,
	0xfd, 0x3f, 0xec, 0x53, 0xc1, 0xff, 0xef, 0x57, 0x9f, 0xfd, 0xee, 0x5f,
	0x9f, 0xfc, 0xae, 0x5f, 0x1f, 0x78, 0xac, 0x5f, 0x3f, 0x00, 0x50, 0x6c,
	0x7f, 0x00, 0xdc, 0x77, 0xff, 0xc0, 0x3f, 0x78, 0xff, 0x01, 0xf8, 0x7f,
	0xff, 0x03, 0x9c, 0x78, 0xff, 0x07, 0x8c, 0x7c, 0xff, 0x0f, 0xce, 0x78,
	0xff, 0xff, 0xcf, 0x7f, 0xff, 0xff, 0xcf, 0x78, 0xff, 0xff, 0xdf, 0x78,
	0xff, 0xff, 0xdf, 0x7d, 0xff, 0xff, 0x3f, 0x7e, 0xff, 0xff, 0xff, 0x7f,
}

// arminEffect is an animation effect type for Armin.
type arminEffect int

const (
	effectTypewriter arminEffect = iota
	effectScanline
	effectRain
	effectFade
	effectCRT
	effectGlitch
	effectDissolve
)

var arminEffects = []arminEffect{
	effectTypewriter, effectScanline, effectRain,
	effectFade, effectCRT, effectGlitch, effectDissolve,
}

// arminGetPixel returns true if (x,y) is foreground (ink), false for background.
func arminGetPixel(x, y int) bool {
	if y >= arminHeight {
		return false
	}
	byteIndex := y*arminBytesPerRow + x/8
	bitIndex := x % 8
	return (arminBits[byteIndex]>>uint(bitIndex))&1 == 0
}

// arminGetChar returns the half-block character for a cell (2 vertical pixels packed).
func arminGetChar(x, row int) string {
	upper := arminGetPixel(x, row*2)
	lower := arminGetPixel(x, row*2+1)
	switch {
	case upper && lower:
		return "█"
	case upper:
		return "▀"
	case lower:
		return "▄"
	default:
		return " "
	}
}

// arminBuildFinalGrid builds the fully revealed character grid.
func arminBuildFinalGrid() [][]string {
	grid := make([][]string, arminDisplayHeight)
	for row := 0; row < arminDisplayHeight; row++ {
		line := make([]string, arminWidth)
		for x := 0; x < arminWidth; x++ {
			line[x] = arminGetChar(x, row)
		}
		grid[row] = line
	}
	return grid
}

// ArminComponent is the "Armin says hi!" easter egg with animated XBM art.
type ArminComponent struct {
	ui           *tui.TUI
	effect       arminEffect
	finalGrid    [][]string
	currentGrid  [][]string
	effectState  map[string]interface{}
	cachedLines  []string
	cachedWidth  int
	gridVersion  int
	cachedVer    int
	ticker       *time.Ticker
	stopCh       chan struct{}
}

// NewArminComponent creates and starts the Armin easter egg.
func NewArminComponent(ui *tui.TUI) *ArminComponent {
	a := &ArminComponent{
		ui:          ui,
		effect:      arminEffects[rand.Intn(len(arminEffects))],
		finalGrid:   arminBuildFinalGrid(),
		effectState: make(map[string]interface{}),
		cachedVer:   -1,
		stopCh:      make(chan struct{}),
	}
	a.currentGrid = a.createEmptyGrid()
	a.initEffect()
	a.startAnimation()
	return a
}

// Invalidate clears cached rendering state.
func (a *ArminComponent) Invalidate() {
	a.cachedWidth = 0
}

// Render renders the Armin easter egg.
func (a *ArminComponent) Render(width int) []string {
	if width == a.cachedWidth && a.cachedVer == a.gridVersion {
		return a.cachedLines
	}

	padding := 1
	availableWidth := width - padding

	lines := make([]string, 0, len(a.currentGrid)+1)
	for _, row := range a.currentGrid {
		clipped := row
		if len(clipped) > availableWidth {
			clipped = clipped[:availableWidth]
		}
		content := strings.Join(clipped, "")
		padRight := width - padding - len(clipped)
		if padRight < 0 {
			padRight = 0
		}
		lines = append(lines, " "+theme.GetTheme().Fg("accent", content)+strings.Repeat(" ", padRight))
	}

	// "ARMIN SAYS HI" message
	message := "ARMIN SAYS HI"
	msgPad := width - padding - len(message)
	if msgPad < 0 {
		msgPad = 0
	}
	lines = append(lines, " "+theme.GetTheme().Fg("accent", message)+strings.Repeat(" ", msgPad))

	a.cachedLines = lines
	a.cachedWidth = width
	a.cachedVer = a.gridVersion
	return lines
}

func (a *ArminComponent) createEmptyGrid() [][]string {
	grid := make([][]string, arminDisplayHeight)
	for row := range grid {
		grid[row] = make([]string, arminWidth)
		for x := range grid[row] {
			grid[row][x] = " "
		}
	}
	return grid
}

func (a *ArminComponent) initEffect() {
	switch a.effect {
	case effectTypewriter:
		a.effectState["pos"] = 0
	case effectScanline:
		a.effectState["row"] = 0
	case effectRain:
		drops := make([]rainDrop, arminWidth)
		for i := range drops {
			drops[i] = rainDrop{y: -rand.Intn(arminDisplayHeight * 2)}
		}
		a.effectState["drops"] = drops
	case effectFade:
		positions := shuffledPositions()
		a.effectState["positions"] = positions
		a.effectState["idx"] = 0
	case effectCRT:
		a.effectState["expansion"] = 0
	case effectGlitch:
		a.effectState["phase"] = 0
		a.effectState["glitchFrames"] = 8
	case effectDissolve:
		// Start with random noise
		chars := []string{" ", "░", "▒", "▓", "█", "▀", "▄"}
		for row := 0; row < arminDisplayHeight; row++ {
			for x := 0; x < arminWidth; x++ {
				a.currentGrid[row][x] = chars[rand.Intn(len(chars))]
			}
		}
		positions := shuffledPositions()
		a.effectState["positions"] = positions
		a.effectState["idx"] = 0
	}
}

type rainDrop struct {
	y       int
	settled int
}

func shuffledPositions() [][2]int {
	positions := make([][2]int, 0, arminDisplayHeight*arminWidth)
	for row := 0; row < arminDisplayHeight; row++ {
		for x := 0; x < arminWidth; x++ {
			positions = append(positions, [2]int{row, x})
		}
	}
	rand.Shuffle(len(positions), func(i, j int) {
		positions[i], positions[j] = positions[j], positions[i]
	})
	return positions
}

func (a *ArminComponent) startAnimation() {
	fps := 30
	if a.effect == effectGlitch {
		fps = 60
	}
	a.ticker = time.NewTicker(time.Second / time.Duration(fps))
	go func() {
		for {
			select {
			case <-a.ticker.C:
				done := a.tickEffect()
				a.gridVersion++
				a.ui.RequestRender(false)
				if done {
					a.stopAnimation()
					return
				}
			case <-a.stopCh:
				return
			}
		}
	}()
}

func (a *ArminComponent) stopAnimation() {
	if a.ticker != nil {
		a.ticker.Stop()
		a.ticker = nil
	}
}

func (a *ArminComponent) tickEffect() bool {
	switch a.effect {
	case effectTypewriter:
		return a.tickTypewriter()
	case effectScanline:
		return a.tickScanline()
	case effectRain:
		return a.tickRain()
	case effectFade:
		return a.tickFade()
	case effectCRT:
		return a.tickCRT()
	case effectGlitch:
		return a.tickGlitch()
	case effectDissolve:
		return a.tickDissolve()
	default:
		return true
	}
}

func (a *ArminComponent) tickTypewriter() bool {
	pos := a.effectState["pos"].(int)
	pixelsPerFrame := 3
	for i := 0; i < pixelsPerFrame; i++ {
		row := pos / arminWidth
		x := pos % arminWidth
		if row >= arminDisplayHeight {
			return true
		}
		a.currentGrid[row][x] = a.finalGrid[row][x]
		pos++
	}
	a.effectState["pos"] = pos
	return false
}

func (a *ArminComponent) tickScanline() bool {
	row := a.effectState["row"].(int)
	if row >= arminDisplayHeight {
		return true
	}
	for x := 0; x < arminWidth; x++ {
		a.currentGrid[row][x] = a.finalGrid[row][x]
	}
	a.effectState["row"] = row + 1
	return false
}

func (a *ArminComponent) tickRain() bool {
	drops := a.effectState["drops"].([]rainDrop)
	allSettled := true
	a.currentGrid = a.createEmptyGrid()

	for x := 0; x < arminWidth; x++ {
		drop := &drops[x]

		// Draw settled pixels
		for row := arminDisplayHeight - 1; row >= arminDisplayHeight-drop.settled; row-- {
			if row >= 0 {
				a.currentGrid[row][x] = a.finalGrid[row][x]
			}
		}

		if drop.settled >= arminDisplayHeight {
			continue
		}
		allSettled = false

		// Find target row (lowest non-space pixel)
		targetRow := -1
		for row := arminDisplayHeight - 1 - drop.settled; row >= 0; row-- {
			if a.finalGrid[row][x] != " " {
				targetRow = row
				break
			}
		}

		drop.y++
		if drop.y >= 0 && drop.y < arminDisplayHeight {
			if targetRow >= 0 && drop.y >= targetRow {
				drop.settled = arminDisplayHeight - targetRow
				drop.y = -rand.Intn(5) - 1
			} else {
				a.currentGrid[drop.y][x] = "▓"
			}
		}
	}
	a.effectState["drops"] = drops
	return allSettled
}

func (a *ArminComponent) tickFade() bool {
	positions := a.effectState["positions"].([][2]int)
	idx := a.effectState["idx"].(int)
	pixelsPerFrame := 15
	for i := 0; i < pixelsPerFrame; i++ {
		if idx >= len(positions) {
			return true
		}
		pos := positions[idx]
		a.currentGrid[pos[0]][pos[1]] = a.finalGrid[pos[0]][pos[1]]
		idx++
	}
	a.effectState["idx"] = idx
	return false
}

func (a *ArminComponent) tickCRT() bool {
	expansion := a.effectState["expansion"].(int)
	midRow := arminDisplayHeight / 2
	a.currentGrid = a.createEmptyGrid()

	top := midRow - expansion
	bottom := midRow + expansion
	for row := max(0, top); row <= min(arminDisplayHeight-1, bottom); row++ {
		for x := 0; x < arminWidth; x++ {
			a.currentGrid[row][x] = a.finalGrid[row][x]
		}
	}
	expansion++
	a.effectState["expansion"] = expansion
	return expansion > arminDisplayHeight
}

func (a *ArminComponent) tickGlitch() bool {
	phase := a.effectState["phase"].(int)
	glitchFrames := a.effectState["glitchFrames"].(int)

	if phase < glitchFrames {
		// Glitch phase: show corrupted version
		for row := 0; row < arminDisplayHeight; row++ {
			glitchRow := make([]string, arminWidth)
			copy(glitchRow, a.finalGrid[row])

			if rand.Float64() < 0.3 {
				// Random horizontal offset
				offset := rand.Intn(7) - 3
				shifted := make([]string, arminWidth)
				for x := 0; x < arminWidth; x++ {
					srcX := x - offset
					if srcX >= 0 && srcX < arminWidth {
						shifted[x] = glitchRow[srcX]
					} else {
						shifted[x] = " "
					}
				}
				a.currentGrid[row] = shifted
			} else if rand.Float64() < 0.2 {
				// Random vertical swap
				swapRow := rand.Intn(arminDisplayHeight)
				copy(a.currentGrid[row], a.finalGrid[swapRow])
			} else {
				copy(a.currentGrid[row], glitchRow)
			}
		}
		a.effectState["phase"] = phase + 1
		return false
	}

	// Final frame: clean image
	for row := 0; row < arminDisplayHeight; row++ {
		copy(a.currentGrid[row], a.finalGrid[row])
	}
	return true
}

func (a *ArminComponent) tickDissolve() bool {
	positions := a.effectState["positions"].([][2]int)
	idx := a.effectState["idx"].(int)
	pixelsPerFrame := 20
	for i := 0; i < pixelsPerFrame; i++ {
		if idx >= len(positions) {
			return true
		}
		pos := positions[idx]
		a.currentGrid[pos[0]][pos[1]] = a.finalGrid[pos[0]][pos[1]]
		idx++
	}
	a.effectState["idx"] = idx
	return false
}

// Dispose stops the animation and releases resources.
func (a *ArminComponent) Dispose() {
	a.stopAnimation()
	select {
	case <-a.stopCh:
		// already closed
	default:
		close(a.stopCh)
	}
}
