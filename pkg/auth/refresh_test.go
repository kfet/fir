package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/ai"
	"github.com/kfet/pinoauth"
)

// countingRefreshProvider is a stub OAuth provider that mints a distinct new
// token triple on every refresh (mirroring a real provider's rotating refresh
// token) and counts how many grants it was asked for.
type countingRefreshProvider struct {
	id        string
	calls     atomic.Int64
	ttl       time.Duration
	err       error
	onRefresh func()
}

func (p *countingRefreshProvider) ID() string               { return p.id }
func (p *countingRefreshProvider) Name() string             { return "Counting " + p.id }
func (p *countingRefreshProvider) UsesCallbackServer() bool { return false }
func (p *countingRefreshProvider) Login(_ context.Context, _ pinoauth.LoginCallbacks) (*ai.OAuthCredentials, error) {
	return nil, nil
}
func (p *countingRefreshProvider) ListModels(_ context.Context, _ *ai.OAuthCredentials) ([]string, error) {
	return nil, nil
}
func (p *countingRefreshProvider) GetAPIKey(c *ai.OAuthCredentials) string {
	if c == nil {
		return ""
	}
	return c.Access
}
func (p *countingRefreshProvider) ModifyModels(_ []*ai.Model, _ *ai.OAuthCredentials) []*ai.Model {
	return nil
}
func (p *countingRefreshProvider) ModelDefaults(_ string, _ []*ai.Model) *ai.Model { return nil }

func (p *countingRefreshProvider) RefreshToken(_ context.Context, c *ai.OAuthCredentials) (*ai.OAuthCredentials, error) {
	n := p.calls.Add(1)
	if p.onRefresh != nil {
		p.onRefresh()
	}
	if p.err != nil {
		return nil, p.err
	}
	if c == nil || c.Refresh == "" {
		return nil, errNoRefresh
	}
	ttl := p.ttl
	if ttl == 0 {
		ttl = 8 * time.Hour
	}
	return &ai.OAuthCredentials{
		Access:  "access-" + itoa(n),
		Refresh: "refresh-" + itoa(n),
		Expires: time.Now().Add(ttl).UnixMilli(),
	}, nil
}

var errNoRefresh = errStr("no refresh token available")

type errStr string

func (e errStr) Error() string { return string(e) }

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func msFromNow(d time.Duration) int64 { return time.Now().Add(d).UnixMilli() }

// TestRefreshAccounts_AllSlots: every stored slot of a provider is refreshed,
// default account first, and each gets its own new refresh token.
func TestRefreshAccounts_AllSlots(t *testing.T) {
	prov := &countingRefreshProvider{id: "multi-prov"}
	ai.RegisterOAuthProvider(prov)

	s := NewInMemoryAuthStorage(AuthStorageData{
		"multi-prov": {
			Type: CredentialTypeOAuth, Access: "a0", Refresh: "r0",
			Expires: msFromNow(-time.Minute), Label: "default@x.com",
		},
		"multi-prov#work": {
			Type: CredentialTypeOAuth, Access: "a1", Refresh: "r1",
			Expires: msFromNow(-time.Minute), Label: "work@x.com",
		},
		"other-prov": {Type: CredentialTypeOAuth, Access: "z", Refresh: "rz", Expires: 1},
	})

	results := s.RefreshAccounts(context.Background(), "multi-prov", DefaultRefreshWindow, false)
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2: %+v", len(results), results)
	}
	if results[0].SlotKey != "multi-prov" || results[1].SlotKey != "multi-prov#work" {
		t.Errorf("slot order wrong: %s, %s", results[0].SlotKey, results[1].SlotKey)
	}
	for _, r := range results {
		if r.Outcome != OutcomeRefreshed {
			t.Errorf("%s: outcome = %s (%v), want refreshed", r.SlotKey, r.Outcome, r.Err)
		}
		if r.Expires <= time.Now().UnixMilli() {
			t.Errorf("%s: expiry not moved forward: %d", r.SlotKey, r.Expires)
		}
	}
	if results[0].Label != "default@x.com" || results[1].Label != "work@x.com" {
		t.Errorf("labels wrong: %q, %q", results[0].Label, results[1].Label)
	}
	if got := prov.calls.Load(); got != 2 {
		t.Errorf("refresh grants = %d, want 2", got)
	}

	// Rotation actually persisted, and the two slots did not cross-contaminate.
	def, work := s.Get("multi-prov"), s.Get("multi-prov#work")
	if def.Refresh == "r0" || work.Refresh == "r1" {
		t.Errorf("refresh token not rotated: %q / %q", def.Refresh, work.Refresh)
	}
	if def.Refresh == work.Refresh {
		t.Errorf("slots share a refresh token: %q", def.Refresh)
	}
	if def.Label != "default@x.com" || work.Label != "work@x.com" {
		t.Errorf("label lost across refresh: %q / %q", def.Label, work.Label)
	}
	// The unrelated provider was left alone.
	if s.Get("other-prov").Refresh != "rz" {
		t.Error("unrelated provider slot was touched")
	}
}

