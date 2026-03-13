package ptydriver

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManagerLifecycle(t *testing.T) {
	m := NewManager()

	// Create session.
	s, err := m.New("test", "shell")
	require.NoError(t, err)
	assert.Equal(t, "test:shell", s.Name)

	// List sessions.
	sessions := m.List("")
	assert.Contains(t, sessions, "test")

	// List windows.
	windows := m.List("test")
	assert.Equal(t, []string{"shell"}, windows)

	// Alive.
	assert.True(t, m.Alive("test:shell"))

	// Kill.
	require.NoError(t, m.Kill("test"))
	assert.Empty(t, m.List(""))
}

func TestManagerSendAndCapture(t *testing.T) {
	m := NewManager()
	_, err := m.New("test", "shell")
	require.NoError(t, err)
	defer m.Kill("test")

	// Send an echo command.
	err = m.Send("test:shell", "echo HELLO_PTY_DRIVER")
	require.NoError(t, err)

	// Wait for the output.
	err = m.Wait("test:shell", "HELLO_PTY_DRIVER", 5*time.Second)
	require.NoError(t, err)

	// Capture should contain it.
	out, err := m.Capture("test:shell", 50)
	require.NoError(t, err)
	assert.Contains(t, out, "HELLO_PTY_DRIVER")
}

func TestManagerWaitTimeout(t *testing.T) {
	m := NewManager()
	_, err := m.New("test", "shell")
	require.NoError(t, err)
	defer m.Kill("test")

	err = m.Wait("test:shell", "THIS_WILL_NEVER_APPEAR", 500*time.Millisecond)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "timeout")
}

func TestManagerMultipleWindows(t *testing.T) {
	m := NewManager()
	_, err := m.New("test", "w1")
	require.NoError(t, err)
	defer m.Kill("test")

	_, err = m.NewWindow("test", "w2", "")
	require.NoError(t, err)

	windows := m.List("test")
	assert.Len(t, windows, 2)
	assert.Contains(t, windows, "w1")
	assert.Contains(t, windows, "w2")

	// Kill one window.
	require.NoError(t, m.KillWindow("test", "w1"))
	windows = m.List("test")
	assert.Equal(t, []string{"w2"}, windows)
}

func TestManagerDuplicateWindow(t *testing.T) {
	m := NewManager()
	_, err := m.New("test", "shell")
	require.NoError(t, err)
	defer m.Kill("test")

	_, err = m.NewWindow("test", "shell", "")
	assert.Error(t, err)
}

func TestManagerCommandWindow(t *testing.T) {
	m := NewManager()
	_, err := m.NewWindow("test", "echo", "echo DONE && exit")
	require.NoError(t, err)
	defer m.Kill("test")

	err = m.Wait("test:echo", "DONE", 5*time.Second)
	require.NoError(t, err)
}

func TestManagerGetBySessionName(t *testing.T) {
	m := NewManager()
	_, err := m.New("test", "w1")
	require.NoError(t, err)
	defer m.Kill("test")

	// Send using just session name (should target first window).
	err = m.Send("test", "echo RESOLVED_OK")
	require.NoError(t, err)

	err = m.Wait("test", "RESOLVED_OK", 5*time.Second)
	require.NoError(t, err)
}
