package extension

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/session/store"
)

// TestObservableCards_SyntheticExtToCardsFile is the design's bedrock
// invariant: a synthetic extension calls put_observable → the card
// lands in the per-session cards file with source stamped to the
// extension's name. This is the end-to-end "live IS at-rest" check.
func TestObservableCards_SyntheticExtToCardsFile(t *testing.T) {
	dir := t.TempDir()

	// Synthetic extension: fires put_observable on session_start.
	extDir := filepath.Join(dir, ".fir", "extensions")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(extDir, "synth-card")
	script := `#!/bin/sh
# ---
# name: synth-card
# ---
read line
echo '{"jsonrpc":"2.0","id":1,"result":{"name":"synth-card","tools":[],"events":["session_start"]}}'
read line
echo '{"jsonrpc":"2.0","id":99,"method":"put_observable","params":{"key":"active","slug":"stamped","detail":"via-rpc"}}'
cat >/dev/null
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ts := NewTrustStoreWithPath(filepath.Join(dir, "trust.json"))
	hash, _ := ComputeHash(scriptPath)
	_ = ts.RecordTrust(dir, "synth-card", hash)

	cardsPath := filepath.Join(dir, "session.jsonl.cards")
	st := store.NewObservableStore(cardsPath)

	api := newMockAPI()
	api.observableStore = st

	mgr := NewManager(slog.Default())
	mgr.SetTrustStore(ts)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx, dir, dir, api); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop() //nolint:errcheck

	// Drive the extension by firing session_start.
	mgr.EmitEvent("session_start", nil)

	// Wait for the card to land in the store. The store updates
	// synchronously on Put; the only async piece is the extension's
	// stdout being read by the bridge. Use a short poll.
	deadline := time.Now().Add(20 * time.Second)
	var got []store.Card
	for time.Now().Before(deadline) {
		got = st.List()
		if len(got) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 card to land via RPC, got %d", len(got))
	}
	if got[0].Source != "synth-card" || got[0].Key != "active" || got[0].Slug != "stamped" || got[0].Detail != "via-rpc" {
		t.Fatalf("card content mismatch: %#v", got[0])
	}

	// On-disk JSON must match the in-memory snapshot — the "sidecar
	// IS canonical" invariant. Same syscall as a live reader.
	//
	// Poll for the file: Store.Put updates the in-memory map under its
	// lock and releases it BEFORE calling flush() (the file write is a
	// separate, flushMu-guarded step). So the List() poll above can
	// observe the card a hair before the sidecar lands on disk. Under
	// `-race` + CI load that sub-millisecond gap widens enough that an
	// immediate ReadFile races the flush and 404s. Polling closes the
	// gap without weakening the invariant (the file is still canonical;
	// it just arrives a moment after the memory mutation).
	var disk []store.Card
	deadline = time.Now().Add(10 * time.Second)
	for {
		data, err := os.ReadFile(cardsPath)
		if err == nil {
			if uerr := json.Unmarshal(data, &disk); uerr != nil {
				t.Fatalf("decode cards file: %v\nraw:\n%s", uerr, data)
			}
			if len(disk) == 1 && disk[0].Source == "synth-card" {
				break
			}
		} else if !os.IsNotExist(err) {
			t.Fatalf("cards file read error: %v", err)
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("cards file not written in time (last read err on missing file): %#v", disk)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestObservableCards_ReexecRestoresLastKnownState pins the design's
// reexec story: a new ObservableStore constructed over the same path
// hydrates from disk before any producer has re-Put. This is what
// makes /reexec "just work" — the footer/header populate from
// last-known state pre-handshake.
func TestObservableCards_ReexecRestoresLastKnownState(t *testing.T) {
	dir := t.TempDir()
	cardsPath := filepath.Join(dir, "session.jsonl.cards")

	// Round 1 — simulate the pre-reexec process writing cards.
	s1 := store.NewObservableStore(cardsPath)
	s1.Put("plan", "active", "2/5 in_progress", "step 2", "tc-pre-reexec")
	s1.Put("mood", "current", "focused", "deep in work", "tc-mood")

	// Round 2 — simulate the post-reexec process constructing a fresh
	// store over the same path. No explicit restore call: the
	// constructor reads the file. Cards must be visible immediately,
	// before any bridge handshake or producer re-Put.
	s2 := store.NewObservableStore(cardsPath)

	got := s2.List()
	if len(got) != 2 {
		t.Fatalf("expected 2 cards on reexec, got %d", len(got))
	}
	// EntryID survives — provenance link is intact across reexec.
	foundPlan := false
	for _, c := range got {
		if c.Source == "plan" && c.Key == "active" {
			foundPlan = true
			if c.EntryID != "tc-pre-reexec" {
				t.Errorf("plan card EntryID lost: %q", c.EntryID)
			}
			if c.Slug != "2/5 in_progress" {
				t.Errorf("plan card slug lost: %q", c.Slug)
			}
		}
	}
	if !foundPlan {
		t.Fatalf("plan/active card missing after reexec")
	}
}

// TestObservableCards_CheckableInvariant_NoExtensionImports is the
// design's first checkable invariant from docs/design/observable-cards.md:
//
//	rg "import.*\.fir\.extensions\." pkg/resources/builtin_extensions/
//
// must produce no matches. The test fails loudly if the rule is broken
// so a future PR can't accidentally leak cross-extension imports.
func TestObservableCards_CheckableInvariant_NoExtensionImports(t *testing.T) {
	root := filepath.Join("..", "resources", "builtin_extensions")
	var violations []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Ext(path) != ".py" {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		for _, line := range strings.Split(string(data), "\n") {
			trim := strings.TrimLeft(line, " \t")
			if (strings.HasPrefix(trim, "import ") || strings.HasPrefix(trim, "from ")) &&
				strings.Contains(line, ".fir.extensions.") {
				violations = append(violations, path+": "+line)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(violations) != 0 {
		t.Errorf("cross-extension imports detected (violates invariant 6):\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// TestObservableCards_CheckableInvariant_NoHardcodedSources is the
// design's second checkable invariant:
//
//	rg '"plan"|"mood"|"footer"' pkg/session/store/observables.go
//
// must be empty — the store knows nothing about specific producers.
// If it does, the abstraction has leaked.
func TestObservableCards_CheckableInvariant_NoHardcodedSources(t *testing.T) {
	path := filepath.Join("..", "session", "store", "observables.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	for _, forbidden := range []string{`"plan"`, `"mood"`, `"footer"`} {
		if strings.Contains(src, forbidden) {
			t.Errorf("observables.go references %s — the abstraction has leaked; remove the hard-coded source", forbidden)
		}
	}
}