// TestRefreshAccounts_OneSlotFailureDoesNotAbortOthers: a failing slot is
// reported as failed while the remaining slots still refresh.
func TestRefreshAccounts_OneSlotFailureDoesNotAbortOthers(t *testing.T) {
	prov := &countingRefreshProvider{id: "partial-prov"}
	ai.RegisterOAuthProvider(prov)

	s := NewInMemoryAuthStorage(AuthStorageData{
		// Default slot has no refresh token -> skipped, not fatal.
		"partial-prov": {Type: CredentialTypeOAuth, Access: "a", Expires: msFromNow(-time.Minute)},
		// An api_key slot -> skipped.
		"partial-prov#key": {Type: CredentialTypeAPIKey, Key: "k"},
		// A healthy OAuth slot -> refreshed.
		"partial-prov#ok": {
			Type: CredentialTypeOAuth, Access: "a", Refresh: "r",
			Expires: msFromNow(-time.Minute),
		},
	})

	results := s.RefreshAccounts(context.Background(), "partial-prov", DefaultRefreshWindow, false)
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	byslot := map[string]RefreshResult{}
	for _, r := range results {
		byslot[r.SlotKey] = r
	}
	if got := byslot["partial-prov"].Outcome; got != OutcomeSkipped {
		t.Errorf("no-refresh-token slot outcome = %s, want skipped", got)
	}
	if got := byslot["partial-prov#key"].Outcome; got != OutcomeSkipped {
		t.Errorf("api_key slot outcome = %s, want skipped", got)
	}
	if got := byslot["partial-prov#ok"].Outcome; got != OutcomeRefreshed {
		t.Errorf("healthy slot outcome = %s (%v), want refreshed", got, byslot["partial-prov#ok"].Err)
	}
}

// TestRefreshAccounts_GrantFailureIsolated: a slot whose grant errors is
// reported failed, and the other slot still refreshes.
func TestRefreshAccounts_GrantFailureIsolated(t *testing.T) {
	prov := &countingRefreshProvider{id: "err-prov", err: errStr("invalid_grant: Refresh token expired")}
	ai.RegisterOAuthProvider(prov)

	s := NewInMemoryAuthStorage(AuthStorageData{
		"err-prov":     {Type: CredentialTypeOAuth, Access: "a", Refresh: "r", Expires: msFromNow(-time.Minute)},
		"err-prov#two": {Type: CredentialTypeOAuth, Access: "b", Refresh: "r2", Expires: msFromNow(-time.Minute)},
	})

	results := s.RefreshAccounts(context.Background(), "err-prov", DefaultRefreshWindow, false)
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	for _, r := range results {
		if r.Outcome != OutcomeFailed || r.Err == nil {
			t.Errorf("%s: outcome = %s err = %v, want failed with error", r.SlotKey, r.Outcome, r.Err)
		}
	}
	// Both slots were attempted — the first failure did not abort the loop.
	if got := prov.calls.Load(); got != 2 {
		t.Errorf("refresh attempts = %d, want 2 (failure must not abort the loop)", got)
	}
	// A failed grant must not damage the stored credential.
	if c := s.Get("err-prov"); c == nil || c.Refresh != "r" {
		t.Errorf("stored credential clobbered by failed refresh: %+v", c)
	}
}

