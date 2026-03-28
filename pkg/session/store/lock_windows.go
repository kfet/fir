//go:build windows

package store

// sessionLock is a no-op on Windows where flock is not available.
type sessionLock struct{}

func tryLockSession(_ string) (*sessionLock, bool) { return &sessionLock{}, true }

func (l *sessionLock) Close() error { return nil }
