package models

import (
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	firlog "github.com/kfet/fir/pkg/log"
)

// The catalog overlay ships model metadata as DATA rather than as a binary
// release. It is a models.d-shaped document published as a static file in the
// public kfet/fir-dist repo, fetched over HTTPS on a TTL, and merged into the
// model registry ABOVE the compiled built-in catalog and BELOW the user's own
// models.json / models.d fragments. Merging a catalog change is therefore
// enough to teach the whole fleet about a new model — no release, no redeploy.
//
// Trust model: fir-dist already gates *binary* execution (`fir update` pulls
// executables from it over HTTPS). Fetching a JSON document from the same
// channel is strictly less trust than what we already grant it, so there is
// deliberately no separate signing infrastructure here.
//
// # Schema compatibility — read before changing anything
//
// Once overlays float independently of binaries, an old binary can be handed a
// new document and vice versa, so field changes are no longer free:
//
//   - WITHIN a schema version, changes must be purely ADDITIVE: new optional
//     fields only. Never remove a field, never repurpose one, never change the
//     meaning or type of an existing one. Old binaries silently ignore unknown
//     fields, which is exactly what makes additive changes safe.
//   - A BREAKING change requires a new schema version AND a new published
//     filename (catalog-v2.json). The versioned filename — not the in-band
//     schemaVersion — is the primary compatibility mechanism: new binaries
//     fetch the new file while old binaries keep receiving updates from the
//     frozen v1 file. If instead we only bumped the in-band version, every
//     un-upgraded binary in the fleet would fall back to its embedded snapshot
//     and stop receiving catalog updates entirely — precisely the
//     binary-release coupling this feature exists to remove.
//   - The in-band schemaVersion is a belt-and-braces sanity check: a document
//     whose version this binary does not understand (including a missing or
//     zero version) is ignored wholesale and the embedded snapshot is used.
//
// # Rolling back a published catalog
//
// A binary loads whichever of its embedded snapshot and its cached document
// has the later generatedAt, so publishing a document with an OLDER timestamp
// can never take effect on a binary whose embedded snapshot is newer. TO ROLL
// BACK, RE-PUBLISH THE OLD CONTENT WITH A FRESH generatedAt. Reverting the
// commit alone is not enough.
//
// Two consequences of the clientVersions floor (see DefaultClientVersions)
// are worth stating here:
//
//   - Because the effective pin is max(overlay, embedded), published data can
//     never roll the fleet BELOW what each binary shipped with. Going below
//     that is a binary release, by design.
//   - A too-high pin (a vendor rejecting an unknown version) rolls back the
//     same way as anything else here — re-publish the previous number with a
//     fresh generatedAt — and takes effect within a TTL with no restart,
//     because placeholder expansion happens at call time.
//
// The wire format is ProviderConfig / ModelDefinition / ModelOverride from
// modelregistry.go. Those types are now a PUBLISHED wire format, not just a
// local config shape — the same additive-only rule applies to them.
const catalogSchemaVersion = 1

// catalogFileName is the published artifact name, the embedded snapshot name
// and the on-disk cache name — all deliberately identical so the file can be
// traced end to end. Bump the -vN suffix together with catalogSchemaVersion.
const catalogFileName = "catalog-v1.json"

// defaultCatalogURL points at the raw file on the fir-dist default branch. A
// static file over HTTPS, not a service: nothing here has its own uptime.
const defaultCatalogURL = "https://raw.githubusercontent.com/kfet/fir-dist/main/" + catalogFileName

const (
	// catalogTTL bounds how stale a cached overlay may be before a fetch is
	// attempted. It governs re-fetching only: a past-TTL cache is still used
	// (stale data beats no data), it just triggers a refresh.
	catalogTTL = 1 * time.Hour
	// catalogFetchTimeout caps the HTTP fetch. Failure is always soft.
	catalogFetchTimeout = 10 * time.Second
	// catalogMaxBytes caps the response body so a runaway document cannot
	// exhaust memory.
	catalogMaxBytes = 8 << 20
)

// embeddedCatalog is the offline baseline. It guarantees fir behaves
// identically to a release build with no network, a cold cache, or a dead
// fir-dist. It is the same file that gets published.
//
//go:embed catalog-v1.json
var embeddedCatalog []byte

