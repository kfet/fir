// Ported from: packages/coding-agent/src/utils/clipboard.ts
// Upstream hash: 1caadb2e
package platform

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// CopyToClipboard copies text to the system clipboard.
// It always emits OSC 52 (works over SSH/mosh) and also tries native clipboard tools.
func CopyToClipboard(text string) {
	// Always emit OSC 52 - works over SSH/mosh, harmless locally
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	fmt.Fprintf(os.Stdout, "\x1b]52;c;%s\x07", encoded)

	// Also try native tools (best effort for local sessions)
	switch runtime.GOOS {
	case "darwin":
		copyViaCommand("pbcopy", text)
	case "windows":
		copyViaCommand("clip", text)
	default:
		// Linux: try Termux, Wayland, or X11
		if os.Getenv("TERMUX_VERSION") != "" {
			if copyViaCommand("termux-clipboard-set", text) == nil {
				return
			}
		}

		if isWaylandSession() {
			if _, err := exec.LookPath("wl-copy"); err == nil {
				cmd := exec.Command("wl-copy")
				cmd.Stdin = strings.NewReader(text)
				_ = cmd.Start()
				// Don't wait - wl-copy has fork behavior issues
				return
			}
			// Fall back to xclip/xsel (works on XWayland)
		}

		if copyViaCommand("xclip", text, "-selection", "clipboard") != nil {
			_ = copyViaCommand("xsel", text, "--clipboard", "--input")
		}
	}
}

// copyViaCommand pipes text to a command's stdin.
func copyViaCommand(name string, text string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

// isWaylandSession returns true if the current session is Wayland.
func isWaylandSession() bool {
	return os.Getenv("WAYLAND_DISPLAY") != "" ||
		strings.Contains(os.Getenv("XDG_SESSION_TYPE"), "wayland")
}
