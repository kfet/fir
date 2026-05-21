package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// loadCardsRaw reads a cards file directly (bypassing the store) so tests
// can assert on the on-disk JSON shape, not just the in-memory cache.
func loadCardsRaw(t *testing.T, path string) []Card {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cards file %s: %v", path, err)
	}
	var cards []Card
	if err := json.Unmarshal(data, &cards); err != nil {
		t.Fatalf("decode cards file: %v\nraw:\n%s", err, data)
	}
	return cards
}

func TestObservableStore_PutListFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cards.json")
	s := NewObservableStore(path)

	s.Put("plan", "active", "3/8", "step three in progress", "tc-1")
	s.Put("mood", "current", "engaged", "feels productive", "tc-2")

	got := s.List()
	if len(got) != 2 {
		t.Fatalf("expected 2 cards, got %d: %#v", len(got), got)
	}

	// List() is ordered (Source asc, Ts desc).
	if got[0].Source != "mood" || got[1].Source != "plan" {
		t.Fatalf("List() source order = %s, %s; want mood, plan", got[0].Source, got[1].Source)
	}

	// On-disk file must contain both cards by content. On-disk order
	// is unspecified — consumers sort on read if they need a shape.
	disk := loadCardsRaw(t, path)
	if len(disk) != 2 {
		t.Fatalf("expected 2 cards on disk, got %d", len(disk))
	}
	byKey := make(map[string]Card, 2)
	for _, c := range disk {
		byKey[c.Source+"/"+c.Key] = c
	}
	if c := byKey["mood/current"]; c.Slug != "engaged" {
		t.Errorf("mood/current slug = %q, want engaged", c.Slug)
	}
	if c := byKey["plan/active"]; c.EntryID != "tc-1" {
		t.Errorf("plan/active entry_id = %q, want tc-1", c.EntryID)
	}
}

func TestObservableStore_PutReplacesByKey(t *testing.T) {
	s := NewObservableStore("")
	s.Put("plan", "active", "1/8", "first", "e1")
	s.Put("plan", "active", "2/8", "second", "e2")
	s.Put("plan", "active", "3/8", "third", "e3")

	got := s.List()
	if len(got) != 1 {
		t.Fatalf("expected 1 card after 3 puts on same (source,key), got %d", len(got))
	}
	if got[0].Slug != "3/8" || got[0].Detail != "third" || got[0].EntryID != "e3" {
		t.Fatalf("expected latest values; got %#v", got[0])
	}
}

func TestObservableStore_ClearRemovesAndPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cards.json")
	s := NewObservableStore(path)

	s.Put("plan", "active", "1/2", "first", "e1")
	s.Put("mood", "current", "calm", "", "e2")
	s.Clear("plan", "active")

	got := s.List()
	if len(got) != 1 || got[0].Source != "mood" {
		t.Fatalf("expected only mood card, got %#v", got)
	}
	disk := loadCardsRaw(t, path)
	if len(disk) != 1 || disk[0].Source != "mood" {
		t.Fatalf("on-disk expected only mood card, got %#v", disk)
	}

	// Clearing absent key is a silent no-op (no file rewrite needed,
	// but no error either).
	s.Clear("plan", "absent")
}

func TestObservableStore_RejectsEmptySourceOrKey(t *testing.T) {
	s := NewObservableStore("")
	s.Put("", "k", "slug", "", "")
	s.Put("src", "", "slug", "", "")
	if got := s.List(); len(got) != 0 {
		t.Fatalf("expected empty list, got %d cards", len(got))
	}
}

func TestObservableStore_SlugTruncatedToMaxLen(t *testing.T) {
	s := NewObservableStore("")
	long := strings.Repeat("x", SlugMaxLen+10)
	s.Put("plan", "k", long, "", "")
	got := s.List()
	if len(got) != 1 {
		t.Fatalf("expected 1 card, got %d", len(got))
	}
	if len(got[0].Slug) > SlugMaxLen {
		t.Errorf("slug not truncated: len=%d max=%d", len(got[0].Slug), SlugMaxLen)
	}
	if got[0].Slug != strings.Repeat("x", SlugMaxLen) {
		t.Errorf("expected %d 'x' runes, got %q", SlugMaxLen, got[0].Slug)
	}
}

func TestObservableStore_SlugTruncationRespectsRuneBoundary(t *testing.T) {
	// 24 emoji is well over the byte limit even if rune count fits.
	emoji := strings.Repeat("🌲", SlugMaxLen+5)
	s := NewObservableStore("")
	s.Put("src", "k", emoji, "", "")
	got := s.List()
	if len(got) != 1 {
		t.Fatalf("expected 1 card, got %d", len(got))
	}
	// Each tree is 4 bytes; we want exactly SlugMaxLen runes,
	// 4*SlugMaxLen bytes.
	if got[0].Slug != strings.Repeat("🌲", SlugMaxLen) {
		t.Errorf("rune-truncated slug wrong: %q", got[0].Slug)
	}
}

