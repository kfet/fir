package core

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestOpenBrowser_CommandExists(t *testing.T) {
	// Verify the browser command for the current platform exists in PATH.
	var name string
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "windows":
		name = "cmd"
	default:
		name = "xdg-open"
	}
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("browser command %q not in PATH: %v", name, err)
	}

	// Call with a URL that won't actually open a browser window.
	// "open" on macOS with a non-existent file returns quickly with no visible effect.
	// We just test that Start() doesn't fail.
	err := OpenBrowser("about:blank")
	if err != nil {
		t.Errorf("OpenBrowser(about:blank) failed: %v", err)
	}
}

func TestHyperlink(t *testing.T) {
	link := Hyperlink("https://example.com", "click here")
	if !strings.Contains(link, "\x1b]8;;https://example.com\x07") {
		t.Errorf("missing OSC 8 open: %q", link)
	}
	if !strings.Contains(link, "click here") {
		t.Errorf("missing visible text: %q", link)
	}
	if !strings.HasSuffix(link, "\x1b]8;;\x07") {
		t.Errorf("missing OSC 8 close: %q", link)
	}
}
