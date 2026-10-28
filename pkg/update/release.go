package update

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	goversion "github.com/hashicorp/go-version"
)

// apiBase is the GitHub REST API root; tests point it at an httptest server.
var apiBase = "https://api.github.com"

// asset is a downloadable release file.
type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	// sums is the URL of the release's checksums.txt, filled in by
	// pickRelease from the sibling assets.
	sums string
}

// checksumsName is the goreleaser checksum file fir-dist publishes.
const checksumsName = "checksums.txt"

type ghRelease struct {
	Tag        string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []asset `json:"assets"`
}

var reVersion = regexp.MustCompile(`\d+\.\d+\.\d+`)

func githubToken() string { return os.Getenv("GITHUB_TOKEN") }

// httpClient bounds every update request: callers' contexts may carry no
// deadline, and a stalled connection must not hang fir. Generous enough for
// a ~20 MB binary on a slow link.
var httpClient = &http.Client{Timeout: 5 * time.Minute}

func httpGet(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if tok := githubToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	return httpClient.Do(req)
}

// platformSuffixes lists the asset-name suffixes that identify the running
// platform: "<os>_<arch>" or "<os>-<arch>", per candidate arch, in
// preference order (GOARM-specific names first on 32-bit ARM).
func platformSuffixes(goos, goarch string, goarm int) []string {
	var arches []string
	if goarch == "arm" && goarm >= 5 && goarm <= 7 {
		for v := goarm; v >= 5; v-- {
			arches = append(arches, fmt.Sprintf("armv%d", v))
		}
		arches = append(arches, "arm")
	} else {
		arches = append(arches, goarch)
		if goarch == "amd64" {
			arches = append(arches, "x86_64")
		}
	}
	var out [][]string
	for _, arch := range arches {
		var group []string
		for _, sep := range []string{"_", "-"} {
			group = append(group, goos+sep+arch)
			if goos == "windows" {
				group = append(group, goos+sep+arch+".exe")
			}
		}
		out = append(out, group)
	}
	var flat []string
	for _, g := range out {
		flat = append(flat, g...)
	}
	return flat
}

func buildGOARM() int {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "GOARM" && s.Value != "" {
				return int(s.Value[0] - '0')
			}
		}
	}
	return 0
}

// pickRelease returns the newest non-draft, non-prerelease release that has
// an asset for the given platform, with its asset and normalized version.
func pickRelease(rels []ghRelease, goos, goarch string, goarm int) (*asset, string, bool) {
	var arches [][]string
	if goarch == "arm" && goarm >= 5 && goarm <= 7 {
		for v := goarm; v >= 5; v-- {
			arches = append(arches, platformSuffixes(goos, fmt.Sprintf("armv%d", v), 0))
		}
		arches = append(arches, platformSuffixes(goos, "arm", 0))
	} else {
		arches = append(arches, platformSuffixes(goos, goarch, 0))
		if goarch == "amd64" {
			arches = append(arches, platformSuffixes(goos, "x86_64", 0))
		}
	}
	for _, suffixes := range arches {
		var best *asset
		var bestV *goversion.Version
		for _, r := range rels {
			if r.Draft || r.Prerelease {
				continue
			}
			loc := reVersion.FindStringIndex(r.Tag)
			if loc == nil {
				continue
			}
			v, err := goversion.NewVersion(r.Tag[loc[0]:])
			if err != nil {
				continue
			}
			a := matchAsset(r.Assets, suffixes)
			if a == nil {
				continue
			}
			if best == nil || v.GreaterThan(bestV) {
				c := *a
				for _, o := range r.Assets {
					if o.Name == checksumsName {
						c.sums = o.URL
					}
				}
				best, bestV = &c, v
			}
		}
		if best != nil {
			return best, bestV.String(), true
		}
	}
	return nil, "", false
}

func matchAsset(assets []asset, suffixes []string) *asset {
	for i := range assets {
		for _, s := range []string{strings.ToLower(assets[i].Name), strings.ToLower(assets[i].URL)} {
			for _, suf := range suffixes {
				if strings.HasSuffix(s, suf) {
					return &assets[i]
				}
			}
		}
	}
	return nil
}

// detectLatest lists the repo's releases and returns the newest one that
// ships a binary for this platform. found is false when none does (or the
// repo has no releases).
func detectLatest(ctx context.Context, base, slug string) (*asset, string, bool, error) {
	resp, err := httpGet(ctx, base+"/repos/"+slug+"/releases")
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, "", false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", false, fmt.Errorf("list releases: %s", resp.Status)
	}
	var rels []ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rels); err != nil {
		return nil, "", false, fmt.Errorf("decode releases: %w", err)
	}
	a, v, ok := pickRelease(rels, runtime.GOOS, runtime.GOARCH, buildGOARM())
	return a, v, ok, nil
}

// executablePath returns the running executable with symlinks resolved.
func executablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// maxDownload caps any single download; fir binaries are ~20 MB.
var maxDownload int64 = 256 << 20

// fetch GETs url and returns the whole body, refusing anything over
// maxDownload so a broken or hostile endpoint cannot exhaust memory.
func fetch(ctx context.Context, url string) ([]byte, error) {
	resp, err := httpGet(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err == nil && int64(len(data)) > maxDownload {
		err = fmt.Errorf("download exceeds %d bytes", maxDownload)
	}
	return data, err
}

// expectedSum finds name's SHA-256 in a goreleaser checksums.txt
// ("<hex>  <name>" per line).
func expectedSum(sums []byte, name string) (string, bool) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && f[1] == name {
			return strings.ToLower(f[0]), true
		}
	}
	return "", false
}

// updateTo downloads the asset (a raw binary, as fir-dist publishes),
// checks it against the release's checksums.txt, and atomically replaces
// target, rolling back if the final rename fails. A download that does not
// match is never installed: a truncated or corrupt binary would brick fir.
// (The checksums ship in the same release, so this catches corruption, not
// a compromised release.)
func updateTo(ctx context.Context, a *asset, target string) error {
	if a.sums == "" {
		return fmt.Errorf("release has no %s; refusing to install unverified %q", checksumsName, a.Name)
	}
	sums, err := fetch(ctx, a.sums)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", checksumsName, err)
	}
	want, ok := expectedSum(sums, a.Name)
	if !ok {
		return fmt.Errorf("%s has no entry for %q", checksumsName, a.Name)
	}
	data, err := fetch(ctx, a.URL)
	if err != nil {
		return fmt.Errorf("failed to read asset %q: %w", a.Name, err)
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("checksum mismatch for %q: download corrupt or truncated", a.Name)
	}
	return replaceFile(target, data)
}

func replaceFile(target string, data []byte) error {
	dir, name := filepath.Dir(target), filepath.Base(target)
	newPath := filepath.Join(dir, "."+name+".new")
	oldPath := filepath.Join(dir, "."+name+".old")
	if err := os.WriteFile(newPath, data, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(newPath, 0o755); err != nil {
		os.Remove(newPath)
		return err
	}
	_ = os.Remove(oldPath)
	if err := os.Rename(target, oldPath); err != nil {
		os.Remove(newPath)
		return err
	}
	if err := os.Rename(newPath, target); err != nil {
		if rerr := os.Rename(oldPath, target); rerr != nil {
			return fmt.Errorf("%w (rollback failed: %v)", err, rerr)
		}
		os.Remove(newPath)
		return err
	}
	_ = os.Remove(oldPath)
	return nil
}
