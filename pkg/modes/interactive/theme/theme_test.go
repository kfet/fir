package theme

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHexToRGB(t *testing.T) {
	r, g, b, err := hexToRGB("#ff0000")
	if err != nil {
		t.Fatal(err)
	}
	if r != 255 || g != 0 || b != 0 {
		t.Errorf("expected (255,0,0), got (%d,%d,%d)", r, g, b)
	}
}

func TestHexToRGB_NoHash(t *testing.T) {
	r, g, b, err := hexToRGB("00ff00")
	if err != nil {
		t.Fatal(err)
	}
	if r != 0 || g != 255 || b != 0 {
		t.Errorf("expected (0,255,0), got (%d,%d,%d)", r, g, b)
	}
}

func TestHexToRGB_Invalid(t *testing.T) {
	_, _, _, err := hexToRGB("#xyz")
	if err == nil {
		t.Error("expected error")
	}
}

func TestHexToRGB_Short(t *testing.T) {
	_, _, _, err := hexToRGB("#fff")
	if err == nil {
		t.Error("expected error")
	}
}

func TestRgbTo256_Pure(t *testing.T) {
	idx := rgbTo256(255, 0, 0)
	if idx != 196 {
		t.Errorf("expected 196, got %d", idx)
	}
}

func TestRgbTo256_Black(t *testing.T) {
	idx := rgbTo256(0, 0, 0)
	if idx != 16 {
		t.Errorf("expected 16, got %d", idx)
	}
}

func TestRgbTo256_Gray(t *testing.T) {
	idx := rgbTo256(128, 128, 128)
	if idx < 232 || idx > 255 {
		t.Errorf("expected grayscale (232-255), got %d", idx)
	}
}

func TestRgbTo256_NearGrayWithTint(t *testing.T) {
	// Dark colors with slight tints (e.g. #282832) should map to grayscale,
	// not saturated cube colors like #00005f (index 17).
	tests := []struct {
		name    string
		r, g, b int
	}{
		{"toolPendingBg #282832", 0x28, 0x28, 0x32},
		{"customMessageBg #2d2838", 0x2d, 0x28, 0x38},
		{"userMessageBg #343541", 0x34, 0x35, 0x41},
		{"selectedBg #3a3a4a", 0x3a, 0x3a, 0x4a},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx := rgbTo256(tt.r, tt.g, tt.b)
			if idx < 232 || idx > 255 {
				t.Errorf("expected grayscale (232-255), got %d", idx)
			}
		})
	}
}

func TestDetectColorMode(t *testing.T) {
	mode := DetectColorMode()
	if mode != ColorModeTruecolor && mode != ColorMode256 {
		t.Errorf("unexpected mode: %s", mode)
	}
}

func TestFgAnsi_Hex(t *testing.T) {
	ansi := fgAnsi("#ff0000", ColorModeTruecolor)
	if !strings.Contains(ansi, "38;2;255;0;0") {
		t.Errorf("expected truecolor fg, got %q", ansi)
	}
}

func TestFgAnsi_256(t *testing.T) {
	ansi := fgAnsi("#ff0000", ColorMode256)
	if !strings.Contains(ansi, "38;5;") {
		t.Errorf("expected 256-color fg, got %q", ansi)
	}
}

func TestFgAnsi_Empty(t *testing.T) {
	if fgAnsi("", ColorModeTruecolor) != "\x1b[39m" {
		t.Error("expected fg reset")
	}
}

func TestBgAnsi_Hex(t *testing.T) {
	ansi := bgAnsi("#0000ff", ColorModeTruecolor)
	if !strings.Contains(ansi, "48;2;0;0;255") {
		t.Errorf("expected truecolor bg, got %q", ansi)
	}
}

func TestBgAnsi_Empty(t *testing.T) {
	if bgAnsi("", ColorModeTruecolor) != "\x1b[49m" {
		t.Error("expected bg reset")
	}
}

func TestGetTheme_LazyInit(t *testing.T) {
	// Save and clear under the mutex to avoid data race with concurrent GetTheme callers.
	globalThemeMu.Lock()
	old := globalTheme
	globalTheme = nil
	globalThemeMu.Unlock()
	defer SetThemeInstance(old)

	if GetTheme() == nil {
		t.Fatal("GetTheme returned nil")
	}
}

