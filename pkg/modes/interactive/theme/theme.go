// Ported from: packages/coding-agent/src/modes/interactive/theme/theme.ts
// Upstream hash: 1caadb2e
package theme

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kfet/fir/pkg/tui/components"
)

//go:embed themes/*.json
var embeddedThemesFS embed.FS

// ============================================================================
// Types
// ============================================================================

// ThemeColor is a foreground color name in the theme (e.g. "accent", "error").
type ThemeColor = string

// ThemeBg is a background color name in the theme (e.g. "selectedBg").
type ThemeBg = string

// ColorMode is the terminal color support level.
type ColorMode string

const (
	ColorModeTruecolor ColorMode = "truecolor"
	ColorMode256       ColorMode = "256color"
)

// ============================================================================
// Color Utilities
// ============================================================================

// DetectColorMode detects the terminal's color support.
func DetectColorMode() ColorMode {
	colorterm := os.Getenv("COLORTERM")
	if colorterm == "truecolor" || colorterm == "24bit" {
		return ColorModeTruecolor
	}
	if os.Getenv("WT_SESSION") != "" {
		return ColorModeTruecolor
	}
	term := os.Getenv("TERM")
	if term == "dumb" || term == "" || term == "linux" {
		return ColorMode256
	}
	if os.Getenv("TERM_PROGRAM") == "Apple_Terminal" {
		return ColorMode256
	}
	return ColorModeTruecolor
}

// hexToRGB parses a hex color string like "#ff0000" or "ff0000" to RGB components.
func hexToRGB(hex string) (r, g, b int, err error) {
	cleaned := strings.TrimPrefix(hex, "#")
	if len(cleaned) != 6 {
		return 0, 0, 0, fmt.Errorf("invalid hex color: %s", hex)
	}
	ri, e := strconv.ParseInt(cleaned[0:2], 16, 32)
	if e != nil {
		return 0, 0, 0, fmt.Errorf("invalid hex color: %s", hex)
	}
	gi, e := strconv.ParseInt(cleaned[2:4], 16, 32)
	if e != nil {
		return 0, 0, 0, fmt.Errorf("invalid hex color: %s", hex)
	}
	bi, e := strconv.ParseInt(cleaned[4:6], 16, 32)
	if e != nil {
		return 0, 0, 0, fmt.Errorf("invalid hex color: %s", hex)
	}
	return int(ri), int(gi), int(bi), nil
}

// 6x6x6 color cube channel values
var cubeValues = [6]int{0, 95, 135, 175, 215, 255}

func findClosestCubeIndex(value int) int {
	minDist := math.MaxInt32
	minIdx := 0
	for i, v := range cubeValues {
		d := value - v
		if d < 0 {
			d = -d
		}
		if d < minDist {
			minDist = d
			minIdx = i
		}
	}
	return minIdx
}

// Grayscale ramp values (indices 232-255)
var grayValues [24]int

func init() {
	for i := 0; i < 24; i++ {
		grayValues[i] = 8 + i*10
	}
}

func findClosestGrayIndex(gray int) int {
	minDist := math.MaxInt32
	minIdx := 0
	for i, v := range grayValues {
		d := gray - v
		if d < 0 {
			d = -d
		}
		if d < minDist {
			minDist = d
			minIdx = i
		}
	}
	return minIdx
}

func colorDistance(r1, g1, b1, r2, g2, b2 int) float64 {
	dr := float64(r1 - r2)
	dg := float64(g1 - g2)
	db := float64(b1 - b2)
	return dr*dr*0.299 + dg*dg*0.587 + db*db*0.114
}

func rgbTo256(r, g, b int) int {
	rIdx := findClosestCubeIndex(r)
	gIdx := findClosestCubeIndex(g)
	bIdx := findClosestCubeIndex(b)
	cubeR := cubeValues[rIdx]
	cubeG := cubeValues[gIdx]
	cubeB := cubeValues[bIdx]
	cubeIndex := 16 + 36*rIdx + 6*gIdx + bIdx
	cubeDist := colorDistance(r, g, b, cubeR, cubeG, cubeB)

	gray := int(math.Round(0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)))
	grayIdx := findClosestGrayIndex(gray)
	grayValue := grayValues[grayIdx]
	grayIndex := 232 + grayIdx
	grayDist := colorDistance(r, g, b, grayValue, grayValue, grayValue)

	maxC := r
	if g > maxC {
		maxC = g
	}
	if b > maxC {
		maxC = b
	}
	minC := r
	if g < minC {
		minC = g
	}
	if b < minC {
		minC = b
	}
	spread := maxC - minC

	if spread < 30 && grayDist < cubeDist {
		return grayIndex
	}
	return cubeIndex
}

