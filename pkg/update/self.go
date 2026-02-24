package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

// SelfUpdate downloads the release binary for the current platform and
// atomically replaces the running executable.
//
// It tries a plain HTTPS download first (works for public repos).  If that
// fails it falls back to "gh release download" which handles authentication
// for private repos via the user's gh login session or SSH credentials.
func SelfUpdate(ctx context.Context, rel *Release) error {
	assetName := "fir-" + CurrentPlatform()

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current executable: %w", err)
	}

	// Download to a sibling temp file so the rename is on the same filesystem.
	tmpPath := exePath + ".new"

	// Try HTTPS download first (works for public repos, no auth needed).
	httpsOK := false
	if rel.AssetURL != "" {
		if err := downloadFile(ctx, rel.AssetURL, tmpPath); err == nil {
			httpsOK = true
		}
	}

	// Fall back to gh CLI (handles private repo auth via SSH/OAuth).
	if !httpsOK {
		if err := downloadViaGH(ctx, rel.Version, assetName, tmpPath); err != nil {
			_ = os.Remove(tmpPath)
			if HasGH() {
				return fmt.Errorf("download failed — run 'gh auth login' if the repo is private")
			}
			return fmt.Errorf("no pre-built binary available — for private repos, " +
				"install gh (https://cli.github.com) and run 'gh auth login'")
		}
	}

	// Verify checksum (only when we have the checksums file via HTTPS).
	if httpsOK && rel.ChecksumsURL != "" {
		if body, err := downloadText(ctx, rel.ChecksumsURL); err == nil {
			if expected := findChecksum(body, assetName); expected != "" {
				if err := verifySHA256(tmpPath, expected); err != nil {
					_ = os.Remove(tmpPath)
					return fmt.Errorf("checksum verification failed: %w", err)
				}
			}
		}
	}

	// Make the downloaded file executable.
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("chmod downloaded binary: %w", err)
	}

	// Atomic rename — replaces the running binary.
	if err := os.Rename(tmpPath, exePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace binary (try with sudo if permission denied): %w", err)
	}

	return nil
}

// downloadViaGH uses "gh release download" to fetch a single release asset.
// This handles all GitHub authentication (SSH, OAuth, token) automatically.
func downloadViaGH(ctx context.Context, tag, assetName, dest string) error {
	ghPath, err := exec.LookPath("gh")
	if err != nil {
		return fmt.Errorf("gh CLI not installed")
	}
	cmd := exec.CommandContext(ctx, ghPath, "release", "download", tag,
		"--repo", repoOwner+"/"+repoName,
		"--pattern", assetName,
		"--output", dest,
		"--clobber")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gh release download: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// downloadText fetches a URL and returns its body as a string.
func downloadText(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return string(data), err
}

// downloadFile downloads url and writes the body to dest.
func downloadFile(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// verifySHA256 checks that the file at path matches the expected hex digest.
func verifySHA256(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != expected {
		return fmt.Errorf("got %s, want %s", got, expected)
	}
	return nil
}

// findChecksum parses sha256sum-format text (lines: "<hash>  <filename>")
// and returns the hash for the given filename, or "" if not found.
func findChecksum(checksums, filename string) string {
	for _, line := range strings.Split(checksums, "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[1] == filename {
			return parts[0]
		}
	}
	return ""
}
