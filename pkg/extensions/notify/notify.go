// Package notify sends native terminal notifications when the agent finishes.
//
// Supports multiple terminal protocols:
//   - OSC 777: Ghostty, iTerm2, WezTerm, rxvt-unicode
//   - OSC 99: Kitty
//
// When inside tmux, wraps sequences in DCS passthrough format.
//
// Import this package to enable the extension:
//
//	import _ "github.com/kfet/fir/pkg/extensions/notify"
package notify

import (
	"fmt"
	"io"
	"os"

	"github.com/kfet/fir/pkg/extension"
)

// output is the writer for notification escape sequences.
// Defaults to stderr to avoid interfering with the TUI's stdout rendering.
var output io.Writer = os.Stderr

// inTmux reports whether the process is running inside tmux.
var inTmux = func() bool {
	return os.Getenv("TMUX") != ""
}

// inKitty reports whether the process is running inside Kitty.
var inKitty = func() bool {
	return os.Getenv("KITTY_WINDOW_ID") != ""
}

func init() {
	extension.Register("notify", func(api extension.API) {
		api.On("agent_end", func(event *extension.Event, ctx extension.Context) (any, error) {
			notifyTerminal("Fi", "Ready for input")
			return nil, nil
		})
	})
}

// notifyTerminal sends a native terminal notification.
func notifyTerminal(title, body string) {
	if inKitty() {
		notifyOSC99(title, body)
	} else {
		notifyOSC777(title, body)
	}
}

// notifyOSC777 sends an OSC 777 notification (Ghostty, iTerm2, WezTerm, rxvt-unicode).
// When inside tmux, wraps the sequence in DCS passthrough format.
func notifyOSC777(title, body string) {
	if inTmux() {
		// DCS passthrough: wrap for tmux (every ESC becomes ESC ESC)
		// Format: ESC P tmux; ESC ESC ] 777;notify;title;body ESC ESC \ ESC \.
		fmt.Fprintf(output, "\x1bPtmux;\x1b\x1b]777;notify;%s;%s\x1b\x1b\\\x1b\\", title, body)
	} else {
		fmt.Fprintf(output, "\x1b]777;notify;%s;%s\x1b\\", title, body)
	}
}

// notifyOSC99 sends a Kitty OSC 99 notification.
// When inside tmux, wraps the sequences in DCS passthrough format.
func notifyOSC99(title, body string) {
	if inTmux() {
		fmt.Fprintf(output, "\x1bPtmux;\x1b\x1b]99;i=1:d=0;%s\x1b\x1b\\\x1b\\", title)
		fmt.Fprintf(output, "\x1bPtmux;\x1b\x1b]99;i=1:p=body;%s\x1b\x1b\\\x1b\\", body)
	} else {
		fmt.Fprintf(output, "\x1b]99;i=1:d=0;%s\x1b\\", title)
		fmt.Fprintf(output, "\x1b]99;i=1:p=body;%s\x1b\\", body)
	}
}