// fgAnsi generates a foreground ANSI escape for a hex color string.
// Empty string returns the default fg reset. Non-hex strings also reset.
func fgAnsi(hex string, mode ColorMode) string {
	if hex == "" || !strings.HasPrefix(hex, "#") {
		return "\x1b[39m"
	}
	if mode == ColorModeTruecolor {
		r, g, b, err := hexToRGB(hex)
		if err != nil {
			return "\x1b[39m"
		}
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
	}
	r, g, b, err := hexToRGB(hex)
	if err != nil {
		return "\x1b[39m"
	}
	return fmt.Sprintf("\x1b[38;5;%dm", rgbTo256(r, g, b))
}

// bgAnsi generates a background ANSI escape for a hex color string.
func bgAnsi(hex string, mode ColorMode) string {
	if hex == "" || !strings.HasPrefix(hex, "#") {
		return "\x1b[49m"
	}
	if mode == ColorModeTruecolor {
		r, g, b, err := hexToRGB(hex)
		if err != nil {
			return "\x1b[49m"
		}
		return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r, g, b)
	}
	r, g, b, err := hexToRGB(hex)
	if err != nil {
		return "\x1b[49m"
	}
	return fmt.Sprintf("\x1b[48;5;%dm", rgbTo256(r, g, b))
}

// ============================================================================
// Variable Resolution
// ============================================================================

// resolveVarRef resolves a color variable reference through the vars map.
func resolveVarRef(name string, vars map[string]string, visited map[string]bool) (string, error) {
	if name == "" || strings.HasPrefix(name, "#") {
		return name, nil
	}
	if visited[name] {
		return "", fmt.Errorf("circular variable reference: %s", name)
	}
	resolved, ok := vars[name]
	if !ok {
		return "", fmt.Errorf("variable not found: %s", name)
	}
	visited[name] = true
	return resolveVarRef(resolved, vars, visited)
}

// resolveColor resolves a color value, returning "" on error.
func resolveColor(name string, vars map[string]string) string {
	result, err := resolveVarRef(name, vars, make(map[string]bool))
	if err != nil {
		return ""
	}
	return result
}

// ============================================================================
// Theme JSON Schema
// ============================================================================

// ThemeJSON is the JSON representation of a theme file.
type ThemeJSON struct {
	Schema string            `json:"$schema,omitempty"`
	Name   string            `json:"name"`
	Vars   map[string]string `json:"vars,omitempty"`
	Colors map[string]string `json:"colors"`
	Export map[string]string `json:"export,omitempty"`
}

// ============================================================================
// Theme
// ============================================================================

// bgColorKeys is the set of color keys that are backgrounds.
var bgColorKeys = map[string]bool{
	"selectedBg":      true,
	"userMessageBg":   true,
	"customMessageBg": true,
	"toolPendingBg":   true,
	"toolSuccessBg":   true,
	"toolErrorBg":     true,
}

// Theme holds pre-computed ANSI escape codes for all theme colors.
type Theme struct {
	Name       string
	SourcePath string
	Mode       ColorMode

	fgColors map[ThemeColor]string // precomputed ANSI fg escapes
	bgColors map[ThemeBg]string    // precomputed ANSI bg escapes
}

// MutedTimestamp returns a muted, bracketed local-time prefix like
// "[15:04:05] " suitable for prefixing error output and tool cards.
func (t *Theme) MutedTimestamp(ts time.Time) string {
	return t.Fg("muted", "["+ts.Format("15:04:05")+"] ")
}

// Fg applies foreground color to text.
func (t *Theme) Fg(color ThemeColor, text string) string {
	ansi, ok := t.fgColors[color]
	if !ok {
		return text
	}
	return ansi + text + "\x1b[39m"
}

