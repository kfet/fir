//go:build !windows

package interactive

import (
	"os"

	"golang.org/x/sys/unix"
)

// restoreStdinBlocking clears the O_NONBLOCK flag on stdin.
//
// Go's runtime sets stdin to non-blocking mode for its internal I/O
// poller.  Before syscall.Exec we must restore blocking mode so the
// replacement process (and, ultimately, the parent shell) inherits a
// clean file descriptor.
func restoreStdinBlocking() {
	_ = unix.SetNonblock(int(os.Stdin.Fd()), false)
}
