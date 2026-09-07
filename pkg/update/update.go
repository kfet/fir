// Package update provides version checking and self-update for fir.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	selfupdate "github.com/creativeprojects/go-selfupdate"
	goversion "github.com/hashicorp/go-version"
)

const (
	// Binaries are distributed through a public mirror repo (kfet/fir-dist).
	//
	// This started as a workaround for kfet/fir being private. It is not
	// that any more — fir is public — and the mirror is kept for a better
	// reason: fir-dist is the STABLE DISTRIBUTION ENDPOINT, baked into
	// every binary ever shipped (here, and as the catalog URL in
	// pkg/models). The installed base can only be redirected THROUGH this
	// endpoint, so it cannot be retired without dual-publishing for as
	// long as any old binary exists. It also decouples distribution from
	// the source repo, which can then be renamed or restructured freely.
	//
	// Do not rename kfet/fir-dist. Shipped binaries resolve it by name.
	repoOwner = "kfet"
	repoName  = "fir-dist"
	cacheTTL  = 24 * time.Hour
)

// errNoRelease is what a check that reached GitHub but found no release
// reports to the dormant resolver, which has no error of its own to log.
var errNoRelease = errors.New("no release found")

// Release holds information about a release for the current platform.
type Release struct {
	// Version is the release tag, e.g. "v0.5.0".
	Version string
	// inner is the underlying go-selfupdate release (nil for cache-only results).
	inner *selfupdate.Release
}

// cacheEntry is persisted to agentDir/update-check.json to limit API calls.
//
// The cache lives here, not in distkit: a CLI-oriented distribution library
// should not own a state file in another program's agent directory.
type cacheEntry struct {
	CheckedAt     time.Time `json:"checked_at"`
	LatestVersion string    `json:"latest_version"`
	// DistkitVersion is what the dormant distkit resolver returned for the
	// same check, and DistkitError what it failed with. They are recorded
	// so a fleet sweep can grep update-check.json for disagreement without
	// needing debug logging to have been enabled at the right moment.
	DistkitVersion string `json:"distkit_version,omitempty"`
	DistkitError   string `json:"distkit_error,omitempty"`
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
	return checkLatest(ctx, currentVersion, cacheDir, false)
}

// CheckLatestFresh is like CheckLatest but skips the cache read and always
// queries GitHub for the latest release. Use it for explicit, user-initiated
// checks (e.g. `fir -V`) where a stale cache must not mask a just-published
// release. It still refreshes the cache on success.
func CheckLatestFresh(ctx context.Context, currentVersion, cacheDir string) (*Release, error) {
	return checkLatest(ctx, currentVersion, cacheDir, true)
}

func checkLatest(ctx context.Context, currentVersion, cacheDir string, forceRefresh bool) (*Release, error) {
	if currentVersion == "" || currentVersion == "dev" {
		return nil, nil
	}

	cachePath := cacheDir + "/update-check.json"

	// Fast path: use cached result if still fresh. Skipped on a forced refresh
	// so explicit checks never report a stale "up to date" result.
	if !forceRefresh {
		if entry, ok := readCache(cachePath); ok && time.Since(entry.CheckedAt) < cacheTTL {
			if !IsNewer(entry.LatestVersion, currentVersion) {
				return nil, nil
			}
			return &Release{Version: entry.LatestVersion}, nil
		}
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

	// The dormant distkit resolver runs CONCURRENTLY with go-selfupdate, so
	// the shadow costs latency only when it is the slower of the two, not
	// on top of the authoritative check.
	shadowCh := startShadowResolve(ctx, currentVersion)

	latest, found, err := updater.DetectLatest(ctx, repo())
	if err != nil {
		shadowCh.logPrimaryFailure(err)
		return nil, err
	}
	if !found {
		shadowCh.logPrimaryFailure(errNoRelease)
		return nil, nil
	}

	version := latest.Version()

	// Persist the authoritative answer straight away, and never block it on
	// the dormant resolver: a distkit call that hangs until the context
	// deadline must not delay the update notice the user actually sees.
	// The shadow rewrites this entry when it lands, and if the process exits
	// first the next check simply asks again.
	checkedAt := time.Now()
	writeCache(cachePath, &cacheEntry{CheckedAt: checkedAt, LatestVersion: version})
	shadowCh.recordAsync(cachePath, version, checkedAt)

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
// atomically replaces the running executable. It returns the path of the
// replaced executable (symlinks resolved), which is the path a caller must
// use to restart into the new binary.
//
// The path matters: the underlying updater renames the running binary to
// <dir>/.<name>.old before moving the new one into place and then deletes
// it, so after this call os.Executable() (i.e. /proc/self/exe) points at a
// deleted file. Restarting must use the returned path, not os.Executable().
func SelfUpdate(ctx context.Context, rel *Release) (string, error) {
	if rel.inner == nil {
		// Re-fetch to get asset URLs (cache-only releases don't have them).
		fetched, err := FetchLatest(ctx)
		if err != nil {
			return "", err
		}
		rel = fetched
	}
	if rel.inner == nil {
		return "", fmt.Errorf("no release assets available")
	}

	source, err := newGitHubSource("")
	if err != nil {
		return "", err
	}
	updater, err := newUpdater(source)
	if err != nil {
		return "", err
	}

	exePath, err := selfupdate.ExecutablePath()
	if err != nil {
		return "", fmt.Errorf("locate current executable: %w", err)
	}

	if err := updater.UpdateTo(ctx, rel.inner, exePath); err != nil {
		return "", err
	}
	return exePath, nil
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

// writeCache persists the entry atomically: the file is now written twice per
// check (the authoritative answer immediately, then again when the dormant
// resolver lands), and a concurrent fir process reading a half-written file
// would throw away a perfectly good check. Rename over the target is atomic on
// the same filesystem, so a reader sees either the old entry or the new one.
func writeCache(path string, e *cacheEntry) {
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".update-check-*.json")
	if err != nil {
		return
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op once the rename succeeds
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(name, path)
}
