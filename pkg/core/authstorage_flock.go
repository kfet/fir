// Ported from: packages/coding-agent/src/core/auth-storage.ts
// Upstream hash: 4ba3e5be

//go:build !windows

package core

import "syscall"

// flockExclusive acquires an exclusive advisory lock on the given file
// descriptor, blocking until the lock is available.
func flockExclusive(fd int) error {
	return syscall.Flock(fd, syscall.LOCK_EX)
}

// flockUnlock releases an advisory lock on the given file descriptor.
func flockUnlock(fd int) error {
	return syscall.Flock(fd, syscall.LOCK_UN)
}
