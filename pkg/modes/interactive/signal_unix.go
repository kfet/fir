//go:build !windows

package interactive

import (
	"os"
	"syscall"
)

func suspendSignal() os.Signal {
	return syscall.SIGTSTP
}
