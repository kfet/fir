// Package update provides version checking and self-update for fir.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	repoOwner = "kfet"
	repoName  = "fir"
	apiURL    = "https://api.github.com/repos/" + repoOwner + "/" + repoName + "/releases/latest"
	cacheTTL  = 24 * time.Hour
)

// ErrNotAccessible indicates the GitHub API returned 401/403/404, typically
// because the repository is private and no credentials were provided.
var ErrNotAccessible = errors.New("GitHub releases not accessible (private repo?)")

// Release holds information about a GitHub release asset for the current platform.
type Release struct {
	// Version is the release tag, e.g. "v0.5.0".
	Version string
	// AssetURL is the direct download URL for this platform's binary.
	// Empty if no matching asset was found in the release.
	AssetURL string
	// ChecksumsURL is the URL for the checksums.txt asset (may be empty).
	ChecksumsURL string
}

// cacheEntry is persisted to agentDir/update-check.json to limit API calls.
type cacheEntry struct {
	CheckedAt     time.Time `json:"checked_at"`
	LatestVersion string    `json:"latest_version"`
}

// CheckLatest returns the latest release if it is newer than currentVersion,
// using a 24-hour cache to avoid hammering the GitHub API.
//
// Uses HTTPS only (no gh fallback) to keep the background check lightweight.
// Returns (nil, nil) if the current version is up to date, if the check is
// skipped (dev build), or if the API call fails non-fatally.
// cacheDir is the directory where update-check.json is written (agentDir).
func CheckLatest(ctx context.Context, currentVersion, cacheDir string) (*Release, error) {
	// Skip check for dev builds — version is meaningless.
	if currentVersion == "" || currentVersion == "dev" {
		return nil, nil
	}

	cachePath := cacheDir + "/update-check.json"

	// Fast path: use cached result if still fresh.
	if entry, ok := readCache(cachePath); ok && time.Since(entry.CheckedAt) < cacheTTL {
		if !isNewer(entry.LatestVersion, currentVersion) {
			return nil, nil
		}
		return &Release{Version: entry.LatestVersion}, nil
	}

	// Slow path: fetch from GitHub.
	rel, err := FetchLatest(ctx)
	if err != nil {
		return nil, err
	}

	// Update cache (best-effort).
	writeCache(cachePath, &cacheEntry{
		CheckedAt:     time.Now(),
		LatestVersion: rel.Version,
	})

	if !isNewer(rel.Version, currentVersion) {
		return nil, nil
	}
	return rel, nil
}

// releaseJSON is the subset of the GitHub Releases API response we need.
type releaseJSON struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// parseRelease converts raw JSON from the GitHub Releases API into a Release.
func parseRelease(data []byte) (*Release, error) {
	var r releaseJSON
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parse release JSON: %w", err)
	}
	rel := &Release{Version: r.TagName}
	want := "fir-" + CurrentPlatform()
	for _, a := range r.Assets {
		switch a.Name {
		case want:
			rel.AssetURL = a.BrowserDownloadURL
		case "checksums.txt":
			rel.ChecksumsURL = a.BrowserDownloadURL
		}
	}
	return rel, nil
}

// FetchLatest fetches the latest release from GitHub via HTTPS (no auth).
// Returns ErrNotAccessible if the repo appears private (401/403/404).
func FetchLatest(ctx context.Context) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "fir-update-check/1")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// success — parse below
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return nil, ErrNotAccessible
	default:
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return parseRelease(data)
}

// FetchLatestOrGH fetches the latest release, trying plain HTTPS first and
// falling back to the gh CLI for private repos.  This is used by "fir update"
// where we want to try harder than the background check.
func FetchLatestOrGH(ctx context.Context) (*Release, error) {
	rel, err := FetchLatest(ctx)
	if err == nil {
		return rel, nil
	}
	if !errors.Is(err, ErrNotAccessible) {
		return nil, err // network error, not auth
	}
	// Repo appears private — try gh CLI.
	return fetchViaGH(ctx)
}