// CatalogOverlay is the published catalog document. Providers has exactly the
// models.d shape and is merged with exactly the models.d merge code.
type CatalogOverlay struct {
	// SchemaVersion must equal catalogSchemaVersion; see the compatibility
	// rules above.
	SchemaVersion int `json:"schemaVersion"`
	// GeneratedAt is a required RFC3339 timestamp. It is how a cached
	// document and the embedded snapshot are compared after a binary
	// upgrade, so it must be total and monotonic across publishes.
	GeneratedAt time.Time `json:"generatedAt"`
	// ProviderDefaults overrides ai.RegisteredProvider.DefaultModelID per
	// provider. Moving a provider's default is plainly data; the built-in
	// value stays as the offline fallback.
	ProviderDefaults map[string]string `json:"providerDefaults,omitempty"`
	// ClientVersions carries third-party client version pins that builtin
	// auth extensions interpolate into HARDCODED header templates via the
	// {clientVersion.<key>} placeholder. Each key is a closed, named scalar
	// validated by validateClientVersion; the set of keys is part of the
	// schema and grows only additively. See the "spirit of the no-headers
	// rule" note on validateCatalogProviders.
	//
	// A POINTER deliberately: `omitempty` on a struct VALUE does not omit,
	// so a value field would force `"clientVersions": {}` into every
	// canonical document. With a pointer, "field absent ⇒ old behaviour" is
	// literally true on the wire.
	ClientVersions *CatalogClientVersions `json:"clientVersions,omitempty"`
	// Providers is a models.d-shaped fragment.
	Providers map[string]ProviderConfig `json:"providers"`
}

// CatalogClientVersions is a CLOSED set. A struct, deliberately not a
// map[string]string: every pin is an individually-named field, so an old
// binary cannot be handed a key it never heard of and a new binary cannot
// accept a key nobody validated. Adding a second pin is a code change —
// the document floats, the vocabulary is versioned with the binary.
type CatalogClientVersions struct {
	// ClaudeCode is the Claude Code CLI version advertised by the
	// anthropic-auth extension as `claude-cli/<v> (external, cli)`.
	// Anthropic gates new models on it.
	ClaudeCode string `json:"claudeCode,omitempty"`
}

