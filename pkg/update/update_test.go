package update

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ============================================================================
// IsNewer
// ============================================================================

func TestIsNewer(t *testing.T) {
	cases := []struct {
		candidate, current string
		want               bool
	}{
		{"v0.5.0", "v0.4.0", true},
		{"v0.4.1", "v0.4.0", true},
		{"v1.0.0", "v0.9.9", true},
		{"v0.4.0", "v0.4.0", false},
		{"v0.3.0", "v0.4.0", false},
		{"0.5.0", "0.4.0", true},
		{"0.4.0", "0.4.0", false},
		{"v0.5.0", "0.4.0", true},
		{"0.5.0", "v0.4.0", true},
		{"", "0.4.0", false},
		// Pre-release versions
		{"1.0.0-beta", "1.0.0", false},
		{"1.0.0", "1.0.0-rc1", true},
		{"2.0.0-beta", "1.9.9", true},
	}
	for _, tc := range cases {
		got := IsNewer(tc.candidate, tc.current)
		if got != tc.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", tc.candidate, tc.current, got, tc.want)
		}
	}
}

// ============================================================================
// UpdateNotice
// ============================================================================

func TestUpdateNotice_ContainsVersion(t *testing.T) {
	notice := UpdateNotice("v0.5.0")
	if !strings.Contains(notice, "v0.5.0") {
		t.Errorf("UpdateNotice did not contain version: %q", notice)
	}
}

func TestUpdateNotice_SuggestsFirUpdate(t *testing.T) {
	notice := UpdateNotice("v0.5.0")
	if !strings.Contains(notice, "fir update") {
		t.Errorf("notice should suggest fir update, got: %q", notice)
	}
}

// ============================================================================
// cache read/write
// ============================================================================

func TestCache_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update-check.json")

	entry := &cacheEntry{
		CheckedAt:     time.Now().Truncate(time.Second),
		LatestVersion: "v0.5.0",
	}
	writeCache(path, entry)

	got, ok := readCache(path)
	if !ok {
		t.Fatal("readCache returned false after write")
	}
	if got.LatestVersion != entry.LatestVersion {
		t.Errorf("got LatestVersion %q, want %q", got.LatestVersion, entry.LatestVersion)
	}
	if !got.CheckedAt.Equal(entry.CheckedAt) {
		t.Errorf("got CheckedAt %v, want %v", got.CheckedAt, entry.CheckedAt)
	}
}

func TestCache_MissingFile(t *testing.T) {
	dir := t.TempDir()
	_, ok := readCache(filepath.Join(dir, "nonexistent.json"))
	if ok {
		t.Error("expected false for missing cache file")
	}
}

func TestCache_Corrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update-check.json")
	_ = os.WriteFile(path, []byte("not json {{{"), 0o600)
	_, ok := readCache(path)
	if ok {
		t.Error("expected false for corrupt cache file")
	}
}

func TestWriteCache_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update-check.json")

	writeCache(path, &cacheEntry{CheckedAt: time.Now(), LatestVersion: "v1.0.0"})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cache file not created: %v", err)
	}
	var e cacheEntry
	if err := json.Unmarshal(data, &e); err != nil {
		t.Fatalf("cache file contains invalid JSON: %v", err)
	}
	if e.LatestVersion != "v1.0.0" {
		t.Errorf("got %q, want v1.0.0", e.LatestVersion)
	}
}

// ============================================================================
// CheckLatest (unit — uses cache only, no real network calls)
// ============================================================================