// Bg applies background color to text.
func (t *Theme) Bg(color ThemeBg, text string) string {
	ansi, ok := t.bgColors[color]
	if !ok {
		return text
	}
	return ansi + text + "\x1b[49m"
}

// Bold applies bold styling.
func (t *Theme) Bold(text string) string {
	return "\x1b[1m" + text + "\x1b[22m"
}

// Italic applies italic styling.
func (t *Theme) Italic(text string) string {
	return "\x1b[3m" + text + "\x1b[23m"
}

// Underline applies underline styling.
func (t *Theme) Underline(text string) string {
	return "\x1b[4m" + text + "\x1b[24m"
}

// Inverse applies inverse styling.
func (t *Theme) Inverse(text string) string {
	return "\x1b[7m" + text + "\x1b[27m"
}

// Strikethrough applies strikethrough styling.
func (t *Theme) Strikethrough(text string) string {
	return "\x1b[9m" + text + "\x1b[29m"
}

// GetFgAnsi returns the raw foreground ANSI escape for a color.
func (t *Theme) GetFgAnsi(color ThemeColor) string {
	return t.fgColors[color]
}

// GetBgAnsi returns the raw background ANSI escape for a color.
func (t *Theme) GetBgAnsi(color ThemeBg) string {
	return t.bgColors[color]
}

// GetThinkingBorderColor returns a styling function for the given thinking level.
func (t *Theme) GetThinkingBorderColor(level string) func(string) string {
	var color ThemeColor
	switch level {
	case "off":
		color = "thinkingOff"
	case "minimal":
		color = "thinkingMinimal"
	case "low":
		color = "thinkingLow"
	case "medium":
		color = "thinkingMedium"
	case "high":
		color = "thinkingHigh"
	case "xhigh":
		color = "thinkingXhigh"
	case "max":
		color = "thinkingMax"
	default:
		color = "thinkingOff"
	}
	return func(s string) string { return t.Fg(color, s) }
}

// GetBashModeBorderColor returns a styling function for bash mode.
func (t *Theme) GetBashModeBorderColor() func(string) string {
	return func(s string) string { return t.Fg("bashMode", s) }
}

// GetSelectListTheme returns a SelectListTheme from this theme.
func (t *Theme) GetSelectListTheme() components.SelectListTheme {
	return components.SelectListTheme{
		SelectedPrefix: func(s string) string { return t.Fg("accent", s) },
		SelectedText:   func(s string) string { return t.Fg("accent", s) },
		Description:    func(s string) string { return t.Fg("muted", s) },
		ScrollInfo:     func(s string) string { return t.Fg("muted", s) },
		NoMatch:        func(s string) string { return t.Fg("muted", s) },
	}
}

// GetEditorTheme returns an EditorTheme from this theme.
func (t *Theme) GetEditorTheme() components.EditorTheme {
	return components.EditorTheme{
		BorderColor: func(s string) string { return t.Fg("borderMuted", s) },
		SelectList:  t.GetSelectListTheme(),
	}
}

// GetMarkdownTheme returns a MarkdownTheme from this theme.
func (t *Theme) GetMarkdownTheme() components.MarkdownTheme {
	return components.MarkdownTheme{
		Heading:         func(s string) string { return t.Fg("mdHeading", s) },
		Link:            func(s string) string { return t.Fg("mdLink", s) },
		LinkURL:         func(s string) string { return t.Fg("mdLinkUrl", s) },
		Code:            func(s string) string { return t.Fg("mdCode", s) },
		CodeBlock:       func(s string) string { return t.Fg("mdCodeBlock", s) },
		CodeBlockBorder: func(s string) string { return t.Fg("mdCodeBlockBorder", s) },
		Quote:           func(s string) string { return t.Fg("mdQuote", s) },
		QuoteBorder:     func(s string) string { return t.Fg("mdQuoteBorder", s) },
		HR:              func(s string) string { return t.Fg("mdHr", s) },
		ListBullet:      func(s string) string { return t.Fg("mdListBullet", s) },
		Bold:            func(s string) string { return t.Bold(s) },
		Italic:          func(s string) string { return t.Italic(s) },
		Underline:       func(s string) string { return t.Underline(s) },
		Strikethrough:   func(s string) string { return t.Strikethrough(s) },
	}
}

