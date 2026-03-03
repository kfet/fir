//go:build windows

package interactive

import (
	"os"
	"syscall"
)

func suspendSignal() os.Signal {
	// Windows doesn't have SIGTSTP; use SIGINT as a fallback.
	return syscall.SIGINT
}

func continueSignal() os.Signal {
	// Windows doesn't have SIGCONT; use SIGINT as a no-op placeholder.
	return syscall.SIGINT
}
