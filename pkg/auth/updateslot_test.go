package auth

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpdateSlot_ReadsUnderLockAndApplies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	a := NewAuthStorage(path)
	b := NewAuthStorage(path) // a second process on the same file
	require.NoError(t, b.Set("k", AuthCredential{Type: CredentialTypeOAuth, Access: "fresh"}))

	// a's in-memory view is stale; fn must still see b's write.
	var seen string
	require.NoError(t, a.UpdateSlot("k", func(cur *AuthCredential) (*AuthCredential, bool, error) {
		require.NotNil(t, cur)
		seen = cur.Access
		next := *cur
		next.Access = "rotated"
		return &next, true, nil
	}))
	require.Equal(t, "fresh", seen)
	require.Equal(t, "rotated", a.Get("k").Access)
	b.Reload()
	require.Equal(t, "rotated", b.Get("k").Access)

	// write=false leaves the row; an error aborts without writing.
	require.NoError(t, a.UpdateSlot("k", func(*AuthCredential) (*AuthCredential, bool, error) { return nil, false, nil }))
	require.Error(t, a.UpdateSlot("k", func(*AuthCredential) (*AuthCredential, bool, error) {
		return nil, true, errors.New("boom")
	}))
	require.True(t, a.Has("k"))

	// next=nil deletes.
	require.NoError(t, a.UpdateSlot("k", func(*AuthCredential) (*AuthCredential, bool, error) { return nil, true, nil }))
	b.Reload()
	require.False(t, b.Has("k"))
	// Deleting an absent row is a no-op.
	require.NoError(t, a.UpdateSlot("k", func(cur *AuthCredential) (*AuthCredential, bool, error) {
		require.Nil(t, cur)
		return nil, true, nil
	}))
}