func TestObservableStore_LoadFromExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cards.json")

	// Round 1 — write and tear down the store.
	s1 := NewObservableStore(path)
	s1.Put("plan", "active", "2/5", "step 2", "tc-x")
	s1.Put("mood", "current", "engaged", "", "tc-y")

	// Round 2 — fresh store reading the same file (this is the
	// reexec scenario in miniature: the old process is gone, a new
	// one constructs a store, and the last-known cards are visible
	// before any producer has written anything).
	s2 := NewObservableStore(path)
	got := s2.List()
	if len(got) != 2 {
		t.Fatalf("expected 2 cards after reload, got %d", len(got))
	}
	planFound := false
	for _, c := range got {
		if c.Source == "plan" && c.Key == "active" {
			planFound = true
			if c.Slug != "2/5" {
				t.Errorf("plan slug = %q want 2/5", c.Slug)
			}
		}
	}
	if !planFound {
		t.Fatalf("plan/active card missing after reload")
	}
}

func TestObservableStore_LoadFromMissingFileIsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doesnotexist.json")
	s := NewObservableStore(path)
	if got := s.List(); len(got) != 0 {
		t.Fatalf("expected empty list from missing file, got %d", len(got))
	}
}

func TestObservableStore_LoadFromCorruptFileIsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cards.json")
	if err := os.WriteFile(path, []byte("not json at all"), 0600); err != nil {
		t.Fatal(err)
	}
	s := NewObservableStore(path)
	if got := s.List(); len(got) != 0 {
		t.Fatalf("expected empty list from corrupt file, got %d", len(got))
	}
	// Next Put should rewrite the file cleanly.
	s.Put("src", "k", "slug", "", "")
	disk := loadCardsRaw(t, path)
	if len(disk) != 1 {
		t.Fatalf("expected 1 card after Put on corrupted file, got %d", len(disk))
	}
}

func TestObservableStore_NilReceiver(t *testing.T) {
	var s *ObservableStore
	// All operations must be safe no-ops on nil receiver — this is
	// how callers avoid threading guards through every Put site.
	s.Put("src", "k", "slug", "", "")
	s.Clear("src", "k")
	if got := s.List(); got != nil {
		t.Fatalf("expected nil List() on nil store, got %#v", got)
	}
}

func TestObservableStore_InMemoryNoFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cards.json")
	s := NewObservableStore("") // in-memory mode
	s.Put("src", "k", "slug", "detail", "e1")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("in-memory store must not touch the disk")
	}
	got := s.List()
	if len(got) != 1 {
		t.Fatalf("expected 1 in-memory card, got %d", len(got))
	}
}

func TestObservableStore_ConcurrentPutListClear(t *testing.T) {
	// Smoke test under -race: 16 goroutines hammering Put / Clear /
	// List on the same store should not data-race or panic.
	dir := t.TempDir()
	path := filepath.Join(dir, "cards.json")
	s := NewObservableStore(path)

	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				src := "src" + string(rune('a'+id%4))
				key := "k" + string(rune('0'+i%5))
				s.Put(src, key, "slug", "detail", "e")
				if i%7 == 0 {
					s.Clear(src, key)
				}
				_ = s.List()
			}
		}(w)
	}
	wg.Wait()

	// And the file should be valid JSON at the end.
	loadCardsRaw(t, path)
}

func TestCardsPath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"/tmp/foo.jsonl", "/tmp/foo.jsonl.cards"},
		{"sess.jsonl", "sess.jsonl.cards"},
	}
	for _, c := range cases {
		if got := CardsPath(c.in); got != c.want {
			t.Errorf("CardsPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestObservableStore_ConcurrentPutDiskMatchesMemory pins the
// flush-serialisation invariant: after a burst of concurrent Puts the
// on-disk file must agree with the in-memory snapshot. Before flushMu
// was added, two concurrent Puts could reorder their renames and lose
// the later write to disk even though it stayed in memory — the file
// would then show a stale snapshot until the next mutation.
func TestObservableStore_ConcurrentPutDiskMatchesMemory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cards.json")
	s := NewObservableStore(path)

	// 8 goroutines, each writing 50 distinct (source, key) tuples.
	// Each tuple is unique so no in-memory collision; the only thing
	// that can go wrong is the on-disk view falling out of sync.
	const workers, perWorker = 8, 50
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				src := "src" + string(rune('a'+id))
				key := "k" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26))
				s.Put(src, key, "slug", "detail", "e")
			}
		}(w)
	}
	wg.Wait()

	want := len(s.List())
	disk := loadCardsRaw(t, path)
	if len(disk) != want {
		t.Errorf("on-disk count = %d, in-memory count = %d — flush serialisation broken", len(disk), want)
	}
}