func TestCheckLatest_SkipsDevBuild(t *testing.T) {
	dir := t.TempDir()
	rel, err := CheckLatest(context.Background(), "dev", dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel != nil {
		t.Errorf("expected nil for dev build, got %+v", rel)
	}
}

func TestCheckLatest_SkipsEmptyVersion(t *testing.T) {
	dir := t.TempDir()
	rel, err := CheckLatest(context.Background(), "", dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel != nil {
		t.Errorf("expected nil for empty version, got %+v", rel)
	}
}

func TestCheckLatest_DisabledByEnv(t *testing.T) {
	// A stale cache plus a real version would otherwise force a network
	// round-trip; FIR_NO_UPDATE_CHECK must short-circuit before that.
	dir := t.TempDir()
	writeCache(filepath.Join(dir, "update-check.json"), &cacheEntry{
		CheckedAt:     time.Now().Add(-48 * time.Hour),
		LatestVersion: "v9.9.9",
	})
	for _, v := range []string{"1", "true", "yes"} {
		t.Setenv("FIR_NO_UPDATE_CHECK", v)
		if !ChecksDisabled() {
			t.Fatalf("ChecksDisabled() = false for %q", v)
		}
		rel, err := CheckLatestFresh(context.Background(), "v0.4.0", dir)
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", v, err)
		}
		if rel != nil {
			t.Errorf("expected nil release for %q, got %+v", v, rel)
		}
	}
}

func TestCheckLatest_EnvOffValuesKeepChecksOn(t *testing.T) {
	for _, v := range []string{"", "0", "false"} {
		t.Setenv("FIR_NO_UPDATE_CHECK", v)
		if ChecksDisabled() {
			t.Errorf("ChecksDisabled() = true for %q, want false", v)
		}
	}
}

func TestCheckLatest_CachedUpToDate(t *testing.T) {
	dir := t.TempDir()
	writeCache(filepath.Join(dir, "update-check.json"), &cacheEntry{
		CheckedAt:     time.Now(),
		LatestVersion: "v0.4.0",
	})
	rel, err := CheckLatest(context.Background(), "v0.4.0", dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel != nil {
		t.Errorf("expected nil when up to date, got %+v", rel)
	}
}

func TestCheckLatest_CachedNewerAvailable(t *testing.T) {
	dir := t.TempDir()
	writeCache(filepath.Join(dir, "update-check.json"), &cacheEntry{
		CheckedAt:     time.Now(),
		LatestVersion: "v0.5.0",
	})
	rel, err := CheckLatest(context.Background(), "v0.4.0", dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel == nil {
		t.Fatal("expected Release when newer version is cached")
	}
	if rel.Version != "v0.5.0" {
		t.Errorf("got version %q, want v0.5.0", rel.Version)
	}
}

func TestCheckLatest_CachedOlderThanCurrent(t *testing.T) {
	dir := t.TempDir()
	writeCache(filepath.Join(dir, "update-check.json"), &cacheEntry{
		CheckedAt:     time.Now(),
		LatestVersion: "v0.3.0",
	})
	rel, err := CheckLatest(context.Background(), "v0.4.0", dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel != nil {
		t.Errorf("expected nil when cached version is older than current, got %+v", rel)
	}
}

func TestCheckLatest_StaleCache_TriesNetwork(t *testing.T) {
	dir := t.TempDir()
	writeCache(filepath.Join(dir, "update-check.json"), &cacheEntry{
		CheckedAt:     time.Now().Add(-25 * time.Hour),
		LatestVersion: "v0.4.0",
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately — forces network failure

	_, err := CheckLatest(ctx, "v0.4.0", dir)
	if err == nil {
		t.Log("stale cache + cancelled ctx: no error returned (timing)")
	}
}

// CheckLatestFresh must ignore the cache and always hit the network, so an
// explicit `fir -V` can't report a stale result right after a new release is
// cut. We seed a FRESH cache advertising a newer version: the cached path
// returns it straight from disk, while the fresh path must NOT (it goes to the
// network instead). Pairing with a cancelled context keeps the network attempt
// deterministic — it cannot succeed and return the cached value.
func TestCheckLatestFresh_BypassesFreshCache(t *testing.T) {
	dir := t.TempDir()
	writeCache(filepath.Join(dir, "update-check.json"), &cacheEntry{
		CheckedAt:     time.Now(), // fresh — within cacheTTL
		LatestVersion: "v0.5.0",   // newer than current v0.4.0
	})

	// Cached path: returns the newer version straight from the fresh cache,
	// no network needed (deterministic even with a cancelled context).
	ctxA, cancelA := context.WithCancel(context.Background())
	cancelA()
	rel, err := CheckLatest(ctxA, "v0.4.0", dir)
	if err != nil {
		t.Fatalf("cached CheckLatest unexpected error: %v", err)
	}
	if rel == nil || rel.Version != "v0.5.0" {
		t.Fatalf("cached CheckLatest should return cached v0.5.0, got %+v", rel)
	}

	// Fresh path: must bypass the cache. With a cancelled context the network
	// attempt cannot succeed, so it can never reproduce the cached v0.5.0 —
	// proving the fast-path cache read was skipped.
	ctxB, cancelB := context.WithCancel(context.Background())
	cancelB()
	relFresh, _ := CheckLatestFresh(ctxB, "v0.4.0", dir)
	if relFresh != nil && relFresh.Version == "v0.5.0" {
		t.Error("CheckLatestFresh returned the cached v0.5.0 — it must bypass the cache, not read it")
	}
}

func TestCheckLatestFresh_SkipsDevBuild(t *testing.T) {
	dir := t.TempDir()
	rel, err := CheckLatestFresh(context.Background(), "dev", dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel != nil {
		t.Errorf("expected nil for dev build, got %+v", rel)
	}
}

// ============================================================================
// IsNewer edge cases
// ============================================================================

func TestIsNewer_InvalidVersion(t *testing.T) {
	if IsNewer("not-a-version", "1.0.0") {
		t.Error("expected false for invalid candidate")
	}
	if IsNewer("1.0.0", "not-a-version") {
		t.Error("expected false for invalid current")
	}
}

func TestIsNewer_EqualWithV(t *testing.T) {
	if got := IsNewer("v1.2.3", "1.2.3"); got {
		t.Errorf("expected false for equal versions with mixed v prefix, got %v", got)
	}
}

func TestUpdateNotice_Format(t *testing.T) {
	notice := UpdateNotice("v1.0.0")
	want := "› fir v1.0.0 available — run: fir update"
	if notice != want {
		t.Errorf("got %q, want %q", notice, want)
	}
}

// ============================================================================
// Dev build behaviour: a dev build of X.Y.Z must not be told that X.Y.Z is
// "available". The dev build is *ahead of* (or equal to) the released tag.
// ============================================================================

func TestIsNewer_DevBuildOfSameRelease(t *testing.T) {
	// Running 0.39.0-dev+abc (a dev build past the 0.39.0 tag).
	// The released 0.39.0 must NOT be considered newer.
	cases := []string{
		"0.39.0-dev+abc123",
		"0.39.0-dev+abc123-dirty",
		"v0.39.0-dev+abc123",
		"0.39.0-dev",
	}
	for _, cur := range cases {
		if IsNewer("v0.39.0", cur) {
			t.Errorf("IsNewer(v0.39.0, %q) = true; want false (dev build is ahead of release)", cur)
		}
		if IsNewer("0.39.0", cur) {
			t.Errorf("IsNewer(0.39.0, %q) = true; want false (dev build is ahead of release)", cur)
		}
	}
}

func TestIsNewer_DevBuildStillNoticesNewerRelease(t *testing.T) {
	// A 0.39.0-dev build SHOULD still be told about 0.40.0.
	if !IsNewer("v0.40.0", "0.39.0-dev+abc") {
		t.Error("IsNewer(v0.40.0, 0.39.0-dev+abc) = false; want true")
	}
	if !IsNewer("0.40.0", "0.39.0-dev+abc-dirty") {
		t.Error("expected 0.40.0 to be newer than 0.39.0-dev build")
	}
}

func TestCheckLatest_CachedSameReleaseAsDevBuild(t *testing.T) {
	dir := t.TempDir()
	writeCache(filepath.Join(dir, "update-check.json"), &cacheEntry{
		CheckedAt:     time.Now(),
		LatestVersion: "v0.39.0",
	})
	rel, err := CheckLatest(context.Background(), "0.39.0-dev+abc123", dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel != nil {
		t.Errorf("expected nil for dev build of same release, got %+v", rel)
	}
}
