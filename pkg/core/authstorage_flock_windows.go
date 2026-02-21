// Ported from: packages/coding-agent/src/core/auth-storage.ts
// Upstream hash: 4ba3e5be

//go:build windows

package core

// flockExclusive is a no-op on Windows; the in-process sync.Mutex suffices
// for the CLI use-case.
func flockExclusive(_ int) error { return nil }

// flockUnlock is a no-op on Windows.
func flockUnlock(_ int) error { return nil }