// ============================================================================
// Theme Construction
// ============================================================================

// buildTheme creates a Theme from resolved hex color maps.
func buildTheme(fgHex map[ThemeColor]string, bgHex map[ThemeBg]string, mode ColorMode, name, sourcePath string) *Theme {
	t := &Theme{
		Name:       name,
		SourcePath: sourcePath,
		Mode:       mode,
		fgColors:   make(map[ThemeColor]string, len(fgHex)),
		bgColors:   make(map[ThemeBg]string, len(bgHex)),
	}
	for k, v := range fgHex {
		t.fgColors[k] = fgAnsi(v, mode)
	}
	for k, v := range bgHex {
		t.bgColors[k] = bgAnsi(v, mode)
	}
	return t
}

// CreateThemeFromJSON creates a Theme from a ThemeJSON.
func CreateThemeFromJSON(tj *ThemeJSON, mode ColorMode, sourcePath string) *Theme {
	fgHex := make(map[ThemeColor]string)
	bgHex := make(map[ThemeBg]string)

	for k, v := range tj.Colors {
		resolved := resolveColor(v, tj.Vars)
		if bgColorKeys[k] {
			bgHex[k] = resolved
		} else {
			fgHex[k] = resolved
		}
	}

	return buildTheme(fgHex, bgHex, mode, tj.Name, sourcePath)
}

// LoadThemeFromPath loads a theme from a JSON file.
func LoadThemeFromPath(path string, mode ColorMode) (*Theme, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tj ThemeJSON
	if err := json.Unmarshal(data, &tj); err != nil {
		return nil, fmt.Errorf("parse theme %s: %w", path, err)
	}
	return CreateThemeFromJSON(&tj, mode, path), nil
}

// ============================================================================
// Default Themes (embedded)
// ============================================================================

func createDarkTheme(mode ColorMode) *Theme {
	fg := map[ThemeColor]string{
		"accent": "#8abeb7", "border": "#5f87ff", "borderAccent": "#00d7ff",
		"borderMuted": "#505050", "success": "#b5bd68", "error": "#cc6666",
		"warning": "#ffff00", "muted": "#808080", "dim": "#666666",
		"text": "", "thinkingText": "#808080",
		"userMessageText": "", "customMessageText": "", "customMessageLabel": "#9575cd",
		"toolTitle": "", "toolOutput": "#b0b0b0",
		"mdHeading": "#f0c674", "mdLink": "#81a2be", "mdLinkUrl": "#666666",
		"mdCode": "#8abeb7", "mdCodeBlock": "#b5bd68", "mdCodeBlockBorder": "#808080",
		"mdQuote": "#808080", "mdQuoteBorder": "#808080", "mdHr": "#808080",
		"mdListBullet":  "#8abeb7",
		"toolDiffAdded": "#b5bd68", "toolDiffRemoved": "#cc6666", "toolDiffContext": "#808080",
		"syntaxComment": "#6A9955", "syntaxKeyword": "#569CD6", "syntaxFunction": "#DCDCAA",
		"syntaxVariable": "#9CDCFE", "syntaxString": "#CE9178", "syntaxNumber": "#B5CEA8",
		"syntaxType": "#4EC9B0", "syntaxOperator": "#D4D4D4", "syntaxPunctuation": "#D4D4D4",
		"thinkingOff": "#505050", "thinkingMinimal": "#6e6e6e", "thinkingLow": "#5f87af",
		"thinkingMedium": "#81a2be", "thinkingHigh": "#b294bb", "thinkingXhigh": "#d183e8", "thinkingMax": "#ff6ec7",
		"bashMode": "#b5bd68",
	}
	bg := map[ThemeBg]string{
		"selectedBg": "#3a3a4a", "userMessageBg": "#343541", "customMessageBg": "#2d2838",
		"toolPendingBg": "#282832", "toolSuccessBg": "#283228", "toolErrorBg": "#3c2828",
	}
	return buildTheme(fg, bg, mode, "dark", "")
}