func TestTheme_Fg(t *testing.T) {
	th := createDarkTheme(ColorModeTruecolor)
	result := th.Fg("accent", "hello")
	if !strings.Contains(result, "hello") || !strings.Contains(result, "\x1b[") {
		t.Errorf("expected colored text, got %q", result)
	}
}

func TestTheme_Bg(t *testing.T) {
	th := createDarkTheme(ColorModeTruecolor)
	result := th.Bg("selectedBg", "hello")
	if !strings.Contains(result, "hello") || !strings.Contains(result, "\x1b[") {
		t.Errorf("expected bg-colored text, got %q", result)
	}
}

func TestTheme_Bold(t *testing.T) {
	th := createDarkTheme(ColorModeTruecolor)
	if !strings.Contains(th.Bold("x"), "\x1b[1m") {
		t.Error("expected bold")
	}
}

func TestTheme_Italic(t *testing.T) {
	th := createDarkTheme(ColorModeTruecolor)
	if !strings.Contains(th.Italic("x"), "\x1b[3m") {
		t.Error("expected italic")
	}
}

func TestTheme_Strikethrough(t *testing.T) {
	th := createDarkTheme(ColorModeTruecolor)
	if !strings.Contains(th.Strikethrough("x"), "\x1b[9m") {
		t.Error("expected strikethrough")
	}
}

func TestTheme_GetThinkingBorderColor(t *testing.T) {
	th := createDarkTheme(ColorModeTruecolor)
	for _, level := range []string{"off", "minimal", "low", "medium", "high", "xhigh"} {
		fn := th.GetThinkingBorderColor(level)
		if !strings.Contains(fn("─"), "\x1b[") {
			t.Errorf("expected ANSI for level %q", level)
		}
	}
}

func TestLoadThemeFromPath(t *testing.T) {
	dir := t.TempDir()
	themeData := map[string]interface{}{
		"name": "test-theme",
		"colors": map[string]interface{}{
			"accent": "#ff0000", "border": "#00ff00", "borderAccent": "#0000ff",
			"borderMuted": "#505050", "success": "#00ff00", "error": "#ff0000",
			"warning": "#ffff00", "muted": "#808080", "dim": "#666666",
			"text": "", "thinkingText": "#808080",
			"selectedBg": "#3a3a4a", "userMessageBg": "#343541",
			"userMessageText": "", "customMessageBg": "#2d2838",
			"customMessageText": "", "customMessageLabel": "#9575cd",
			"toolPendingBg": "#282832", "toolSuccessBg": "#283228",
			"toolErrorBg": "#3c2828", "toolTitle": "", "toolOutput": "#b0b0b0",
			"mdHeading": "#f0c674", "mdLink": "#81a2be", "mdLinkUrl": "#666666",
			"mdCode": "#8abeb7", "mdCodeBlock": "#b5bd68", "mdCodeBlockBorder": "#808080",
			"mdQuote": "#808080", "mdQuoteBorder": "#808080", "mdHr": "#808080",
			"mdListBullet": "#8abeb7",
			"toolDiffAdded": "#00ff00", "toolDiffRemoved": "#ff0000", "toolDiffContext": "#808080",
			"syntaxComment": "#6A9955", "syntaxKeyword": "#569CD6",
			"syntaxFunction": "#DCDCAA", "syntaxVariable": "#9CDCFE",
			"syntaxString": "#CE9178", "syntaxNumber": "#B5CEA8",
			"syntaxType": "#4EC9B0", "syntaxOperator": "#D4D4D4",
			"syntaxPunctuation": "#D4D4D4",
			"thinkingOff": "#505050", "thinkingMinimal": "#6e6e6e",
			"thinkingLow": "#5f87af", "thinkingMedium": "#81a2be",
			"thinkingHigh": "#b294bb", "thinkingXhigh": "#d183e8",
			"bashMode": "#b5bd68",
		},
	}
	data, _ := json.Marshal(themeData)
	p := filepath.Join(dir, "test.json")
	os.WriteFile(p, data, 0644)

	th, err := LoadThemeFromPath(p, ColorModeTruecolor)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if th.Name != "test-theme" {
		t.Errorf("expected 'test-theme', got %q", th.Name)
	}
}

