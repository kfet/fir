package models

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/fir/pkg/auth"
	"github.com/kfet/pinoauth"
)

// clientVersionDoc builds a catalog document carrying a clientVersions block.
func clientVersionDoc(generatedAt, clientVersions string) string {
	return `{"schemaVersion":1,"generatedAt":"` + generatedAt + `","clientVersions":` +
		clientVersions + `,"providers":{}}`
}

func TestValidateClientVersion(t *testing.T) {
	cases := []struct {
		name  string
		value string
		ok    bool
	}{
		{"empty is the floor", "", true},
		{"three components", "2.1.280", true},
		{"single component", "2", true},
		{"four components", "1.2.3.4", true},
		{"five components", "1.2.3.4.5", false},
		{"pre-release tag", "2.1.280-beta", false},
		{"header injection", "2.1.280; x-evil: 1", false},
		{"CRLF injection", "2.1.280\r\nX-Evil: 1", false},
		{"leading space", " 2.1.280", false},
		{"trailing newline", "2.1.280\n", false},
		{"unicode digits", "٢.١.٢٨٠", false},
		{"too long", strings.Repeat("1", ClientVersionMaxLen+1), false},
		{"exactly at the cap", "99999999.99999999.99999999.99999", true}, // 32 bytes
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateClientVersion("clientVersions.claudeCode", tc.value)
			if tc.ok && err != nil {
				t.Fatalf("value %q: unexpected error %v", tc.value, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("value %q: expected rejection, got none", tc.value)
			}
		})
	}
}

func TestParseCatalogOverlayRejectsBadClientVersion(t *testing.T) {
	cases := map[string]string{
		"injection":  clientVersionDoc("2026-01-01T00:00:00Z", `{"claudeCode":"2.1.280; x-evil: 1"}`),
		"free form":  clientVersionDoc("2026-01-01T00:00:00Z", `{"claudeCode":"latest"}`),
		"too long":   clientVersionDoc("2026-01-01T00:00:00Z", `{"claudeCode":"`+strings.Repeat("1", 33)+`"}`),
		"prerelease": clientVersionDoc("2026-01-01T00:00:00Z", `{"claudeCode":"2.1.280-beta.1"}`),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseCatalogOverlay([]byte(body)); err == nil {
				t.Fatal("expected rejection, got none")
			}
		})
	}

	// Absent and empty are both fine — the compiled-in floor applies.
	for _, body := range []string{
		`{"schemaVersion":1,"generatedAt":"2026-01-01T00:00:00Z","providers":{}}`,
		clientVersionDoc("2026-01-01T00:00:00Z", `{}`),
		clientVersionDoc("2026-01-01T00:00:00Z", `{"claudeCode":"2.1.280"}`),
	} {
		if _, err := ParseCatalogOverlay([]byte(body)); err != nil {
			t.Fatalf("document %s rejected: %v", body, err)
		}
	}
}

func TestEmbeddedCatalogCarriesClaudeCodePin(t *testing.T) {
	// The pin is the compiled-in FLOOR: without it in the embedded snapshot,
	// an offline host would advertise an empty version.
	o, err := ParseCatalogOverlay(embeddedCatalog)
	if err != nil {
		t.Fatal(err)
	}
	if o.ClientVersions == nil || o.ClientVersions.ClaudeCode == "" {
		t.Fatal("embedded catalog must set clientVersions.claudeCode")
	}
	if got := DefaultClientVersions().Get(ClientVersionKeyClaudeCode); got != o.ClientVersions.ClaudeCode {
		t.Fatalf("DefaultClientVersions = %q, embedded = %q", got, o.ClientVersions.ClaudeCode)
	}
	if got := DefaultClientVersions().Get("nope"); got != "" {
		t.Fatalf("unknown key = %q, want empty", got)
	}
}

func TestClientVersionsOmittedWhenAbsent(t *testing.T) {
	// The pointer exists precisely so an unset field is ABSENT on the wire:
	// `omitempty` on a struct value would emit `"clientVersions": {}` into
	// every canonical document.
	out, err := MarshalCatalogOverlay(&CatalogOverlay{
		SchemaVersion: catalogSchemaVersion,
		GeneratedAt:   time.Now().UTC(),
		Providers:     map[string]ProviderConfig{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "clientVersions") {
		t.Fatalf("unset clientVersions must not be marshalled:\n%s", out)
	}
}

func TestCompareClientVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"2.1.280", "2.1.280", 0},
		// The whole reason for a numeric compare: a string compare says
		// "2.1.280" < "2.1.90".
		{"2.1.90", "2.1.280", -1},
		{"2.1.280", "2.1.90", 1},
		{"2", "2.0.0", 0},
		{"2.0.1", "2", 1},
		{"1.9.9", "2.0.0", -1},
		{"3.0.0.1", "3", 1},
	}
	for _, tc := range cases {
		if got := CompareClientVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareClientVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestClientVersionFloorSemantics(t *testing.T) {
	floor := DefaultClientVersions().Get(ClientVersionKeyClaudeCode)
	if floor == "" {
		t.Fatal("no compiled-in floor to test against")
	}
	cases := []struct {
		name    string
		overlay string
		want    string
	}{
		{"absent falls back to the floor", "", floor},
		{"equal to the floor", floor, floor},
		{"older than the floor is ignored", "0.0.1", floor},
		{"newer than the floor advances", "99.0.0", "99.0.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := maxClientVersion(tc.overlay, floor); got != tc.want {
				t.Fatalf("maxClientVersion(%q, %q) = %q, want %q", tc.overlay, floor, got, tc.want)
			}
		})
	}
}