func createLightTheme(mode ColorMode) *Theme {
	fg := map[ThemeColor]string{
		"accent": "#2aa198", "border": "#268bd2", "borderAccent": "#d33682",
		"borderMuted": "#b0b0b0", "success": "#859900", "error": "#dc322f",
		"warning": "#b58900", "muted": "#93a1a1", "dim": "#b0b0b0",
		"text": "", "thinkingText": "#93a1a1",
		"userMessageText": "", "customMessageText": "", "customMessageLabel": "#6c71c4",
		"toolTitle": "", "toolOutput": "#93a1a1",
		"mdHeading": "#b58900", "mdLink": "#268bd2", "mdLinkUrl": "#b0b0b0",
		"mdCode": "#2aa198", "mdCodeBlock": "#859900", "mdCodeBlockBorder": "#93a1a1",
		"mdQuote": "#93a1a1", "mdQuoteBorder": "#93a1a1", "mdHr": "#93a1a1",
		"mdListBullet":  "#2aa198",
		"toolDiffAdded": "#859900", "toolDiffRemoved": "#dc322f", "toolDiffContext": "#93a1a1",
		"syntaxComment": "#93a1a1", "syntaxKeyword": "#268bd2", "syntaxFunction": "#b58900",
		"syntaxVariable": "#657b83", "syntaxString": "#2aa198", "syntaxNumber": "#d33682",
		"syntaxType": "#859900", "syntaxOperator": "#657b83", "syntaxPunctuation": "#657b83",
		"thinkingOff": "#b0b0b0", "thinkingMinimal": "#93a1a1", "thinkingLow": "#268bd2",
		"thinkingMedium": "#6c71c4", "thinkingHigh": "#d33682", "thinkingXhigh": "#cb4b16", "thinkingMax": "#a30000",
		"bashMode": "#859900",
	}
	bg := map[ThemeBg]string{
		"selectedBg": "#eee8d5", "userMessageBg": "#fdf6e3", "customMessageBg": "#f5f0e8",
		"toolPendingBg": "#f0ece0", "toolSuccessBg": "#eef5e0", "toolErrorBg": "#fce8e6",
	}
	return buildTheme(fg, bg, mode, "light", "")
}

// ============================================================================
// Global Theme Instance
// ============================================================================

var (
	globalTheme   *Theme
	globalThemeMu sync.RWMutex
)

// GetTheme returns the current global theme. Lazily initializes to dark if nil.
func GetTheme() *Theme {
	globalThemeMu.RLock()
	t := globalTheme
	globalThemeMu.RUnlock()
	if t != nil {
		return t
	}
	globalThemeMu.Lock()
	defer globalThemeMu.Unlock()
	if globalTheme == nil {
		globalTheme = createDarkTheme(DetectColorMode())
	}
	return globalTheme
}

// SetThemeInstance sets the global theme to a specific instance.
func SetThemeInstance(t *Theme) {
	globalThemeMu.Lock()
	globalTheme = t
	globalThemeMu.Unlock()
}

// DetectTerminalBackground guesses whether the terminal has a dark or light background.
func DetectTerminalBackground() string {
	colorfgbg := os.Getenv("COLORFGBG")
	if colorfgbg != "" {
		parts := strings.Split(colorfgbg, ";")
		if len(parts) >= 2 {
			bg, err := strconv.Atoi(parts[1])
			if err == nil {
				if bg < 8 {
					return "dark"
				}
				return "light"
			}
		}
	}
	return "dark"
}

// loadEmbeddedTheme loads a bundled theme by name from the embedded FS.
// Returns nil if no such theme exists.
func loadEmbeddedTheme(name string, mode ColorMode) *Theme {
	data, err := embeddedThemesFS.ReadFile("themes/" + name + ".json")
	if err != nil {
		return nil
	}
	var tj ThemeJSON
	if err := json.Unmarshal(data, &tj); err != nil {
		return nil
	}
	return CreateThemeFromJSON(&tj, mode, "embedded:"+name)
}

// embeddedThemeNames returns the names of all bundled themes.
func embeddedThemeNames() []string {
	entries, err := fs.ReadDir(embeddedThemesFS, "themes")
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	return names
}

