package store

import (
	"os"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
)

func TestSessionStore_ObservablesAlwaysNonNil(t *testing.T) {
	// In-memory store: still has an Observables() store; writes touch nothing on disk.
	s := InMemorySessionStore(t.TempDir())
	if s.Observables() == nil {
		t.Fatal("in-memory SessionStore.Observables() must be non-nil")
	}

	// Persistent store: writes land at <sessionFile>.cards.
	dir := t.TempDir()
	ss := NewSessionStore("", dir)
	defer ss.Close()
	ss.Observables().Put("src", "k", "slug", "", "")
	want := CardsPath(ss.GetSessionFile())
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected cards file at %s after Put: %v", want, err)
	}
}

func TestSessionStore_ObservablesPersistAndReload(t *testing.T) {
	dir := t.TempDir()
	sm1 := NewSessionStore("", dir)
	sm1.Observables().Put("plan", "active", "1/2", "step one", "tc-1")
	sm1.Observables().Put("mood", "current", "engaged", "fresh", "tc-2")
	sessionFile := sm1.GetSessionFile()
	sm1.Close()

	// The on-disk cards file must exist now.
	if _, err := os.Stat(CardsPath(sessionFile)); err != nil {
		t.Fatalf("cards file not written: %v", err)
	}

	// Re-open the same session — observables should be hydrated from disk
	// before any producer runs. This is the /reexec story.
	sm2, _ := OpenSessionStore(sessionFile, dir)
	defer sm2.Close()

	got := sm2.Observables().List()
	if len(got) != 2 {
		t.Fatalf("expected 2 cards on resume, got %d", len(got))
	}
}

func TestSessionStore_ObservablesRebindOnSetSessionFile(t *testing.T) {
	dir := t.TempDir()
	sm := NewSessionStore("", dir)
	defer sm.Close()

	// Write a card under session A.
	sm.Observables().Put("plan", "active", "A", "session A card", "e1")
	fileA := sm.GetSessionFile()

	// Create a separate session B with its own cards on disk via a
	// second store, close it, then SetSessionFile to it. The first
	// store's observables should rebind to B's cards file and show
	// B's cards (not A's).
	smB := NewSessionStore("", dir)
	smB.Observables().Put("plan", "active", "B", "session B card", "e2")
	fileB := smB.GetSessionFile()
	smB.Close()

	if forked := sm.SetSessionFile(fileB); forked {
		t.Fatalf("did not expect fork (B was closed); sm now on %s", sm.GetSessionFile())
	}
	// On resume, sm's observables should reflect B's cards.
	got := sm.Observables().List()
	if len(got) != 1 {
		t.Fatalf("expected 1 card after SetSessionFile, got %d: %#v", len(got), got)
	}
	if got[0].Slug != "B" {
		t.Errorf("expected B card after SetSessionFile, got slug=%q", got[0].Slug)
	}

	// Sanity: A's cards file untouched.
	disk := loadCardsRaw(t, CardsPath(fileA))
	if len(disk) != 1 || disk[0].Slug != "A" {
		t.Errorf("expected A's cards untouched: %#v", disk)
	}
}

func TestSessionStore_ObservablesEntryIDFromTranscriptID(t *testing.T) {
	// Producers in core stamp entry_id from a transcript entry they
	// just appended. This test just verifies the round-trip works.
	dir := t.TempDir()
	sm := NewSessionStore("", dir)
	defer sm.Close()

	id := sm.AppendAIMessage(ai.NewUserMsg("hello", time.Now().UnixMilli()))
	sm.Observables().Put("model", "active", "claude", "test detail", id)

	got := sm.Observables().List()
	if len(got) != 1 {
		t.Fatalf("expected 1 card, got %d", len(got))
	}
	if got[0].EntryID != id {
		t.Errorf("entry_id round-trip failed: got %q want %q", got[0].EntryID, id)
	}
}

func TestSessionStore_BranchGetsFreshObservables(t *testing.T) {
	dir := t.TempDir()
	sm := NewSessionStore("", dir)
	defer sm.Close()

	// Build a branchable tree.
	id1 := sm.AppendAIMessage(ai.NewUserMsg("hi", time.Now().UnixMilli()))
	sm.AppendAIMessage(ai.NewUserMsg("there", time.Now().UnixMilli()))

	// Card on the source session.
	sm.Observables().Put("plan", "active", "1/2", "first", "e1")
	srcPath := sm.GetSessionFile()
	srcCardsPath := CardsPath(srcPath)

	// Create a branched session file — sm.sessionFile rotates.
	newPath, err := sm.CreateBranchedSession(id1)
	if err != nil {
		t.Fatalf("CreateBranchedSession: %v", err)
	}
	if newPath == "" || newPath == srcPath {
		t.Fatalf("Branch did not rotate sessionFile: src=%q new=%q", srcPath, newPath)
	}

	// Observables should now be empty on the branched session.
	if got := sm.Observables().List(); len(got) != 0 {
		t.Errorf("branched session observables should start empty, got %#v", got)
	}
	// A Put on the branched store lands at the new session's cards file,
	// not the original.
	sm.Observables().Put("src", "k", "slug", "", "")
	if _, err := os.Stat(CardsPath(newPath)); err != nil {
		t.Errorf("expected cards file at branched path %s: %v", CardsPath(newPath), err)
	}
	// Original cards untouched.
	if _, err := os.Stat(srcCardsPath); err != nil {
		t.Errorf("original cards file vanished: %v", err)
	}
}

func TestSessionStore_ObservablesInMemoryNoFile(t *testing.T) {
	dir := t.TempDir()
	s := InMemorySessionStore(dir)
	s.Observables().Put("plan", "active", "1/2", "", "")
	// In-memory stores have no session file at all, so any
	// <sessionFile>.cards path is "" and no file gets written.
	if got := s.Observables().List(); len(got) != 1 {
		t.Fatalf("expected 1 card in memory, got %d", len(got))
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		t.Errorf("in-memory store created file %s", e.Name())
	}
}
