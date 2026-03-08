package core

import (
	"os"
	"testing"
)

func TestIsWaylandSession(t *testing.T) {
	// Save original env
	origWayland := os.Getenv("WAYLAND_DISPLAY")
	origXDG := os.Getenv("XDG_SESSION_TYPE")
	defer func() {
		os.Setenv("WAYLAND_DISPLAY", origWayland)
		os.Setenv("XDG_SESSION_TYPE", origXDG)
	}()

	// No Wayland
	os.Setenv("WAYLAND_DISPLAY", "")
	os.Setenv("XDG_SESSION_TYPE", "x11")
	if isWaylandSession() {
		t.Error("expected false for X11 session")
	}

	// WAYLAND_DISPLAY set
	os.Setenv("WAYLAND_DISPLAY", "wayland-0")
	os.Setenv("XDG_SESSION_TYPE", "")
	if !isWaylandSession() {
		t.Error("expected true when WAYLAND_DISPLAY is set")
	}

	// XDG_SESSION_TYPE = wayland
	os.Setenv("WAYLAND_DISPLAY", "")
	os.Setenv("XDG_SESSION_TYPE", "wayland")
	if !isWaylandSession() {
		t.Error("expected true when XDG_SESSION_TYPE is wayland")
	}
}

func TestCopyViaCommand_InvalidCommand(t *testing.T) {
	err := copyViaCommand("nonexistent-command-xyz", "test")
	if err == nil {
		t.Error("expected error for nonexistent command")
	}
}
