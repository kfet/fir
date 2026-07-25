package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
)

// catalogDoc builds a valid catalog document body.
func catalogDoc(t *testing.T, generatedAt string, body string) string {
	t.Helper()
	if body == "" {
		body = `{}`
	}
	return `{"schemaVersion":1,"generatedAt":"` + generatedAt + `","providers":` + body + `}`
}

// newTestRegistry returns a registry rooted at a temp agent dir, with catalog
// fetching pointed at url (empty = disabled).
func newCatalogTestRegistry(t *testing.T, agentDir, url string) *ModelRegistry {
	t.Helper()
	if url == "" {
		t.Setenv("FIR_NO_CATALOG_OVERLAY", "1")
	} else {
		t.Setenv("FIR_CATALOG_OVERLAY_URL", url)
	}
	storage := auth.NewAuthStorage(filepath.Join(agentDir, "auth.json"))
	return NewModelRegistry(storage, filepath.Join(agentDir, "models.json"))
}

// registryHasModel reports whether a model was actually registered. Find() is
// unsuitable here: it falls back to sibling synthesis for unknown IDs.
func registryHasModel(r *ModelRegistry, provider, id string) bool {
	for _, m := range r.GetAll() {
		if m.Provider == provider && m.ID == id {
			return true
		}
	}
	return false
}

func writeCatalogCache(t *testing.T, agentDir, content string) string {
	t.Helper()
	path := filepath.Join(agentDir, "cache", catalogFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// --- Schema / validation ---

func TestEmbeddedCatalogIsValid(t *testing.T) {
	// Guards the publish path: the file in the tree is what gets shipped to
	// the fleet, so a typo must fail CI, not the fleet.
	o, err := parseCatalogOverlay(embeddedCatalog)
	if err != nil {
		t.Fatalf("embedded catalog-v1.json is invalid: %v", err)
	}
	if o.SchemaVersion != catalogSchemaVersion {
		t.Fatalf("schemaVersion = %d, want %d", o.SchemaVersion, catalogSchemaVersion)
	}
}

func TestParseCatalogOverlayRejects(t *testing.T) {
	cases := map[string]string{
		"malformed json":        `{not json`,
		"empty":                 ``,
		"unknown schemaVersion": `{"schemaVersion":99,"generatedAt":"2026-01-01T00:00:00Z","providers":{}}`,
		"missing schemaVersion": `{"generatedAt":"2026-01-01T00:00:00Z","providers":{}}`,
		"missing generatedAt":   `{"schemaVersion":1,"providers":{}}`,
		"bad generatedAt":       `{"schemaVersion":1,"generatedAt":"yesterday","providers":{}}`,
		"missing providers":     `{"schemaVersion":1,"generatedAt":"2026-01-01T00:00:00Z"}`,
		"provider apiKey":       catalogDoc(t, "2026-01-01T00:00:00Z", `{"anthropic":{"apiKey":"sk-x","modelOverrides":{"a":{"name":"b"}}}}`),
		"provider baseUrl":      catalogDoc(t, "2026-01-01T00:00:00Z", `{"anthropic":{"baseUrl":"https://evil.example"}}`),
		"provider headers":      catalogDoc(t, "2026-01-01T00:00:00Z", `{"anthropic":{"headers":{"X":"y"},"modelOverrides":{"a":{"name":"b"}}}}`),
		"provider authHeader":   catalogDoc(t, "2026-01-01T00:00:00Z", `{"anthropic":{"authHeader":true,"modelOverrides":{"a":{"name":"b"}}}}`),
		// Endpoint/credential fields are rejected at EVERY level the schema
		// allows them, not just the provider level.
		"model baseUrl":    catalogDoc(t, "2026-01-01T00:00:00Z", `{"anthropic":{"models":[{"id":"x","baseUrl":"https://evil.example"}]}}`),
		"model headers":    catalogDoc(t, "2026-01-01T00:00:00Z", `{"anthropic":{"models":[{"id":"x","headers":{"Authorization":"Bearer y"}}]}}`),
		"override headers": catalogDoc(t, "2026-01-01T00:00:00Z", `{"anthropic":{"modelOverrides":{"claude-opus-5":{"headers":{"Authorization":"Bearer y"}}}}}`),
		"invalid model":    catalogDoc(t, "2026-01-01T00:00:00Z", `{"anthropic":{"models":[{"id":"x","contextWindow":0}]}}`),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseCatalogOverlay([]byte(body)); err == nil {
				t.Fatalf("expected rejection, got none")
			}
		})
	}
}

