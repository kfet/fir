//go:build !windows

package reexec

import (
	"os"

	"golang.org/x/sys/unix"
)

// restoreStdinBlocking clears the O_NONBLOCK flag on stdin before exec.
func restoreStdinBlocking() {
	_ = unix.SetNonblock(int(os.Stdin.Fd()), false)
}
