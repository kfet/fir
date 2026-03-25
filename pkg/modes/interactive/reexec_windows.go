//go:build windows

package interactive

// restoreStdinBlocking is a no-op on Windows.
// Windows does not use O_NONBLOCK on console handles, and syscall.Exec
// is not supported on Windows anyway.
func restoreStdinBlocking() {}
