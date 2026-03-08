package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTracker_RecordAndGet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	tr := New(path)

	tr.Record(EventSlashCommand, "/help")
	tr.Record(EventSlashCommand, "/help")
	tr.Record(EventSlashCommand, "/model")
	tr.Record(EventCLIFlag, "--print")

	c := tr.Get(EventSlashCommand, "/help")
	require.NotNil(t, c)
	assert.Equal(t, int64(2), c.Count)

	c = tr.Get(EventSlashCommand, "/model")
	require.NotNil(t, c)
	assert.Equal(t, int64(1), c.Count)

	c = tr.Get(EventCLIFlag, "--print")
	require.NotNil(t, c)
	assert.Equal(t, int64(1), c.Count)

	assert.Nil(t, tr.Get(EventSlashCommand, "/nonexistent"))
}

func TestTracker_Persistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")

	tr1 := New(path)
	tr1.Record(EventToolUse, "bash")
	tr1.Record(EventToolUse, "bash")

	// New tracker reading the same file.
	tr2 := New(path)
	c := tr2.Get(EventToolUse, "bash")
	require.NotNil(t, c)
	assert.Equal(t, int64(2), c.Count)

	// Continue incrementing.
	tr2.Record(EventToolUse, "bash")
	c = tr2.Get(EventToolUse, "bash")
	assert.Equal(t, int64(3), c.Count)
}

func TestTracker_MissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent", "usage.json")
	tr := New(path)

	// Get on missing file returns nil, no panic.
	assert.Nil(t, tr.Get(EventSlashCommand, "/help"))

	// Record creates the file.
	tr.Record(EventSlashCommand, "/help")
	_, err := os.Stat(path)
	assert.NoError(t, err)
}

func TestTracker_CorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	require.NoError(t, os.WriteFile(path, []byte("{bad json"), 0o644))

	tr := New(path)
	// Should not panic, just start fresh.
	tr.Record(EventSlashCommand, "/help")
	c := tr.Get(EventSlashCommand, "/help")
	require.NotNil(t, c)
	assert.Equal(t, int64(1), c.Count)
}

func TestTracker_All(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	tr := New(path)

	tr.Record(EventSlashCommand, "/help")
	tr.Record(EventMode, "rpc")

	all := tr.All()
	assert.Len(t, all.Events, 2)
	assert.Equal(t, int64(1), all.Events[EventSlashCommand]["/help"].Count)
	assert.Equal(t, int64(1), all.Events[EventMode]["rpc"].Count)

	// Mutating the copy shouldn't affect the tracker.
	all.Events[EventSlashCommand]["/help"].Count = 999
	c := tr.Get(EventSlashCommand, "/help")
	assert.Equal(t, int64(1), c.Count)
}

func TestDefaultPath(t *testing.T) {
	p := DefaultPath("/home/user/.fir/agent")
	assert.Equal(t, "/home/user/.fir/agent/usage.json", p)
}
