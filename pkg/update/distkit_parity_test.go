package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kfet/distkit"
)

// releasePlatforms is every GOOS/GOARCH pair the release workflow publishes a
// fir binary for. Keep in step with .goreleaser.yaml.
var releasePlatforms = []struct{ goos, goarch string }{
	{"darwin", "amd64"},
	{"darwin", "arm64"},
	{"linux", "amd64"},
	{"linux", "arm64"},
	{"linux", "arm"},
}

// TestDistkitAssetNameParity is the gate on the whole bridge: distkit may only
// be wired in if the names it renders are exactly the names kfet/fir-dist
// actually publishes. It asks the real release for its asset list and compares.
//
// A failure here is an upstream distkit (or release-workflow) bug, not
// something to paper over by contorting the fir Config.
//
// The test skips rather than fails when GitHub is unreachable or the anonymous
// API budget — 60 requests/hour per IP, shared by a NAT'd fleet — is spent, so
// an offline `make all` stays green. FIR_PARITY_STRICT=1 turns those skips into
// failures, which is what a release check wants.
func TestDistkitAssetNameParity(t *testing.T) {
	strict := os.Getenv("FIR_PARITY_STRICT") != ""
	if testing.Short() && !strict {
		t.Skip("short mode: skipping network parity check")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tag, assets, err := latestReleaseAssets(ctx)
	if err != nil {
		if strict {
			t.Fatalf("cannot reach %s/%s releases API: %v", repoOwner, repoName, err)
		}
		t.Skipf("cannot reach %s/%s releases API: %v", repoOwner, repoName, err)
	}
	t.Logf("%s/%s latest = %s, assets = %v", repoOwner, repoName, tag, assets)

	published := make(map[string]bool, len(assets))
	for _, a := range assets {
		published[a] = true
	}

	cfg := DistkitConfig("v0.0.0")
	for _, p := range releasePlatforms {
		name := renderAssetName(cfg, tag, p.goos, p.goarch)
		if !published[name] {
			t.Errorf("distkit renders %q for %s/%s, but %s %s publishes no such asset",
				name, p.goos, p.goarch, repoName, tag)
		}
	}

	// The checksum manifest distkit verifies against must exist too: without
	// it every download would abort before the swap.
	if !published[cfg.ChecksumsAsset] {
		t.Errorf("distkit expects checksum manifest %q, not published in %s", cfg.ChecksumsAsset, tag)
	}
}

// renderAssetName renders the asset name for an arbitrary platform. distkit's
// own Config.AssetName is fixed to the running platform, so this mirrors its
// template substitution for the cross-platform sweep;
// TestDistkitAssetNameMatchesRuntime pins the two against each other.
func renderAssetName(cfg distkit.Config, tag, goos, goarch string) string {
	arch := goarch
	if arch == "arm" {
		arch = cfg.ArmSuffix
	}
	v := distkit.EnsureV(tag)
	return strings.NewReplacer(
		"{stem}", cfg.AssetStem,
		"{binary}", cfg.Binary,
		"{os}", goos,
		"{arch}", arch,
		"{version}", v,
		"{version_no_v}", strings.TrimPrefix(v, "v"),
	).Replace(cfg.AssetTemplate)
}

// TestDistkitAssetNameMatchesRuntime pins that the renderer above agrees with
// distkit's own AssetName on the platform the test runs on, so the
// cross-platform sweep is not silently testing a divergent copy.
func TestDistkitAssetNameMatchesRuntime(t *testing.T) {
	cfg := DistkitConfig("v1.2.3")
	got := cfg.AssetName("v1.2.3")
	want := renderAssetName(cfg, "v1.2.3", runtime.GOOS, runtime.GOARCH)
	if got != want {
		t.Fatalf("renderer drift: distkit says %q, test renderer says %q", got, want)
	}
}

// TestDistkitConfigIsFullySpecified pins the naming-relevant fields fir sets
// explicitly. fir renders asset names without going through distkit's internal
// normalisation, so a field left empty here would render as an empty token
// instead of silently picking up a default.
func TestDistkitConfigIsFullySpecified(t *testing.T) {
	cfg := DistkitConfig("v1.0.0")
	if cfg.Repo != repoOwner+"/"+repoName {
		t.Errorf("Repo = %q, want %s/%s", cfg.Repo, repoOwner, repoName)
	}
	for name, got := range map[string]string{
		"Binary":         cfg.Binary,
		"AssetStem":      cfg.AssetStem,
		"AssetTemplate":  cfg.AssetTemplate,
		"ChecksumsAsset": cfg.ChecksumsAsset,
		"ArmSuffix":      cfg.ArmSuffix,
		"Version":        cfg.Version,
	} {
		if got == "" {
			t.Errorf("Config.%s is empty; fir must set it explicitly", name)
		}
	}
}

// latestReleaseAssets returns the latest release tag of the distribution repo
// and the names of its assets, straight from the GitHub API.
func latestReleaseAssets(ctx context.Context) (string, []string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", repoOwner, repoName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	// A token lifts the 60/hour anonymous cap that a NAT'd fleet shares.
	if tok := distkit.DiscoverToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	var payload struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", nil, err
	}
	names := make([]string, 0, len(payload.Assets))
	for _, a := range payload.Assets {
		names = append(names, a.Name)
	}
	return payload.TagName, names, nil
}