func TestRegistryClientVersion(t *testing.T) {
	dir := t.TempDir()
	r := newCatalogTestRegistry(t, dir, "")
	floor := DefaultClientVersions().Get(ClientVersionKeyClaudeCode)
	if got := r.ClientVersion(ClientVersionKeyClaudeCode); got != floor {
		t.Fatalf("ClientVersion = %q, want the floor %q", got, floor)
	}
	if got := r.ClientVersion("noSuchKey"); got != "" {
		t.Fatalf("unknown key = %q, want empty", got)
	}
}

func TestRefreshCatalogOverlayMovesClientVersion(t *testing.T) {
	// The point of the whole feature: publishing a document that changes ONLY
	// the pin moves the advertised version, with no rebuild and no restart.
	embedded, _ := ParseCatalogOverlay(embeddedCatalog)
	newer := embedded.GeneratedAt.Add(time.Hour).UTC().Format(time.RFC3339)
	body := clientVersionDoc(newer, `{"claudeCode":"99.1.2"}`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	r := newCatalogTestRegistry(t, t.TempDir(), srv.URL)
	if got := r.ClientVersion(ClientVersionKeyClaudeCode); got != embedded.ClientVersions.ClaudeCode {
		t.Fatalf("before refresh: %q", got)
	}

	changed, err := r.RefreshCatalogOverlay(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("a moved pin must count as a changed document")
	}
	r.Refresh()

	if got := r.ClientVersion(ClientVersionKeyClaudeCode); got != "99.1.2" {
		t.Fatalf("after refresh: ClientVersion = %q, want 99.1.2", got)
	}
}

func TestRefreshCatalogOverlayCannotDowngradeClientVersion(t *testing.T) {
	// Data may only ever ADVANCE the pin. A published document naming a
	// version older than the binary shipped with is loaded (it is a newer
	// document) but cannot drag the advertised version below the floor.
	embedded, _ := ParseCatalogOverlay(embeddedCatalog)
	newer := embedded.GeneratedAt.Add(time.Hour).UTC().Format(time.RFC3339)
	body := clientVersionDoc(newer, `{"claudeCode":"0.0.1"}`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	r := newCatalogTestRegistry(t, t.TempDir(), srv.URL)
	if _, err := r.RefreshCatalogOverlay(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.Refresh()

	if got := r.ClientVersion(ClientVersionKeyClaudeCode); got != embedded.ClientVersions.ClaudeCode {
		t.Fatalf("ClientVersion = %q, want the floor %q", got, embedded.ClientVersions.ClaudeCode)
	}
}

// pinObservingProvider records the registry's effective pin at the moment
// ModifyModels runs — which is exactly when a header template is expanded.
type pinObservingProvider struct {
	registry *ModelRegistry
	seen     []string
}

func (p *pinObservingProvider) ID() string               { return "pin-observer" }
func (p *pinObservingProvider) Name() string             { return "Pin Observer" }
func (p *pinObservingProvider) UsesCallbackServer() bool { return true }
func (p *pinObservingProvider) Login(_ context.Context, _ pinoauth.LoginCallbacks) (*ai.OAuthCredentials, error) {
	return nil, nil
}
func (p *pinObservingProvider) RefreshToken(_ context.Context, _ *ai.OAuthCredentials) (*ai.OAuthCredentials, error) {
	return nil, nil
}
func (p *pinObservingProvider) GetAPIKey(_ *ai.OAuthCredentials) string { return "" }
func (p *pinObservingProvider) ListModels(_ context.Context, _ *ai.OAuthCredentials) ([]string, error) {
	return nil, nil
}
func (p *pinObservingProvider) ModelDefaults(_ string, _ []*ai.Model) *ai.Model { return nil }
func (p *pinObservingProvider) ModifyModels(models []*ai.Model, _ *ai.OAuthCredentials) []*ai.Model {
	p.seen = append(p.seen, p.registry.ClientVersion(ClientVersionKeyClaudeCode))
	return models
}

// The gated path is ModifyModels: that is where a header template gets
// expanded. If the new pin were only installed after buildModels returned,
// the expansion would use the PREVIOUS document and a freshly published pin
// would not reach the messages-API headers until the rebuild after next.
func TestModifyModelsSeesTheNewPinInTheSameRebuild(t *testing.T) {
	embedded, _ := ParseCatalogOverlay(embeddedCatalog)
	newer := embedded.GeneratedAt.Add(time.Hour).UTC().Format(time.RFC3339)
	body := clientVersionDoc(newer, `{"claudeCode":"98.7.6"}`)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	dir := t.TempDir()
	r := newCatalogTestRegistry(t, dir, srv.URL)

	observer := &pinObservingProvider{registry: r}
	saved := ai.GetOAuthProvider(observer.ID())
	ai.RegisterOAuthProvider(observer)
	t.Cleanup(func() {
		if saved == nil {
			ai.ResetOAuthProviders()
		}
	})
	// The provider is only consulted for a credential of OAuth type.
	storage := auth.NewAuthStorage(filepath.Join(dir, "auth.json"))
	if err := storage.Set(observer.ID(), auth.AuthCredential{
		Type: auth.CredentialTypeOAuth, Access: "tok", Expires: time.Now().Add(time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	r.authStorage.Reload()

	if _, err := r.RefreshCatalogOverlay(context.Background()); err != nil {
		t.Fatal(err)
	}
	observer.seen = nil
	r.Refresh()

	if len(observer.seen) == 0 {
		t.Fatal("ModifyModels never ran — the test cannot observe the pin")
	}
	if got := observer.seen[len(observer.seen)-1]; got != "98.7.6" {
		t.Fatalf("ModifyModels saw pin %q, want the freshly published 98.7.6", got)
	}
}