// --- Load precedence between embedded snapshot and cache ---

func TestLoadCatalogOverlayPrefersNewerGeneratedAt(t *testing.T) {
	embedded, err := parseCatalogOverlay(embeddedCatalog)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("newer cache wins", func(t *testing.T) {
		dir := t.TempDir()
		newer := embedded.GeneratedAt.Add(24 * time.Hour).UTC().Format(time.RFC3339)
		writeCatalogCache(t, dir, catalogDoc(t, newer, `{"anthropic":{"models":[{"id":"cache-model"}]}}`))
		r := newCatalogTestRegistry(t, dir, "")
		if got := r.ModelOrigin("anthropic", "cache-model"); got != OriginOverlay {
			t.Fatalf("origin = %q, want %q", got, OriginOverlay)
		}
	})

	t.Run("older cache loses to embedded snapshot", func(t *testing.T) {
		// A host that upgraded its binary offline: the embedded snapshot can
		// be newer than a long-stale cache.
		dir := t.TempDir()
		older := embedded.GeneratedAt.Add(-24 * time.Hour).UTC().Format(time.RFC3339)
		writeCatalogCache(t, dir, catalogDoc(t, older, `{"anthropic":{"models":[{"id":"cache-model"}]}}`))
		r := newCatalogTestRegistry(t, dir, "")
		if registryHasModel(r, "anthropic", "cache-model") {
			t.Fatal("stale cache should not have been applied over a newer embedded snapshot")
		}
	})

	t.Run("malformed cache falls back to embedded snapshot", func(t *testing.T) {
		dir := t.TempDir()
		writeCatalogCache(t, dir, `{"schemaVersion":`)
		r := newCatalogTestRegistry(t, dir, "")
		if got := r.GetError(); got != "" {
			t.Fatalf("malformed cache must not surface a user error, got %q", got)
		}
		if len(r.GetAll()) == 0 {
			t.Fatal("built-in models must still load")
		}
	})

	t.Run("unknown schemaVersion in cache is ignored", func(t *testing.T) {
		dir := t.TempDir()
		newer := embedded.GeneratedAt.Add(24 * time.Hour).UTC().Format(time.RFC3339)
		writeCatalogCache(t, dir,
			`{"schemaVersion":2,"generatedAt":"`+newer+`","providers":{"anthropic":{"models":[{"id":"v2-model"}]}}}`)
		r := newCatalogTestRegistry(t, dir, "")
		if registryHasModel(r, "anthropic", "v2-model") {
			t.Fatal("a document from an unknown schema version must be ignored wholesale")
		}
		if len(r.GetAll()) == 0 {
			t.Fatal("built-in models must still load")
		}
	})
}

func TestNoCatalogSourcesStillLoadsBuiltIns(t *testing.T) {
	// Offline-first: no cache, no network, and (simulated) no embedded
	// snapshot must never degrade below built-ins-only.
	dir := t.TempDir()
	r := newCatalogTestRegistry(t, dir, "")
	if len(r.GetAll()) == 0 {
		t.Fatal("expected built-in models")
	}
	if got := r.ModelOrigin("anthropic", "claude-opus-5"); got != OriginBuiltIn {
		t.Fatalf("origin = %q, want %q", got, OriginBuiltIn)
	}
}

// --- Merge precedence ---

