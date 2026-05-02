// Package update provides version checking and self-update for fir.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	selfupdate "github.com/creativeprojects/go-selfupdate"
	goversion "github.com/hashicorp/go-version"
)

const (
	// Binaries are distributed through a public mirror repo (kfet/fir-dist)
	// so update checks and downloads work without GitHub authentication,
	// regardless of the visibility of the source repo.
	repoOwner = "kfet"
	repoName  = "fir-dist"
	cacheTTL  = 24 * time.Hour
)

// Release holds information about a release for the current platform.
type Release struct {
	// Version is the release tag, e.g. "v0.5.0".
	Version string
	// inner is the underlying go-selfupdate release (nil for cache-only results).
	inner *selfupdate.Release
}

// cacheEntry is persisted to agentDir/update-check.json to limit API calls.
type cacheEntry struct {
	CheckedAt     time.Time `json:"checked_at"`
	LatestVersion string    `json:"latest_version"`
}

// newUpdater creates a go-selfupdate Updater configured for our asset naming.
// Our release assets are named "fir-{os}-{arch}" (raw binaries, no archive).
func newUpdater(source selfupdate.Source) (*selfupdate.Updater, error) {
	return selfupdate.NewUpdater(selfupdate.Config{
		Source: source,
	})
}

// newGitHubSource creates a GitHub source, optionally with a token.
func newGitHubSource(token string) (*selfupdate.GitHubSource, error) {
	return selfupdate.NewGitHubSource(selfupdate.GitHubConfig{
		APIToken: token,
	})
}

// repo returns the repository slug for our project.
func repo() selfupdate.RepositorySlug {
	return selfupdate.ParseSlug(repoOwner + "/" + repoName)
}

// CheckLatest returns the latest release if it is newer than currentVersion,
// using a 24-hour cache to avoid hammering the GitHub API.
//
// Returns (nil, nil) if the current version is up to date, if the check is
// skipped (dev build), or if the API call fails non-fatally.
// cacheDir is the directory where update-check.json is written (agentDir).
func CheckLatest(ctx context.Context, currentVersion, cacheDir string) (*Release, error) {
	if currentVersion == "" || currentVersion == "dev" {
		return nil, nil
	}

	cachePath := cacheDir + "/update-check.json"

	// Fast path: use cached result if still fresh.
	if entry, ok := readCache(cachePath); ok && time.Since(entry.CheckedAt) < cacheTTL {
		if !IsNewer(entry.LatestVersion, currentVersion) {
			return nil, nil
		}
		return &Release{Version: entry.LatestVersion}, nil
	}

	// Slow path: fetch from GitHub (no auth for background check).
	source, err := newGitHubSource("")
	if err != nil {
		return nil, err
	}
	updater, err := newUpdater(source)
	if err != nil {
		return nil, err
	}

	latest, found, err := updater.DetectLatest(ctx, repo())
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}

	version := latest.Version()

	// Update cache (best-effort).
	writeCache(cachePath, &cacheEntry{
		CheckedAt:     time.Now(),
		LatestVersion: version,
	})

	if !IsNewer(version, currentVersion) {
		return nil, nil
	}
	return &Release{Version: version, inner: latest}, nil
}

// FetchLatest fetches the latest release from the public distribution repo
// via HTTPS (no auth required).
func FetchLatest(ctx context.Context) (*Release, error) {
	source, err := newGitHubSource("")
	if err != nil {
		return nil, err
	}
	return fetchLatestWithSource(ctx, source)
}

func fetchLatestWithSource(ctx context.Context, source selfupdate.Source) (*Release, error) {
	updater, err := newUpdater(source)
	if err != nil {
		return nil, err
	}
	latest, found, err := updater.DetectLatest(ctx, repo())
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("no release found for %s/%s", repoOwner, repoName)
	}
	return &Release{Version: latest.Version(), inner: latest}, nil
}

// SelfUpdate downloads the release binary for the current platform and
// atomically replaces the running executable.
func SelfUpdate(ctx context.Context, rel *Release) error {
	if rel.inner == nil {
		// Re-fetch to get asset URLs (cache-only releases don't have them).
		fetched, err := FetchLatest(ctx)
		if err != nil {
			return err
		}
		rel = fetched
	}
	if rel.inner == nil {
		return fmt.Errorf("no release assets available")
	}

	source, err := newGitHubSource("")
	if err != nil {
		return err
	}
	updater, err := newUpdater(source)
	if err != nil {
		return err
	}

	exePath, err := selfupdate.ExecutablePath()
	if err != nil {
		return fmt.Errorf("locate current executable: %w", err)
	}

	if err := updater.UpdateTo(ctx, rel.inner, exePath); err != nil {
		return err
	}
	return nil
}

// UpdateNotice returns a one-line message when a newer version is available.
func UpdateNotice(newVersion string) string {
	return fmt.Sprintf("› fir %s available — run: fir update", newVersion)
}

// IsNewer reports whether candidate is strictly newer than current.
// Both strings are expected to be semver with an optional leading "v".
//
// Dev-build special case: if current has a prerelease segment starting with
// "dev" (e.g. "0.39.0-dev+abc"), the running binary is built from a commit
// after the v0.39.0 tag and is therefore considered AHEAD of the v0.39.0
// release. We compare candidate against current's core (major.minor.patch)
// and only report newer when the candidate's core is strictly greater.
// This prevents "fir v0.39.0 available" notices on a 0.39.0-dev+sha build.
func IsNewer(candidate, current string) bool {
	c, err := goversion.NewVersion(candidate)
	if err != nil {
		return false
	}
	cur, err := goversion.NewVersion(current)
	if err != nil {
		return false
	}
	if isDevPrerelease(cur) {
		return c.Core().GreaterThan(cur.Core())
	}
	return c.GreaterThan(cur)
}

// isDevPrerelease reports whether v's prerelease segment marks it as a dev
// build past a release tag (e.g. "0.39.0-dev+abc123" or "0.39.0-dev").
func isDevPrerelease(v *goversion.Version) bool {
	pre := v.Prerelease()
	return pre == "dev" || strings.HasPrefix(pre, "dev.") || strings.HasPrefix(pre, "dev-")
}

func readCache(path string) (*cacheEntry, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var e cacheEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, false
	}
	return &e, true
}

func writeCache(path string, e *cacheEntry) {
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o600)
}
