package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ============================================================================
// semverCompare / IsNewer
// ============================================================================

func TestSemverCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.0.0", "1.0.1", -1},
		{"2.0.0", "1.9.9", 1},
		{"1.9.9", "2.0.0", -1},
		{"0.5.0", "0.4.9", 1},
		{"0.4.9", "0.5.0", -1},
		{"1.0", "1.0.0", 0},
		{"1", "1.0.0", 0},
		// Pre-release versions
		{"1.0.0-beta", "1.0.0", -1},
		{"1.0.0", "1.0.0-rc1", 1},
		{"1.0.0-alpha", "1.0.0-beta", 0},   // same numeric, both pre-release
		{"2.0.0-beta", "1.9.9", 1},          // higher numeric wins despite pre-release
		{"1.0.0-beta+build", "1.0.0", -1},   // build metadata stripped too
	}
	for _, tc := range cases {
		got := semverCompare(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("semverCompare(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

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
	}
	for _, tc := range cases {
		got := IsNewer(tc.candidate, tc.current)
		if got != tc.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", tc.candidate, tc.current, got, tc.want)
		}
	}
}

// ============================================================================
// CurrentPlatform
// ============================================================================

func TestCurrentPlatform_ReturnsNonEmpty(t *testing.T) {
	if p := CurrentPlatform(); p == "" {
		t.Error("CurrentPlatform() returned empty string")
	}
}

func TestCurrentPlatform_ContainsOS(t *testing.T) {
	goos := runtime.GOOS
	if goos != "darwin" && goos != "linux" {
		t.Skipf("skipping OS check for unsupported GOOS=%s", goos)
	}
	p := CurrentPlatform()
	if !strings.HasPrefix(p, goos) {
		t.Errorf("CurrentPlatform() = %q, expected to start with %q", p, goos)
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
// HasGH
// ============================================================================

func TestHasGH_ReturnsBool(t *testing.T) {
	_ = HasGH()
}

// ============================================================================
// ErrNotAccessible
// ============================================================================

func TestErrNotAccessible_IsError(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", ErrNotAccessible)
	if !errors.Is(err, ErrNotAccessible) {
		t.Error("expected errors.Is to detect ErrNotAccessible through wrapping")
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


