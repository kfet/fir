// Ported from: packages/tui/src/terminal.ts (enableWindowsVTInput)
// Upstream hash: 9e22d391
//go:build windows

package tui

import (
	"syscall"
	"unsafe"
)

const enableVirtualTerminalInput = 0x0200

var (
	kernel32              = syscall.NewLazyDLL("kernel32.dll")
	procGetStdHandle      = kernel32.NewProc("GetStdHandle")
	procGetConsoleMode    = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode    = kernel32.NewProc("SetConsoleMode")
)

// enableWindowsVTInput adds ENABLE_VIRTUAL_TERMINAL_INPUT (0x0200) to the
// stdin console handle so the terminal sends VT sequences for modified keys
// (e.g. \x1b[Z for Shift+Tab). Without this, ReadConsoleInputW discards
// modifier state and Shift+Tab arrives as plain \t.
func enableWindowsVTInput() {
	const stdInputHandle = ^uintptr(0) - 10 + 1 // STD_INPUT_HANDLE = -10
	handle, _, _ := procGetStdHandle.Call(stdInputHandle)
	if handle == 0 || handle == ^uintptr(0) {
		return
	}
	var mode uint32
	ret, _, _ := procGetConsoleMode.Call(handle, uintptr(unsafe.Pointer(&mode)))
	if ret == 0 {
		return
	}
	procSetConsoleMode.Call(handle, uintptr(mode|enableVirtualTerminalInput))
}
