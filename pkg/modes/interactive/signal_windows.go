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