// TestRefreshAccounts_RestraintAndForce: a token comfortably inside its
// lifetime is left alone (no grant spent) unless --force is given.
func TestRefreshAccounts_RestraintAndForce(t *testing.T) {
	prov := &countingRefreshProvider{id: "fresh-prov"}
	ai.RegisterOAuthProvider(prov)

	s := NewInMemoryAuthStorage(AuthStorageData{
		"fresh-prov": {
			Type: CredentialTypeOAuth, Access: "a", Refresh: "r",
			Expires: msFromNow(6 * time.Hour),
		},
	})

	results := s.RefreshAccounts(context.Background(), "fresh-prov", time.Hour, false)
	if len(results) != 1 || results[0].Outcome != OutcomeFresh {
		t.Fatalf("fresh token was not left alone: %+v", results)
	}
	if got := prov.calls.Load(); got != 0 {
		t.Errorf("spent %d grants on a fresh token, want 0", got)
	}
	if s.Get("fresh-prov").Refresh != "r" {
		t.Error("refresh token rotated despite being fresh")
	}

	// A wide enough window brings it into range.
	results = s.RefreshAccounts(context.Background(), "fresh-prov", 20*time.Hour, false)
	if results[0].Outcome != OutcomeRefreshed {
		t.Errorf("outcome with 20h window = %s, want refreshed", results[0].Outcome)
	}

	// --force rotates regardless.
	before := s.Get("fresh-prov").Refresh
	results = s.RefreshAccounts(context.Background(), "fresh-prov", time.Hour, true)
	if results[0].Outcome != OutcomeRefreshed {
		t.Errorf("forced outcome = %s, want refreshed", results[0].Outcome)
	}
	if s.Get("fresh-prov").Refresh == before {
		t.Error("--force did not rotate the refresh token")
	}
}

// TestRefreshAccount_UnknownProviderAndSlot covers the two "can't act" paths.
func TestRefreshAccount_UnknownProviderAndSlot(t *testing.T) {
	s := NewInMemoryAuthStorage(AuthStorageData{
		"unregistered-prov": {Type: CredentialTypeOAuth, Access: "a", Refresh: "r", Expires: 1},
	})

	r := s.RefreshAccount(context.Background(), "no-such-slot", time.Hour, false)
	if r.Outcome != OutcomeFailed || r.Err == nil {
		t.Errorf("missing slot: outcome = %s err = %v", r.Outcome, r.Err)
	}

	r = s.RefreshAccount(context.Background(), "unregistered-prov", time.Hour, false)
	if r.Outcome != OutcomeFailed || r.Err == nil {
		t.Errorf("unregistered provider: outcome = %s err = %v", r.Outcome, r.Err)
	}

	if got := s.RefreshAccounts(context.Background(), "nothing-stored", time.Hour, false); len(got) != 0 {
		t.Errorf("provider with no accounts returned %d results, want 0", len(got))
	}
}

// TestRefreshAccount_NoExpiryIsNotRotated: a credential fir treats as
// non-expiring must not have a grant spent on it by the scheduled path.
func TestRefreshAccount_NoExpiryIsNotRotated(t *testing.T) {
	prov := &countingRefreshProvider{id: "noexp-prov"}
	ai.RegisterOAuthProvider(prov)
	s := NewInMemoryAuthStorage(AuthStorageData{
		"noexp-prov": {Type: CredentialTypeOAuth, Access: "a", Refresh: "r"},
	})

	if got := s.RefreshAccount(context.Background(), "noexp-prov", time.Hour, false); got.Outcome != OutcomeFresh {
		t.Errorf("outcome = %s, want fresh", got.Outcome)
	}
	if got := prov.calls.Load(); got != 0 {
		t.Errorf("spent %d grants on a non-expiring credential, want 0", got)
	}
	// --force still works for the operator who really means it.
	if got := s.RefreshAccount(context.Background(), "noexp-prov", time.Hour, true); got.Outcome != OutcomeRefreshed {
		t.Errorf("forced outcome = %s, want refreshed", got.Outcome)
	}
}

// TestRefreshAccount_SingleWriterAcrossStorages is the crux test: two
// AuthStorage instances over the SAME auth.json (the cron-vs-running-fir race)
// must never both spend a grant on the same refresh token. The loser re-reads
// the winner's rotated credential under the flock and declines.
func TestRefreshAccount_SingleWriterAcrossStorages(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	seed := AuthStorageData{
		"race-prov": {
			Type: CredentialTypeOAuth, Access: "a", Refresh: "r0",
			Expires: msFromNow(-time.Minute), Label: "me@x.com",
		},
	}
	b, err := json.MarshalIndent(seed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}

	// Block inside the refresh grant until both goroutines are in flight, so
	// they genuinely contend for the file lock rather than running in series.
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	prov := &countingRefreshProvider{
		id: "race-prov",
		onRefresh: func() {
			entered <- struct{}{}
			<-release
		},
	}
	ai.RegisterOAuthProvider(prov)

	s1 := NewAuthStorage(path)
	s2 := NewAuthStorage(path)

	var wg sync.WaitGroup
	outcomes := make([]RefreshResult, 2)
	for i, s := range []*AuthStorage{s1, s2} {
		wg.Add(1)
		go func(i int, s *AuthStorage) {
			defer wg.Done()
			outcomes[i] = s.RefreshAccount(context.Background(), "race-prov", time.Hour, false)
		}(i, s)
	}

	// Exactly one goroutine can hold the flock, so exactly one reaches the
	// grant. Let it through; the other must then find a fresh credential.
	<-entered
	close(release)
	wg.Wait()

	if got := prov.calls.Load(); got != 1 {
		t.Fatalf("refresh grants = %d, want exactly 1 — the stored refresh token was replayed", got)
	}
	refreshed, fresh := 0, 0
	for _, o := range outcomes {
		switch o.Outcome {
		case OutcomeRefreshed:
			refreshed++
		case OutcomeFresh:
			fresh++
		default:
			t.Errorf("unexpected outcome %s: %v", o.Outcome, o.Err)
		}
	}
	if refreshed != 1 || fresh != 1 {
		t.Errorf("outcomes = %d refreshed / %d fresh, want 1/1", refreshed, fresh)
	}

	// The file on disk holds exactly one rotated credential, with metadata intact.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk AuthStorageData
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("auth.json is not valid JSON after concurrent refresh: %v", err)
	}
	cred := onDisk["race-prov"]
	if cred.Refresh == "r0" {
		t.Error("refresh token on disk was not rotated")
	}
	if cred.Label != "me@x.com" {
		t.Errorf("label lost across refresh: %q", cred.Label)
	}
	if cred.Expires <= time.Now().UnixMilli() {
		t.Errorf("expiry on disk not moved forward: %d", cred.Expires)
	}
}

