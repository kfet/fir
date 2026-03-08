package platform

import (
	"fmt"
	"os/exec"
	"runtime"
)

// OpenBrowser opens a URL in the user's default browser.
// Returns an error if the platform is unsupported or the command fails to start.
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		// Linux / FreeBSD / etc.
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("opening browser: %w", err)
	}
	// Don't wait — the browser is a separate process.
	go cmd.Wait()
	return nil
}

// Hyperlink wraps text in an OSC 8 terminal hyperlink escape sequence.
// Terminals that support OSC 8 render it as a clickable link;
// others silently ignore the escape and show only the visible text.
func Hyperlink(url, text string) string {
	return fmt.Sprintf("\x1b]8;;%s\x07%s\x1b]8;;\x07", url, text)
}
