// Package notify sends native terminal notifications when the agent finishes.
//
// Supports multiple terminal protocols:
//   - OSC 777: Ghostty, iTerm2, WezTerm, rxvt-unicode
//   - OSC 99: Kitty
//
// Import this package to enable the extension:
//
//	import _ "github.com/kfet/pi-go/pkg/extensions/notify"
package notify

import (
	"fmt"
	"os"

	"github.com/kfet/pi-go/pkg/extension"
)

func init() {
	extension.Register("notify", func(api extension.API) {
		api.On("agent_end", func(event *extension.Event, ctx extension.Context) (any, error) {
			notifyTerminal("Pi", "Ready for input")
			return nil, nil
		})
	})
}

// notifyTerminal sends a native terminal notification.
func notifyTerminal(title, body string) {
	if os.Getenv("KITTY_WINDOW_ID") != "" {
		notifyOSC99(title, body)
	} else {
		notifyOSC777(title, body)
	}
}

// notifyOSC777 sends an OSC 777 notification (Ghostty, iTerm2, WezTerm, rxvt-unicode).
// Writes to stderr to avoid interfering with the TUI's stdout rendering.
func notifyOSC777(title, body string) {
	fmt.Fprintf(os.Stderr, "\x1b]777;notify;%s;%s\x07", title, body)
}

// notifyOSC99 sends a Kitty OSC 99 notification.
// Writes to stderr to avoid interfering with the TUI's stdout rendering.
func notifyOSC99(title, body string) {
	fmt.Fprintf(os.Stderr, "\x1b]99;i=1:d=0;%s\x1b\\", title)
	fmt.Fprintf(os.Stderr, "\x1b]99;i=1:p=body;%s\x1b\\", body)
}