// TestCredentialNeedsRefresh pins the restraint policy.
func TestCredentialNeedsRefresh(t *testing.T) {
	cases := []struct {
		name   string
		expiry int64
		window time.Duration
		want   bool
	}{
		{"expired", msFromNow(-time.Hour), time.Hour, true},
		{"inside window", msFromNow(30 * time.Minute), time.Hour, true},
		{"outside window", msFromNow(3 * time.Hour), time.Hour, false},
		{"no expiry", 0, time.Hour, false},
		{"zero window, expired", msFromNow(-time.Second), 0, true},
		{"zero window, valid", msFromNow(time.Minute), 0, false},
		{"negative window clamps to zero", msFromNow(time.Minute), -time.Hour, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := credentialNeedsRefresh(AuthCredential{Expires: tc.expiry}, tc.window)
			if got != tc.want {
				t.Errorf("credentialNeedsRefresh = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCarryAccountMetadata_LegacyProjectID: a Google-style account whose
// project id lives only in the legacy top-level column keeps it across a
// refresh that returns a bare token triple.
func TestCarryAccountMetadata_LegacyProjectID(t *testing.T) {
	previous := AuthCredential{
		Type: CredentialTypeOAuth, Access: "a", Refresh: "r",
		Label: "me@x.com", ProjectID: "proj-123",
	}
	updated := carryAccountMetadata(AuthCredential{
		Type: CredentialTypeOAuth, Access: "a2", Refresh: "r2",
	}, previous)

	if updated.Label != "me@x.com" {
		t.Errorf("label = %q, want me@x.com", updated.Label)
	}
	if updated.ProjectID != "proj-123" {
		t.Errorf("legacy ProjectID lost across refresh: %q", updated.ProjectID)
	}
	if got, _ := updated.Extra["projectId"].(string); got != "proj-123" {
		t.Errorf("Extra[projectId] = %q, want proj-123", got)
	}
}

// writeAuthFile seeds an auth.json for the cross-process tests.
func writeAuthFile(t *testing.T, path string, data AuthStorageData) {
	t.Helper()
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

// TestGetApiKey_PicksUpExternalRotation is the regression test for the
// revoke-on-rotate wedge.
//
// Anthropic revokes the previous access token the instant a refresh grant
// rotates the credential. A live session therefore dies the moment cron runs
// `fir auth refresh` — its in-memory access token is revoked while its cached
// `expires` is still hours in the future, so the expiry-driven refresh path
// never fires and it 401s forever. Before the staleness check, this test
// returned the stale (revoked) token.
func TestGetApiKey_PicksUpExternalRotation(t *testing.T) {
	prov := &countingRefreshProvider{id: "rotate-prov"}
	ai.RegisterOAuthProvider(prov)

	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	writeAuthFile(t, path, AuthStorageData{
		"rotate-prov": {
			Type: CredentialTypeOAuth, Access: "stale-access", Refresh: "r0",
			// Comfortably valid: nothing in the expiry-driven path will fire.
			Expires: msFromNow(8 * time.Hour),
		},
	})

	live := NewAuthStorage(path) // the long-running session
	cron := NewAuthStorage(path) // `fir auth refresh` in another process

	if got := live.GetApiKey("rotate-prov"); got != "stale-access" {
		t.Fatalf("precondition: live key = %q, want stale-access", got)
	}

	// Cron force-refreshes. Upstream now considers "stale-access" revoked.
	if r := cron.RefreshAccount(context.Background(), "rotate-prov", time.Hour, true); r.Outcome != OutcomeRefreshed {
		t.Fatalf("cron refresh outcome = %s: %v", r.Outcome, r.Err)
	}
	rotated := cron.Get("rotate-prov").Access
	if rotated == "stale-access" {
		t.Fatal("cron did not actually rotate the credential")
	}

	// The live session must now resolve the rotated token, without having
	// spent a refresh grant of its own.
	grantsBefore := prov.calls.Load()
	if got := live.GetApiKey("rotate-prov"); got != rotated {
		t.Errorf("live session returned %q after external rotation, want %q "+
			"— it is holding a revoked token and will 401 forever", got, rotated)
	}
	if got := prov.calls.Load(); got != grantsBefore {
		t.Errorf("live session spent %d extra refresh grant(s); it should have "+
			"re-read the rotated credential, not requested its own", got-grantsBefore)
	}
}

// TestRefreshApiKey_RereadsEvenWhenStampUnchanged: the post-401 path must not
// depend on filesystem mtime granularity. A filesystem that reports an
// unchanged stamp across a rotation would otherwise strand the session.
func TestRefreshApiKey_RereadsEvenWhenStampUnchanged(t *testing.T) {
	prov := &countingRefreshProvider{id: "stamp-prov"}
	ai.RegisterOAuthProvider(prov)

	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	writeAuthFile(t, path, AuthStorageData{
		"stamp-prov": {Type: CredentialTypeOAuth, Access: "old", Refresh: "r0", Expires: msFromNow(8 * time.Hour)},
	})

	s := NewAuthStorage(path)
	if got := s.GetApiKey("stamp-prov"); got != "old" {
		t.Fatalf("precondition: key = %q", got)
	}

	// Rotate on disk, then forge the stamp to look unchanged — simulating a
	// filesystem whose mtime resolution hid the write.
	writeAuthFile(t, path, AuthStorageData{
		"stamp-prov": {Type: CredentialTypeOAuth, Access: "new", Refresh: "r1", Expires: msFromNow(8 * time.Hour)},
	})
	s.mu.Lock()
	s.lastStamp = s.backendStamp()
	s.mu.Unlock()

	if got := s.GetApiKey("stamp-prov"); got != "old" {
		t.Fatalf("test setup is not exercising the stamp shortcut: got %q", got)
	}
	if got := s.RefreshApiKey("stamp-prov"); got != "new" {
		t.Errorf("RefreshApiKey = %q, want new — the post-401 re-read must be "+
			"unconditional, not stamp-gated", got)
	}
}

// TestReloadIfStale_NoOpWithoutChange: the staleness check must not re-read
// (or disturb in-memory state) when nothing changed on disk.
func TestReloadIfStale_NoOpWithoutChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	writeAuthFile(t, path, AuthStorageData{
		"quiet-prov": {Type: CredentialTypeAPIKey, Key: "k"},
	})
	s := NewAuthStorage(path)

	s.mu.RLock()
	before := s.lastStamp
	s.mu.RUnlock()
	if before == "" {
		t.Fatal("file backend did not produce a stamp")
	}

	// A runtime override is process-local state a spurious reload must not
	// disturb, and doubles as proof we took the fast path.
	s.SetRuntimeApiKey("quiet-prov", "runtime")
	for i := 0; i < 3; i++ {
		if got := s.GetApiKey("quiet-prov"); got != "runtime" {
			t.Fatalf("key = %q, want runtime", got)
		}
	}
	s.mu.RLock()
	after := s.lastStamp
	s.mu.RUnlock()
	if before != after {
		t.Errorf("stamp changed without a write: %q -> %q", before, after)
	}
}

// TestOwnWriteDoesNotLookStale: after this process writes a credential, its
// own stamp must be adopted so the next lookup doesn't re-read needlessly.
func TestOwnWriteDoesNotLookStale(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	s := NewAuthStorage(path)

	if err := s.Set("self-prov", AuthCredential{Type: CredentialTypeAPIKey, Key: "k1"}); err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	stamp, live := s.lastStamp, s.backendStamp()
	s.mu.RUnlock()
	if stamp != live {
		t.Errorf("stamp not adopted after own write: %q vs on-disk %q", stamp, live)
	}
	if got := s.GetApiKey("self-prov"); got != "k1" {
		t.Errorf("key = %q, want k1", got)
	}
}