func TestOverlayOverridesBuiltIn(t *testing.T) {
	dir := t.TempDir()
	embedded, err := parseCatalogOverlay(embeddedCatalog)
	if err != nil {
		t.Fatal(err)
	}
	newer := embedded.GeneratedAt.Add(time.Hour).UTC().Format(time.RFC3339)
	writeCatalogCache(t, dir, catalogDoc(t, newer,
		`{"anthropic":{"modelOverrides":{"claude-opus-5":{"contextWindow":999999}}}}`))

	r := newCatalogTestRegistry(t, dir, "")
	m := r.Find("anthropic", "claude-opus-5")
	if m == nil {
		t.Fatal("built-in model missing")
	}
	if m.ContextWindow != 999999 {
		t.Fatalf("contextWindow = %d, want overlay value", m.ContextWindow)
	}
	if got := r.ModelOrigin("anthropic", "claude-opus-5"); got != OriginOverlay {
		t.Fatalf("origin = %q, want %q", got, OriginOverlay)
	}
}

func TestUserConfigBeatsOverlay(t *testing.T) {
	embedded, err := parseCatalogOverlay(embeddedCatalog)
	if err != nil {
		t.Fatal(err)
	}
	newer := embedded.GeneratedAt.Add(time.Hour).UTC().Format(time.RFC3339)
	overlay := catalogDoc(t, newer,
		`{"anthropic":{"modelOverrides":{"claude-opus-5":{"contextWindow":111,"maxTokens":222}}}}`)

	t.Run("models.json wins", func(t *testing.T) {
		dir := t.TempDir()
		writeCatalogCache(t, dir, overlay)
		if err := os.WriteFile(filepath.Join(dir, "models.json"),
			[]byte(`{"providers":{"anthropic":{"modelOverrides":{"claude-opus-5":{"contextWindow":333}}}}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		r := newCatalogTestRegistry(t, dir, "")
		m := r.Find("anthropic", "claude-opus-5")
		if m.ContextWindow != 333 {
			t.Fatalf("contextWindow = %d, want user value 333", m.ContextWindow)
		}
		if got := r.ModelOrigin("anthropic", "claude-opus-5"); got != OriginUserModelsJSON {
			t.Fatalf("origin = %q, want %q", got, OriginUserModelsJSON)
		}
	})

	t.Run("models.d fragment wins over models.json and overlay", func(t *testing.T) {
		dir := t.TempDir()
		writeCatalogCache(t, dir, overlay)
		if err := os.WriteFile(filepath.Join(dir, "models.json"),
			[]byte(`{"providers":{"anthropic":{"modelOverrides":{"claude-opus-5":{"contextWindow":333}}}}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		fragDir := filepath.Join(dir, "models.d")
		if err := os.MkdirAll(fragDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fragDir, "10-local.json"),
			[]byte(`{"providers":{"anthropic":{"modelOverrides":{"claude-opus-5":{"contextWindow":444}}}}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		r := newCatalogTestRegistry(t, dir, "")
		if m := r.Find("anthropic", "claude-opus-5"); m.ContextWindow != 444 {
			t.Fatalf("contextWindow = %d, want fragment value 444", m.ContextWindow)
		}
		if got := r.ModelOrigin("anthropic", "claude-opus-5"); got != OriginUserFragment+"10-local.json" {
			t.Fatalf("origin = %q, want fragment origin", got)
		}
	})
}

func TestBrokenUserConfigDoesNotSurfaceOverlayErrors(t *testing.T) {
	// A broken models.json degrades to built-ins-only with a loud error —
	// exactly today's behaviour, not worse — and never blames the overlay.
	dir := t.TempDir()
	embedded, _ := parseCatalogOverlay(embeddedCatalog)
	newer := embedded.GeneratedAt.Add(time.Hour).UTC().Format(time.RFC3339)
	writeCatalogCache(t, dir, catalogDoc(t, newer, `{"anthropic":{"models":[{"id":"overlay-model"}]}}`))
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{ broken`), 0o644); err != nil {
		t.Fatal(err)
	}
	r := newCatalogTestRegistry(t, dir, "")
	if r.GetError() == "" {
		t.Fatal("expected a models.json parse error")
	}
	if len(r.GetAll()) == 0 {
		t.Fatal("built-in models must still load")
	}
}

// --- Provider defaults ---

func TestOverlayOverridesProviderDefault(t *testing.T) {
	dir := t.TempDir()
	embedded, _ := parseCatalogOverlay(embeddedCatalog)
	newer := embedded.GeneratedAt.Add(time.Hour).UTC().Format(time.RFC3339)
	writeCatalogCache(t, dir,
		`{"schemaVersion":1,"generatedAt":"`+newer+`","providerDefaults":{"anthropic":"claude-opus-9"},"providers":{}}`)

	r := newCatalogTestRegistry(t, dir, "")
	if got := r.DefaultModelForProvider(ai.ProviderAnthropic); got != "claude-opus-9" {
		t.Fatalf("default = %q, want overlay value", got)
	}
	// Built-in remains the offline fallback for providers the overlay
	// doesn't mention.
	if got := r.DefaultModelForProvider(ai.ProviderOpenAI); got == "" {
		t.Fatal("expected built-in default for an unmentioned provider")
	}
}

func TestDefaultModelForProviderUnknownProvider(t *testing.T) {
	r := newCatalogTestRegistry(t, t.TempDir(), "")
	if got := r.DefaultModelForProvider(ai.Provider("nope")); got != "" {
		t.Fatalf("default = %q, want empty", got)
	}
}

// --- Fetch failure modes ---

func TestRefreshCatalogOverlayFailureModes(t *testing.T) {
	embedded, _ := parseCatalogOverlay(embeddedCatalog)
	newer := embedded.GeneratedAt.Add(time.Hour).UTC().Format(time.RFC3339)

	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"404", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }},
		{"500", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }},
		{"malformed json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{nope`)) }},
		{"unknown schemaVersion", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"schemaVersion":42,"generatedAt":"` + newer + `","providers":{}}`))
		}},
		{"rejected provider field", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(catalogDoc(t, newer, `{"anthropic":{"baseUrl":"https://evil.example"}}`)))
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()

			dir := t.TempDir()
			r := newCatalogTestRegistry(t, dir, srv.URL)
			before := len(r.GetAll())

			changed, err := r.RefreshCatalogOverlay(context.Background())
			if err == nil {
				t.Fatal("expected an error")
			}
			if changed {
				t.Fatal("a failed fetch must not report a change")
			}
			if got := r.GetError(); got != "" {
				t.Fatalf("failure must stay soft, got user error %q", got)
			}
			if len(r.GetAll()) != before {
				t.Fatal("registry must be unchanged after a failed fetch")
			}
			if _, err := os.Stat(filepath.Join(dir, "cache", catalogFileName)); !os.IsNotExist(err) {
				t.Fatal("a rejected document must not be cached")
			}
		})
	}
}

func TestRefreshCatalogOverlayNoNetwork(t *testing.T) {
	// Server closed before the request: connection refused, not a timeout,
	// so the test stays deterministic and fast.
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	r := newCatalogTestRegistry(t, t.TempDir(), url)
	if _, err := r.RefreshCatalogOverlay(context.Background()); err == nil {
		t.Fatal("expected a connection error")
	}
	if len(r.GetAll()) == 0 {
		t.Fatal("fir must work fully offline")
	}
}

func TestRefreshCatalogOverlayCancelledContext(t *testing.T) {
	// Cancellation stands in for a timeout without any wall-clock waiting.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()

	r := newCatalogTestRegistry(t, t.TempDir(), srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.RefreshCatalogOverlay(ctx); err == nil {
		t.Fatal("expected a cancellation error")
	}
}

func TestRefreshCatalogOverlayAppliesAndDetectsChange(t *testing.T) {
	embedded, _ := parseCatalogOverlay(embeddedCatalog)
	newer := embedded.GeneratedAt.Add(time.Hour).UTC().Format(time.RFC3339)
	body := catalogDoc(t, newer, `{"anthropic":{"models":[{"id":"fetched-model","name":"Fetched"}]}}`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	dir := t.TempDir()
	r := newCatalogTestRegistry(t, dir, srv.URL)

	changed, err := r.RefreshCatalogOverlay(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected the fetched document to differ from the embedded snapshot")
	}

	// The cache holds the published bytes verbatim.
	cached, err := os.ReadFile(filepath.Join(dir, "cache", catalogFileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(cached) != body {
		t.Fatal("cache must be the published document, byte for byte")
	}

	// Hot-apply: the model appears without a restart.
	r.Refresh()
	if !registryHasModel(r, "anthropic", "fetched-model") {
		t.Fatal("fetched model not applied after Refresh")
	}
	if m := r.Find("anthropic", "fetched-model"); m.Name != "Fetched" {
		t.Fatalf("name = %q", m.Name)
	}

	// Re-fetching the same document reports no change, so no needless reload.
	changed, err = r.RefreshCatalogOverlay(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("an unchanged document must not report a change")
	}
}

func TestRefreshCatalogOverlayOlderThanEmbeddedIsNotAChange(t *testing.T) {
	// A published document that loses the generatedAt comparison is cached
	// but never loaded, so reporting a change would rebuild the whole
	// registry on every tick, forever.
	embedded, _ := parseCatalogOverlay(embeddedCatalog)
	older := embedded.GeneratedAt.Add(-time.Hour).UTC().Format(time.RFC3339)
	body := catalogDoc(t, older, `{"anthropic":{"models":[{"id":"rolled-back"}]}}`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	dir := t.TempDir()
	r := newCatalogTestRegistry(t, dir, srv.URL)

	for i := 0; i < 3; i++ {
		changed, err := r.RefreshCatalogOverlay(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if changed {
			t.Fatal("a document the loader would not pick must not report a change")
		}
	}
	if registryHasModel(r, "anthropic", "rolled-back") {
		t.Fatal("an older document must not be applied")
	}
}

func TestRefreshCatalogOverlayCacheWriteFailureIsAnError(t *testing.T) {
	// An unwritable cache means the fetch achieved nothing — it must not
	// claim a change a reload would never see.
	embedded, _ := parseCatalogOverlay(embeddedCatalog)
	newer := embedded.GeneratedAt.Add(time.Hour).UTC().Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(catalogDoc(t, newer, `{}`)))
	}))
	defer srv.Close()

	dir := t.TempDir()
	// Occupy the cache directory path with a regular file so MkdirAll fails.
	if err := os.WriteFile(filepath.Join(dir, "cache"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := newCatalogTestRegistry(t, dir, srv.URL)
	changed, err := r.RefreshCatalogOverlay(context.Background())
	if err == nil {
		t.Fatal("expected a cache write error")
	}
	if changed {
		t.Fatal("a failed cache write must not report a change")
	}
}

func TestStartCatalogOverlayFetchDoesInitialFetch(t *testing.T) {
	embedded, _ := parseCatalogOverlay(embeddedCatalog)
	newer := embedded.GeneratedAt.Add(time.Hour).UTC().Format(time.RFC3339)
	body := catalogDoc(t, newer, `{"anthropic":{"models":[{"id":"started"}]}}`)

	// The handler signals on a channel, so the test waits on the actual
	// event rather than on the clock.
	hit := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
		select {
		case hit <- struct{}{}:
		default:
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	r := newCatalogTestRegistry(t, dir, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.StartCatalogOverlayFetch(ctx)
	<-hit
}

func TestConcurrentRefreshIsSerialised(t *testing.T) {
	// Rebuilds must not interleave; readers must stay safe throughout.
	// Run under -race for this to mean anything.
	r := newCatalogTestRegistry(t, t.TempDir(), "")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Refresh()
			_ = r.GetAll()
			_ = r.ModelOrigin("anthropic", "claude-opus-5")
			_ = r.DefaultModelForProvider(ai.ProviderAnthropic)
		}()
	}
	wg.Wait()
	if len(r.GetAll()) == 0 {
		t.Fatal("registry emptied by concurrent refreshes")
	}
}

func TestCatalogFetchDisabledInTestBinaryByDefault(t *testing.T) {
	// A test binary must never reach the public catalog URL; opting in
	// requires pointing FIR_CATALOG_OVERLAY_URL at a local server.
	t.Setenv("FIR_NO_CATALOG_OVERLAY", "")
	t.Setenv("FIR_CATALOG_OVERLAY_URL", "")
	if catalogFetchEnabled() {
		t.Fatal("catalog fetching must be off by default inside a test binary")
	}
	t.Setenv("FIR_CATALOG_OVERLAY_URL", "http://127.0.0.1:0/x")
	if !catalogFetchEnabled() {
		t.Fatal("an explicit overlay URL must re-enable fetching")
	}
	t.Setenv("FIR_NO_CATALOG_OVERLAY", "1")
	if catalogFetchEnabled() {
		t.Fatal("FIR_NO_CATALOG_OVERLAY must win over an explicit URL")
	}
}

func TestCatalogFetchDisabledByEnv(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte(catalogDoc(t, "2099-01-01T00:00:00Z", `{}`)))
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("FIR_CATALOG_OVERLAY_URL", srv.URL)
	t.Setenv("FIR_NO_CATALOG_OVERLAY", "1")

	storage := auth.NewAuthStorage(filepath.Join(dir, "auth.json"))
	r := NewModelRegistry(storage, filepath.Join(dir, "models.json"))
	r.StartCatalogOverlayFetch(context.Background())
	r.ForceRefreshCatalogOverlay(context.Background())

	if hits != 0 {
		t.Fatalf("fetch happened despite FIR_NO_CATALOG_OVERLAY (%d hits)", hits)
	}
	if len(r.GetAll()) == 0 {
		t.Fatal("the embedded snapshot must still apply when fetching is disabled")
	}
}

func TestCatalogCacheFreshnessGovernsFetch(t *testing.T) {
	dir := t.TempDir()
	path := writeCatalogCache(t, dir, catalogDoc(t, "2026-01-01T00:00:00Z", `{}`))
	if !catalogCacheFresh(path) {
		t.Fatal("a just-written cache must be fresh")
	}
	old := time.Now().Add(-2 * catalogTTL)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if catalogCacheFresh(path) {
		t.Fatal("a past-TTL cache must not be fresh")
	}
	if catalogCacheFresh(filepath.Join(dir, "missing.json")) {
		t.Fatal("a missing cache must not be fresh")
	}
}

func TestWriteFileAtomicReplaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "f.json")
	if err := writeFileAtomic(path, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, []byte("two")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "two" {
		t.Fatalf("content = %q", got)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %d entries", len(entries))
	}
}

func TestCatalogOverlayRoundTripsAsModelsFragment(t *testing.T) {
	// The overlay must be exactly a models.d fragment — same schema, same
	// merge code. If these ever diverge, this fails.
	o, err := parseCatalogOverlay([]byte(catalogDoc(t, "2026-01-01T00:00:00Z",
		`{"anthropic":{"models":[{"id":"x"}],"modelOverrides":{"y":{"name":"Y"}}}}`)))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(o.modelsConfig())
	if err != nil {
		t.Fatal(err)
	}
	var frag ModelsConfig
	if err := json.Unmarshal(raw, &frag); err != nil {
		t.Fatal(err)
	}
	if len(frag.Providers["anthropic"].Models) != 1 || frag.Providers["anthropic"].Models[0].ID != "x" {
		t.Fatalf("fragment round-trip lost data: %s", raw)
	}
}