// InitTheme initializes the global theme by name.
// name: "dark", "light", a file path, or "" for auto-detect.
// searchDirs: additional directories to search for theme JSON files.
func InitTheme(name string, searchDirs []string) error {
	if name == "" {
		name = DetectTerminalBackground()
	}

	mode := DetectColorMode()

	switch name {
	case "dark":
		SetThemeInstance(createDarkTheme(mode))
		return nil
	case "light":
		SetThemeInstance(createLightTheme(mode))
		return nil
	}

	for _, dir := range searchDirs {
		path := filepath.Join(dir, name+".json")
		if _, err := os.Stat(path); err == nil {
			t, err := LoadThemeFromPath(path, mode)
			if err != nil {
				SetThemeInstance(createDarkTheme(mode))
				return fmt.Errorf("failed to load theme %s: %w", name, err)
			}
			SetThemeInstance(t)
			return nil
		}
	}

	if t := loadEmbeddedTheme(name, mode); t != nil {
		SetThemeInstance(t)
		return nil
	}

	if _, err := os.Stat(name); err == nil {
		t, err := LoadThemeFromPath(name, mode)
		if err != nil {
			SetThemeInstance(createDarkTheme(mode))
			return fmt.Errorf("failed to load theme %s: %w", name, err)
		}
		SetThemeInstance(t)
		return nil
	}

	SetThemeInstance(createDarkTheme(mode))
	return fmt.Errorf("theme not found: %s", name)
}

// GetAvailableThemes returns names of available themes.
func GetAvailableThemes(searchDirs []string) []string {
	themes := make(map[string]bool)
	for _, name := range embeddedThemeNames() {
		themes[name] = true
	}
	for _, dir := range searchDirs {
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
					themes[strings.TrimSuffix(e.Name(), ".json")] = true
				}
			}
		}
	}
	result := make([]string, 0, len(themes))
	for name := range themes {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

// ============================================================================
// Package-level TUI Theme Helpers (use global theme)
// ============================================================================

// GetSelectListTheme returns a SelectListTheme from the global theme.
func GetSelectListTheme() components.SelectListTheme {
	return GetTheme().GetSelectListTheme()
}

// GetEditorTheme returns an EditorTheme from the global theme.
func GetEditorTheme() components.EditorTheme {
	return GetTheme().GetEditorTheme()
}

// GetMarkdownTheme returns a MarkdownTheme from the global theme.
func GetMarkdownTheme() components.MarkdownTheme {
	return GetTheme().GetMarkdownTheme()
}

// ============================================================================
// Language Detection
// ============================================================================

var extToLang = map[string]string{
	"ts": "typescript", "tsx": "typescript",
	"js": "javascript", "jsx": "javascript", "mjs": "javascript", "cjs": "javascript",
	"py": "python", "rb": "ruby", "rs": "rust", "go": "go",
	"java": "java", "kt": "kotlin", "swift": "swift",
	"c": "c", "h": "c", "cpp": "cpp", "cc": "cpp", "cxx": "cpp", "hpp": "cpp",
	"cs": "csharp", "php": "php",
	"sh": "bash", "bash": "bash", "zsh": "bash", "fish": "fish",
	"ps1": "powershell", "sql": "sql",
	"html": "html", "htm": "html", "css": "css",
	"scss": "scss", "sass": "sass", "less": "less",
	"json": "json", "yaml": "yaml", "yml": "yaml", "toml": "toml",
	"xml": "xml", "md": "markdown", "markdown": "markdown",
	"dockerfile": "dockerfile", "makefile": "makefile", "cmake": "cmake",
	"lua": "lua", "perl": "perl", "r": "r", "scala": "scala",
	"clj": "clojure", "ex": "elixir", "exs": "elixir",
	"erl": "erlang", "hs": "haskell", "ml": "ocaml",
	"vim": "vim", "graphql": "graphql", "proto": "protobuf",
	"tf": "hcl", "hcl": "hcl",
}

// GetLanguageFromPath returns a language identifier from a file path extension.
func GetLanguageFromPath(filePath string) string {
	ext := filepath.Ext(filePath)
	if ext == "" {
		return ""
	}
	ext = strings.TrimPrefix(ext, ".")
	ext = strings.ToLower(ext)
	return extToLang[ext]
}
