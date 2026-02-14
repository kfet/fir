// Ported from: packages/tui/src/terminal.ts (enableWindowsVTInput)
// Upstream hash: 9e22d391
//go:build !windows

package tui

// enableWindowsVTInput is a no-op on non-Windows platforms.
func enableWindowsVTInput() {}
