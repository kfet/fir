package session

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

// FormatAuthURL formats an auth URL for terminal display.
// For short URLs (like device flow), returns just the URL.
// For long URLs, returns instructions + URL on separate line.
func FormatAuthURL(url string) string {
	if len(url) < 60 {
		return url
	}
	return "Copy this URL and open in browser:\n" + url
}

// FormatAuthURLs formats an auth URL pair for terminal display: the short
// URL prominently, with the full URL on a separate line as a fallback.
// If shortURL is empty, falls back to FormatAuthURL for the full URL.
func FormatAuthURLs(fullURL, shortURL string) string {
	if shortURL == "" {
		return FormatAuthURL(fullURL)
	}
	return shortURL + "\n(if that doesn't work, use the full URL: " + fullURL + ")"
}

// PreferredAuthURL returns the URL most appropriate to open in the user's
// browser: the short one if available, otherwise the full one.
func PreferredAuthURL(fullURL, shortURL string) string {
	if shortURL != "" {
		return shortURL
	}
	return fullURL
}
