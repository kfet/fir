// Package selfupdate checks for and applies updates to the poe-bridge
// binary from GitHub Releases tagged poe-bridge/vX.Y.Z.
package selfupdate

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"strings"

	"github.com/creativeprojects/go-selfupdate"
)

const (
	// Repo is the GitHub owner/repo where releases are published.
	Repo = "kfet/fir"
	// TagPrefix is prepended to the semver tag for this binary.
	TagPrefix = "poe-bridge/v"
)

// CheckResult holds the outcome of an update check.
type CheckResult struct {
	Current   string // running version
	Latest    string // latest release version (empty if up to date)
	UpdateURL string // download URL for the asset
	Available bool
}

// Check queries GitHub for the latest poe-bridge release and compares
// it to the running version. Returns Available=true if a newer version
// exists.
func Check(ctx context.Context, currentVersion string) (*CheckResult, error) {
	source, err := selfupdate.NewGitHubSource(selfupdate.GitHubConfig{})
	if err != nil {
		return nil, fmt.Errorf("selfupdate: github source: %w", err)
	}

	updater, err := selfupdate.NewUpdater(selfupdate.Config{
		Source:  source,
		Filters: []string{fmt.Sprintf("poe-bridge-%s-%s$", runtime.GOOS, mapArch())},
	})
	if err != nil {
		return nil, fmt.Errorf("selfupdate: updater: %w", err)
	}

	latest, found, err := updater.DetectLatest(ctx, selfupdate.NewRepositorySlug("kfet", "fir"))
	if err != nil {
		return nil, fmt.Errorf("selfupdate: detect: %w", err)
	}

	res := &CheckResult{Current: currentVersion}
	if !found {
		return res, nil
	}

	// Strip tag prefix for comparison.
	latestVer := strings.TrimPrefix(latest.Version(), TagPrefix)
	latestVer = strings.TrimPrefix(latestVer, "v")
	currentClean := strings.TrimPrefix(currentVersion, "v")

	if latestVer != currentClean && found {
		res.Latest = latestVer
		res.Available = true
		res.UpdateURL = latest.AssetURL
	}
	return res, nil
}

// Apply downloads and replaces the running binary with the latest release.
// Returns the new version string.
func Apply(ctx context.Context, currentVersion string) (newVersion string, err error) {
	source, err := selfupdate.NewGitHubSource(selfupdate.GitHubConfig{})
	if err != nil {
		return "", fmt.Errorf("selfupdate: github source: %w", err)
	}

	updater, err := selfupdate.NewUpdater(selfupdate.Config{
		Source:  source,
		Filters: []string{fmt.Sprintf("poe-bridge-%s-%s$", runtime.GOOS, mapArch())},
	})
	if err != nil {
		return "", fmt.Errorf("selfupdate: updater: %w", err)
	}

	latest, err := updater.UpdateSelf(ctx, currentVersion, selfupdate.NewRepositorySlug("kfet", "fir"))
	if err != nil {
		return "", fmt.Errorf("selfupdate: apply: %w", err)
	}
	return latest.Version(), nil
}

// LogIfAvailable runs Check in the background and logs if an update is
// available. Intended to be called from main() at startup as a goroutine.
func LogIfAvailable(ctx context.Context, currentVersion string) {
	res, err := Check(ctx, currentVersion)
	if err != nil {
		log.Printf("selfupdate: check failed: %v", err)
		return
	}
	if res.Available {
		log.Printf("selfupdate: poe-bridge %s available (running %s). Run with --self-update to upgrade.", res.Latest, res.Current)
	}
}

func mapArch() string {
	switch runtime.GOARCH {
	case "arm":
		return "armv6"
	default:
		return runtime.GOARCH
	}
}
