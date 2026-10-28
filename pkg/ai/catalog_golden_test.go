package ai

import (
	"bytes"
	"compress/flate"
	"flag"
	"os"
	"testing"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/registry_golden.txt")

// TestRegistryGolden pins the full built-in catalog: every field of every
// model, nil vs empty included. Regenerating the catalog legitimately changes
// it; refresh with `go test ./pkg/ai -run TestRegistryGolden -update-golden`.
func TestRegistryGolden(t *testing.T) {
	got := dumpRegistry()
	const path = "testdata/registry_golden.txt"
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("registry differs from %s (rerun with -update-golden if the catalog change is intended)", path)
	}
}

// TestCatalogRoundTrip re-encodes the registry and decodes it again; the
// result must dump identically, so the generator's encoder and the loader
// cannot drift apart.
func TestCatalogRoundTrip(t *testing.T) {
	var ms []*Model
	ensureCatalog()
	modelRegistryMu.RLock()
	for _, pm := range modelRegistry {
		for _, m := range pm {
			ms = append(ms, m)
		}
	}
	modelRegistryMu.RUnlock()
	b := EncodeCatalog(ms)
	back, err := decodeCatalog(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != len(ms) {
		t.Fatalf("got %d models, want %d", len(back), len(ms))
	}
	orig := dumpRegistry()
	modelRegistryMu.Lock()
	saved := modelRegistry
	modelRegistry = make(map[Provider]map[string]*Model)
	modelRegistryMu.Unlock()
	t.Cleanup(func() { modelRegistryMu.Lock(); modelRegistry = saved; modelRegistryMu.Unlock() })
	for _, m := range back {
		RegisterModel(m)
	}
	if dumpRegistry() != orig {
		t.Fatal("round-tripped registry differs")
	}
}

func TestRegisterCatalogCorrupt(t *testing.T) {
	before := dumpRegistry()
	registerCatalog([]byte("not a catalog"))
	if dumpRegistry() != before {
		t.Fatal("corrupt catalog must not change the registry")
	}
}

func TestDecodeCatalogBadJSON(t *testing.T) {
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, flate.BestSpeed)
	_, _ = w.Write([]byte("{not json"))
	_ = w.Close()
	if _, err := decodeCatalog(buf.Bytes()); err == nil {
		t.Fatal("want error")
	}
}

// TestCatalogOverrideAfterLoad: a custom registration overrides the
// built-in entry. (RegisterModel calls ensureCatalog first, so the same
// holds when it is the very first registry access.)
func TestCatalogOverrideAfterLoad(t *testing.T) {
	var base *Model
	for _, p := range GetProviders() {
		if ms := GetModels(p); len(ms) > 0 {
			base = ms[0]
			break
		}
	}
	if base == nil {
		t.Fatal("built-in catalog is empty")
	}
	custom := *base
	custom.Name = "override"
	RegisterModel(&custom)
	t.Cleanup(func() { RegisterModel(base) })
	if got := GetModel(base.Provider, base.ID); got.Name != "override" {
		t.Fatalf("override lost: %q", got.Name)
	}
}