// ClientVersionRE is the entire grammar: 1-4 dot-separated numeric
// components. No pre-release tags, no build metadata, no whitespace. The
// grammar is what makes header injection ("2.1.280; x-evil: 1", CRLF,
// unicode) structurally impossible. Exported so cmd/generate-models
// validates a proposed pin with exactly the code that will later load it.
var ClientVersionRE = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,3}$`)

// ClientVersionMaxLen caps the scalar so a runaway document is impossible.
const ClientVersionMaxLen = 32

// validateClientVersion enforces the grammar and the length cap.
func validateClientVersion(where, v string) error {
	if v == "" {
		return nil // absent is fine: the compiled-in floor applies
	}
	if len(v) > ClientVersionMaxLen {
		return fmt.Errorf("%s: client version too long (%d > %d)", where, len(v), ClientVersionMaxLen)
	}
	if !ClientVersionRE.MatchString(v) {
		return fmt.Errorf("%s: %q is not a plain dotted version", where, v)
	}
	return nil
}

// validateCatalogClientVersions validates every named pin. Nil (field
// absent) is valid and means "use the compiled-in floor".
func validateCatalogClientVersions(cv *CatalogClientVersions) error {
	if cv == nil {
		return nil
	}
	return validateClientVersion("clientVersions.claudeCode", cv.ClaudeCode)
}

// ClientVersionKeyClaudeCode is the JSON name of the Claude Code pin, used
// by the {clientVersion.<key>} placeholder.
const ClientVersionKeyClaudeCode = "claudeCode"

// Get returns the pin named by its JSON key, or "" for an unknown key.
func (cv *CatalogClientVersions) Get(key string) string {
	if cv == nil {
		return ""
	}
	if key == ClientVersionKeyClaudeCode {
		return cv.ClaudeCode
	}
	return ""
}

// DefaultClientVersions is the compiled-in FLOOR: the embedded snapshot's
// clientVersions. Exported so callers that run before any registry exists
// (`fir login` from the CLI, tests) get the same answer the registry would.
//
// The floor exists so a published document can only ever ADVANCE a pin:
// data can never roll the fleet below what each binary shipped with and was
// tested against. Rolling below that is a binary release, by design.
func DefaultClientVersions() *CatalogClientVersions {
	embeddedClientVersionsOnce.Do(func() {
		o, err := ParseCatalogOverlay(embeddedCatalog)
		if err != nil || o == nil || o.ClientVersions == nil {
			embeddedClientVersions = &CatalogClientVersions{}
			return
		}
		cv := *o.ClientVersions
		embeddedClientVersions = &cv
	})
	return embeddedClientVersions
}

var (
	embeddedClientVersionsOnce sync.Once
	embeddedClientVersions     *CatalogClientVersions
)

// CompareClientVersions compares two strings matching ClientVersionRE
// component-wise and numerically (so 2.1.90 < 2.1.280, which a string
// compare gets wrong). Missing trailing components count as zero. Four
// integers do not justify a semver dependency.
func CompareClientVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		av, bv := 0, 0
		if i < len(as) {
			av, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bv, _ = strconv.Atoi(bs[i])
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
}

// modelsConfig views the overlay as a plain models.d fragment.
func (o *CatalogOverlay) modelsConfig() *ModelsConfig {
	return &ModelsConfig{Providers: o.Providers}
}

// ParseCatalogOverlay decodes and validates a catalog document. Every rejection
// is an error the caller turns into "ignore this document" — never a
// user-visible failure. Exported so cmd/generate-models can validate an
// overlay it proposes with exactly the code that will later load it.
func ParseCatalogOverlay(data []byte) (*CatalogOverlay, error) {
	var o CatalogOverlay
	if err := json.Unmarshal(data, &o); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if o.SchemaVersion != catalogSchemaVersion {
		return nil, fmt.Errorf("unsupported schemaVersion %d (want %d)", o.SchemaVersion, catalogSchemaVersion)
	}
	if o.GeneratedAt.IsZero() {
		return nil, fmt.Errorf("missing generatedAt")
	}
	if o.Providers == nil {
		return nil, fmt.Errorf("missing providers")
	}
	if err := validateCatalogProviders(o.Providers); err != nil {
		return nil, err
	}
	if err := validateCatalogClientVersions(o.ClientVersions); err != nil {
		return nil, err
	}
	if err := validateModelsConfig(o.modelsConfig()); err != nil {
		return nil, err
	}
	return &o, nil
}

// validateCatalogProviders rejects the fields that a remotely fetched document
// has no business setting: anything that decides WHERE a request goes or WHAT
// credentials ride along. The threat is not a hostile fir-dist (see the trust
// note above) — it is an accidental typo in a merged catalog change silently
// redirecting the fleet's credentials or bricking a provider within the TTL.
// Endpoint and auth stay under the operator's control; the overlay describes
// model metadata only. The invariant to preserve when editing this schema: the
// overlay can change what a request LOOKS LIKE and which model is CHOSEN, never
// where it GOES or what CREDENTIAL it carries.
//
// Checked at every level the schema allows them: provider, model definition,
// and per-model override.
//
// # Why clientVersions does not breach this rule
//
// The rejected fields are rejected because each is an OPEN-ENDED INSTRUCTION:
// baseUrl redirects, apiKey/authHeader swap credentials, headers is a free map
// that can carry either. A clientVersions scalar is none of those. It must
// match ClientVersionRE and fit in ClientVersionMaxLen; it is substituted only
// into a template whose entire text is compiled into an extension; it lands in
// a header VALUE, never a header name and never a URL; and the compiled-in
// value is a floor it can only advance. A wrong number produces a visible
// vendor 400 on new models, recoverable within one TTL by re-publishing — the
// same blast radius as a typo'd model entry, which is the accidental-typo
// threat this function names. It is squarely on the "what a request LOOKS
// LIKE" side of the invariant.
//
// The key set stays CLOSED IN THE BINARY (CatalogClientVersions is a struct,
// not a map). Adding a second pin is a code change: the document floats, the
// vocabulary is versioned with the binary.
//
// Consequence: since validateModelsConfig requires baseUrl+apiKey for a
// non-built-in provider that defines models, the overlay can only add models
// to BUILT-IN providers. That is fine — a genuinely new provider needs a
// stream function, i.e. a binary anyway.
func validateCatalogProviders(providers map[string]ProviderConfig) error {
	reject := func(where, field string) error {
		return fmt.Errorf("%s: %q is not allowed in the catalog overlay", where, field)
	}
	for name, pc := range providers {
		where := "provider " + name
		switch {
		case pc.ApiKey != "":
			return reject(where, "apiKey")
		case pc.BaseURL != "":
			return reject(where, "baseUrl")
		case len(pc.Headers) > 0:
			return reject(where, "headers")
		case pc.AuthHeader != nil:
			return reject(where, "authHeader")
		}
		for _, m := range pc.Models {
			where := "provider " + name + ", model " + m.ID
			switch {
			case m.BaseURL != "":
				return reject(where, "baseUrl")
			case len(m.Headers) > 0:
				return reject(where, "headers")
			}
		}
		for id, ov := range pc.ModelOverrides {
			if len(ov.Headers) > 0 {
				return reject("provider "+name+", modelOverrides."+id, "headers")
			}
		}
	}
	return nil
}

// MarshalCatalogOverlay renders an overlay document in the canonical published
// form: two-space indent, trailing newline. It is the single definition of
// that form — cmd/generate-models writes proposed entries with it and
// TestEmbeddedCatalogIsCanonical holds the committed file to it, so a
// generated hunk is a few added lines rather than a 300-line reformat of
// somebody's hand-written entries.
func MarshalCatalogOverlay(o *CatalogOverlay) ([]byte, error) {
	data, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// catalogCachePath returns the on-disk cache path, or "" when the registry has
// no agent directory to hang a cache off (embedded snapshot only).
func (r *ModelRegistry) catalogCachePath() string {
	if r.modelsJsonPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(r.modelsJsonPath), "cache", catalogFileName)
}

// loadCatalogOverlay picks the best locally-available overlay: the newer of the
// cached document and the embedded snapshot, by generatedAt. Cache-always-wins
// would be wrong — after a binary upgrade on a host that cannot reach the
// network, the embedded snapshot can be newer than a long-stale cache.
//
// Returns nil only if neither source is usable, which degrades to exactly
// today's built-ins-only behaviour.
func (r *ModelRegistry) loadCatalogOverlay() (*CatalogOverlay, []byte) {
	best, bestRaw := (*CatalogOverlay)(nil), []byte(nil)
	consider := func(data []byte, src string) {
		if len(data) == 0 {
			return
		}
		o, err := ParseCatalogOverlay(data)
		if err != nil {
			firlog.Debug("catalog overlay: ignoring %s: %v", src, err)
			return
		}
		if best == nil || o.GeneratedAt.After(best.GeneratedAt) {
			best, bestRaw = o, data
		}
	}

	consider(embeddedCatalog, "embedded snapshot")
	if path := r.catalogCachePath(); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			consider(data, "cache "+path)
		}
	}
	if best != nil {
		firlog.Debug("catalog overlay: using document generated %s", best.GeneratedAt.Format(time.RFC3339))
	}
	return best, bestRaw
}

// catalogFetchEnabled reports whether the overlay may be fetched from the
// network. FIR_NO_CATALOG_OVERLAY=1 is the operator escape hatch: it disables
// FETCHING only. The embedded snapshot still applies, because it is morally
// part of the binary — setting the env var pins a host to whatever catalog its
// binary shipped with.
//
// Test binaries never reach the public network by default: session
// construction starts this fetcher, so without the guard every test that
// builds a session would depend on the internet and could write into a
// t.TempDir() after the test body returned, racing its cleanup. Tests that
// exercise the fetch path point FIR_CATALOG_OVERLAY_URL at an httptest server,
// which re-enables it. (Same spirit as update.CheckLatest not phoning home on
// dev builds; fold both into one policy if the SDK ever grows such a knob.)
func catalogFetchEnabled() bool {
	switch os.Getenv("FIR_NO_CATALOG_OVERLAY") {
	case "", "0", "false":
	default:
		return false
	}
	if os.Getenv("FIR_CATALOG_OVERLAY_URL") != "" {
		return true
	}
	// flag.Lookup avoids importing testing into the production binary; the
	// testing package registers test.* flags only in a test binary.
	return flag.Lookup("test.v") == nil
}

// catalogURL is the fetch URL. FIR_CATALOG_OVERLAY_URL overrides it — used to
// point at a staging file, or at an httptest server in tests.
func catalogURL() string {
	if u := os.Getenv("FIR_CATALOG_OVERLAY_URL"); u != "" {
		return u
	}
	return defaultCatalogURL
}

// catalogCacheFresh reports whether the cached document is within the TTL.
func catalogCacheFresh(path string) bool {
	if path == "" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && time.Since(st.ModTime()) < catalogTTL
}

// fetchCatalogOverlay GETs and validates the published document.
func fetchCatalogOverlay(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, catalogFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, catalogMaxBytes))
	if err != nil {
		return nil, err
	}
	if _, err := ParseCatalogOverlay(data); err != nil {
		return nil, err
	}
	return data, nil
}

// RefreshCatalogOverlay fetches the published catalog, caches it, and reports
// whether a reload would actually pick up different bytes. Synchronous and
// side-effect-contained so tests can drive it directly with no sleeps.
//
// Every failure mode — no network, timeout, 404, malformed JSON, unknown
// schemaVersion, an unwritable cache — returns an error and changes nothing:
// the already-loaded overlay (cache or embedded) stays in force.
func (r *ModelRegistry) RefreshCatalogOverlay(ctx context.Context) (bool, error) {
	data, err := fetchCatalogOverlay(ctx, catalogURL())
	if err != nil {
		return false, err
	}

	// The cache is how a fetched document reaches the loader, so a failed
	// write means the fetch achieved nothing — report it as a failure rather
	// than claiming a change that a reload would not see.
	path := r.catalogCachePath()
	if path == "" {
		return false, nil // no agent dir: nowhere to persist, nothing to apply
	}
	if err := ctx.Err(); err != nil {
		return false, err // cancelled mid-flight: don't write after teardown
	}
	if err := writeFileAtomic(path, data); err != nil {
		return false, fmt.Errorf("cache write: %w", err)
	}

	// Compare against what the loader would now choose, not against the bytes
	// we just fetched: load takes the newer of the embedded snapshot and the
	// cache, so a published document that loses that comparison changes
	// nothing — and must not report a change, or every tick would trigger a
	// pointless full registry rebuild forever.
	_, winnerRaw := r.loadCatalogOverlay()
	r.mu.RLock()
	same := string(r.catalogRaw) == string(winnerRaw)
	r.mu.RUnlock()
	return !same, nil
}

// StartCatalogOverlayFetch keeps the catalog overlay fresh for the lifetime of
// ctx: an initial refresh (skipped while the cached document is still within
// the TTL, so short-lived CLI invocations don't each hit the network) followed
// by one every catalogTTL. The periodic tick is what makes "merge a catalog
// change, the fleet follows within the TTL" true for long-lived bot processes
// that may not restart for weeks. Worst-case staleness is bounded by 2×TTL,
// when a process starts against an almost-expired cache.
func (r *ModelRegistry) StartCatalogOverlayFetch(ctx context.Context) {
	if !r.catalogFetchable() {
		return
	}
	go func() {
		if !catalogCacheFresh(r.catalogCachePath()) {
			r.refreshAndApplyCatalog(ctx)
		}
		ticker := time.NewTicker(catalogTTL)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.refreshAndApplyCatalog(ctx)
			}
		}
	}()
}

// ForceRefreshCatalogOverlay refreshes the catalog overlay in the background
// regardless of the cache TTL. This is the operator's force-refresh gesture,
// wired to /reload.
func (r *ModelRegistry) ForceRefreshCatalogOverlay(ctx context.Context) {
	if !r.catalogFetchable() {
		return
	}
	go r.refreshAndApplyCatalog(ctx)
}

// catalogFetchable reports whether fetching is worth starting at all: enabled,
// and with somewhere to persist the result (the cache is the only route from a
// fetch to the loader).
func (r *ModelRegistry) catalogFetchable() bool {
	if !catalogFetchEnabled() {
		firlog.Debug("catalog overlay: fetching disabled")
		return false
	}
	if r.catalogCachePath() == "" {
		firlog.Debug("catalog overlay: no agent dir, using embedded snapshot only")
		return false
	}
	return true
}

// refreshAndApplyCatalog refreshes and, when the document actually changed,
// hot-applies it so a running process picks it up without a restart.
func (r *ModelRegistry) refreshAndApplyCatalog(ctx context.Context) {
	changed, err := r.RefreshCatalogOverlay(ctx)
	if err != nil {
		firlog.Debug("catalog overlay: fetch failed: %v", err)
		return
	}
	if changed {
		firlog.Debug("catalog overlay: updated, reloading model registry")
		r.Refresh()
	}
}

// writeFileAtomic writes via a temp file + rename so a crashed or truncated
// write can never leave a half-JSON cache behind.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