func TestLoadThemeFromPath_NotFound(t *testing.T) {
	_, err := LoadThemeFromPath("/nonexistent", ColorModeTruecolor)
	if err == nil {
		t.Error("expected error")
	}
}

func TestGetLanguageFromPath(t *testing.T) {
	tests := []struct{ path, want string }{
		{"main.go", "go"}, {"app.ts", "typescript"}, {"x.py", "python"}, {"x.xyz", ""},
	}
	for _, tc := range tests {
		if got := GetLanguageFromPath(tc.path); got != tc.want {
			t.Errorf("GetLanguageFromPath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestFindClosestCubeIndex(t *testing.T) {
	if findClosestCubeIndex(0) != 0 {
		t.Error("expected 0")
	}
	if findClosestCubeIndex(255) != 5 {
		t.Error("expected 5")
	}
}

func TestColorDistance(t *testing.T) {
	if colorDistance(0, 0, 0, 0, 0, 0) != 0 {
		t.Error("expected 0")
	}
	if colorDistance(0, 0, 0, 255, 255, 255) <= 0 {
		t.Error("expected positive")
	}
}

func TestEmbeddedThemeNames(t *testing.T) {
	names := embeddedThemeNames()
	if len(names) == 0 {
		t.Fatal("expected embedded theme names, got none")
	}
	want := []string{"dark", "light", "dracula", "gruvbox", "catppuccin-mocha", "nord"}
	byName := make(map[string]bool, len(names))
	for _, n := range names {
		byName[n] = true
	}
	for _, w := range want {
		if !byName[w] {
			t.Errorf("embedded theme %q not found; got %v", w, names)
		}
	}
}

func TestLoadEmbeddedTheme_Valid(t *testing.T) {
	th := loadEmbeddedTheme("dark", ColorModeTruecolor)
	if th == nil {
		t.Fatal("expected non-nil theme for 'dark'")
	}
	if th.Name != "dark" {
		t.Errorf("expected name 'dark', got %q", th.Name)
	}
	if th.SourcePath != "embedded:dark" {
		t.Errorf("expected source path 'embedded:dark', got %q", th.SourcePath)
	}
}

func TestLoadEmbeddedTheme_Invalid(t *testing.T) {
	th := loadEmbeddedTheme("nonexistent-theme-xyz", ColorModeTruecolor)
	if th != nil {
		t.Error("expected nil for unknown embedded theme")
	}
}

func TestGetAvailableThemes_IncludesEmbedded(t *testing.T) {
	themes := GetAvailableThemes(nil)
	if len(themes) == 0 {
		t.Fatal("expected at least one available theme")
	}
	byName := make(map[string]bool, len(themes))
	for _, n := range themes {
		byName[n] = true
	}
	for _, w := range []string{"dark", "light", "dracula"} {
		if !byName[w] {
			t.Errorf("GetAvailableThemes missing embedded theme %q; got %v", w, themes)
		}
	}
	// Result must be sorted.
	for i := 1; i < len(themes); i++ {
		if themes[i] < themes[i-1] {
			t.Errorf("themes not sorted: %v", themes)
			break
		}
	}
}

func TestInitTheme_EmbeddedTheme(t *testing.T) {
	// Save and restore global theme around this test.
	globalThemeMu.Lock()
	old := globalTheme
	globalThemeMu.Unlock()
	defer SetThemeInstance(old)

	if err := InitTheme("catppuccin-mocha", nil); err != nil {
		t.Fatalf("InitTheme('catppuccin-mocha') error: %v", err)
	}
	th := GetTheme()
	if th == nil {
		t.Fatal("GetTheme returned nil after InitTheme")
	}
	if th.Name != "catppuccin-mocha" {
		t.Errorf("expected theme name 'catppuccin-mocha', got %q", th.Name)
	}
}
