//go:build !windows

package store

import (
	"os"
	"syscall"
)

// sessionLock holds an flock on a session's .meta.json file.
// The lock is released when Close is called or the process exits/crashes.
type sessionLock struct {
	f *os.File
}

// tryLockSession attempts a non-blocking exclusive flock on the session's
// .meta.json file. Returns (lock, true) on success, (nil, false) if already
// locked by another process.
func tryLockSession(sessionPath string) (*sessionLock, bool) {
	mp := metaPath(sessionPath)
	f, err := os.OpenFile(mp, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, false
	}
	return &sessionLock{f: f}, true
}

// Close releases the flock and closes the file descriptor.
func (l *sessionLock) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	return l.f.Close()
}
