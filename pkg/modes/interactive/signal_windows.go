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
	// Windows doesn't have SIGCONT. Return a signal that will never fire
	// naturally in the suspend flow (suspend itself is a no-op on Windows).
	// Using SIGINT here is safe because handleCtrlZ never actually suspends
	// on Windows, so the SIGCONT listener goroutine is effectively dead code.
	return syscall.SIGINT
}