// fetchViaGH uses the gh CLI (which handles SSH/OAuth auth) to query the
// GitHub Releases API.  Returns an error if gh is not installed or not
// authenticated.
func fetchViaGH(ctx context.Context) (*Release, error) {
	ghPath, err := exec.LookPath("gh")
	if err != nil {
		return nil, fmt.Errorf("repo is private and gh CLI is not installed — " +
			"install gh (https://cli.github.com) and run 'gh auth login'")
	}
	cmd := exec.CommandContext(ctx, ghPath, "api",
		fmt.Sprintf("repos/%s/%s/releases/latest", repoOwner, repoName))
	// Prevent gh from colorizing JSON output. CLICOLOR_FORCE (set for bash
	// tool display) overrides NO_COLOR in gh, so we must clear it explicitly.
	cmd.Env = filterEnv(os.Environ(), "CLICOLOR", "CLICOLOR_FORCE", "FORCE_COLOR")
	cmd.Env = append(cmd.Env, "NO_COLOR=1")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh api failed (run 'gh auth login'?): %w", err)
	}
	return parseRelease(out)
}

// HasGH reports whether the gh CLI is on the PATH.
func HasGH() bool {
	_, err := exec.LookPath("gh")
	return err == nil
}

// CurrentPlatform returns the platform suffix used in release asset names,
// matching the naming convention in the Makefile (e.g. "darwin-arm64",
// "linux-arm6", "linux-amd64").
func CurrentPlatform() string {
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	switch {
	case goos == "darwin" && goarch == "arm64":
		return "darwin-arm64"
	case goos == "darwin" && goarch == "amd64":
		return "darwin-amd64"
	case goos == "linux" && goarch == "arm64":
		return "linux-arm64"
	case goos == "linux" && goarch == "arm":
		return "linux-arm6"
	case goos == "linux" && goarch == "amd64":
		return "linux-amd64"
	default:
		return goos + "-" + goarch
	}
}

// UpdateNotice returns a one-line message to print when a newer version is
// available, suggesting "fir update".
func UpdateNotice(newVersion string) string {
	return fmt.Sprintf("› fir %s available — run: fir update", newVersion)
}

// IsNewer reports whether candidate is strictly newer than current.
// Both strings are expected to be semver with an optional leading "v".
func IsNewer(candidate, current string) bool {
	return isNewer(candidate, current)
}

func isNewer(candidate, current string) bool {
	c := trimV(candidate)
	cur := trimV(current)
	return c != "" && c != cur && semverCompare(c, cur) > 0
}

func trimV(s string) string { return strings.TrimPrefix(s, "v") }

// semverCompare compares two "major.minor.patch[-pre]" version strings.
// Returns 1 if a > b, -1 if a < b, 0 if equal.
// Pre-release versions (e.g., "1.0.0-beta") are considered less than
// the corresponding release ("1.0.0"), per semver spec.
func semverCompare(a, b string) int {
	aPre := ""
	bPre := ""
	if idx := strings.IndexAny(a, "-+"); idx >= 0 {
		aPre = a[idx:]
		a = a[:idx]
	}
	if idx := strings.IndexAny(b, "-+"); idx >= 0 {
		bPre = b[idx:]
		b = b[:idx]
	}
	aParts := strings.SplitN(a, ".", 3)
	bParts := strings.SplitN(b, ".", 3)
	for i := 0; i < 3; i++ {
		av, bv := 0, 0
		if i < len(aParts) {
			av, _ = strconv.Atoi(aParts[i])
		}
		if i < len(bParts) {
			bv, _ = strconv.Atoi(bParts[i])
		}
		if av != bv {
			if av > bv {
				return 1
			}
			return -1
		}
	}
	// Numeric parts equal; a pre-release version is less than a release.
	switch {
	case aPre != "" && bPre == "":
		return -1
	case aPre == "" && bPre != "":
		return 1
	}
	return 0
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

// filterEnv returns env with entries matching any of the given prefixes removed.
func filterEnv(env []string, prefixes ...string) []string {
	filtered := make([]string, 0, len(env))
	for _, e := range env {
		skip := false
		for _, p := range prefixes {
			if strings.HasPrefix(e, p+"=") {
				skip = true
				break
			}
		}
		if !skip {
			filtered = append(filtered, e)
		}
	}
	return filtered
}
