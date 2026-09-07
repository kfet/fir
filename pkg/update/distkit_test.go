package update

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeDist stands in for the download host: it 302s /<repo>/releases/latest
// to the release page, exactly as github.com does, which is the anonymous
// resolution path distkit takes for the dormant check.
func fakeDist(t *testing.T, tag string) *httptest.Server {
	t.Helper()
	repo := repoOwner + "/" + repoName
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+repo+"/releases/latest" {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		http.Redirect(w, r, "/"+repo+"/releases/tag/"+tag, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// useFakeDist points the dormant resolver at srv for the duration of a test.
// The API base is pointed at a server that fails the test if it is called:
// the background check must never spend the 60/hour anonymous API budget a
// NAT'd fleet shares between its hosts.
func useFakeDist(t *testing.T, srv *httptest.Server) {
	t.Helper()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("dormant check must not call the GitHub API, but requested %s", r.URL.Path)
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	t.Cleanup(dead.Close)

	oldAPI, oldDL := distkitAPIBase, distkitDownloadBase
	distkitAPIBase, distkitDownloadBase = dead.URL, srv.URL
	t.Cleanup(func() { distkitAPIBase, distkitDownloadBase = oldAPI, oldDL })
}

func TestDistkitResolveAnonymously(t *testing.T) {
	useFakeDist(t, fakeDist(t, "v9.9.9"))

	got, err := DistkitResolve(context.Background(), "v1.0.0", true)
	if err != nil {
		t.Fatalf("DistkitResolve: %v", err)
	}
	if got != "v9.9.9" {
		t.Fatalf("resolved %q, want v9.9.9", got)
	}
}

// fakeAPI answers the releases API with a single release. `fir update -check`
// resolves WITH token discovery — it must behave like the `fir update` it
// advertises — so on a host with `gh` logged in it takes the API path, and the
// test has to serve both.
func fakeAPI(t *testing.T, tag string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/"+repoOwner+"/"+repoName+"/releases/latest" {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": tag})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func useFakeHosts(t *testing.T, tag string) {
	t.Helper()
	oldAPI, oldDL := distkitAPIBase, distkitDownloadBase
	distkitAPIBase, distkitDownloadBase = fakeAPI(t, tag).URL, fakeDist(t, tag).URL
	t.Cleanup(func() { distkitAPIBase, distkitDownloadBase = oldAPI, oldDL })
}

func TestDistkitCheckReportsAvailability(t *testing.T) {
	useFakeHosts(t, "v2.0.0")

	tests := []struct {
		name      string
		current   string
		available bool
	}{
		{"older", "v1.0.0", true},
		{"same", "v2.0.0", false},
		{"newer", "v3.0.0", false},
		// A dev build sits after its tag, so v2.0.0 is not an update.
		{"dev build past the tag", "v2.0.0-dev", false},
		{"placeholder dev version", "dev", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rep, err := DistkitCheck(context.Background(), tc.current)
			if err != nil {
				t.Fatalf("DistkitCheck: %v", err)
			}
			if rep.Target != "v2.0.0" {
				t.Errorf("Target = %q, want v2.0.0", rep.Target)
			}
			if rep.Available != tc.available {
				t.Errorf("Available = %v, want %v (current %s)", rep.Available, tc.available, tc.current)
			}
			// A working-tree build must be reported as such: "up to
			// date" would claim it is running a release it is not.
			if wantDev := tc.current == "v2.0.0-dev" || tc.current == "dev"; rep.Dev != wantDev {
				t.Errorf("Dev = %v, want %v (current %s)", rep.Dev, wantDev, tc.current)
			}
			if rep.Dev && rep.Current != tc.current {
				t.Errorf("Current = %q, want the version as built (%q)", rep.Current, tc.current)
			}
		})
	}
}

// The shadow resolver runs alongside the authoritative one and reports what
// should be cached. These are the three outcomes the fleet can produce.
func TestShadowResolveResult(t *testing.T) {
	tests := []struct {
		name          string
		out           shadowOutcome
		authoritative string
		wantVersion   string
		wantErr       string
	}{
		{
			name:          "agreement",
			out:           shadowOutcome{version: "v1.7.1"},
			authoritative: "1.7.1", // go-selfupdate drops the leading v
			wantVersion:   "v1.7.1",
		},
		{
			name:          "disagreement is recorded, not acted on",
			out:           shadowOutcome{version: "v1.7.0"},
			authoritative: "1.7.1",
			wantVersion:   "v1.7.0",
		},
		{
			name:          "resolver error",
			out:           shadowOutcome{err: errors.New("boom")},
			authoritative: "1.7.1",
			wantErr:       "boom",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ch := make(chan shadowOutcome, 1)
			ch <- tc.out
			gotV, gotErr := shadowResolve{ch: ch}.result(tc.authoritative)
			if gotV != tc.wantVersion || gotErr != tc.wantErr {
				t.Fatalf("got (%q, %q), want (%q, %q)", gotV, gotErr, tc.wantVersion, tc.wantErr)
			}
		})
	}
}

// A caller that abandons the shadow — because the authoritative check failed
// — must not leak the goroutine that is still resolving.
func TestStartShadowResolveDoesNotBlockOnAnAbandonedCaller(t *testing.T) {
	useFakeDist(t, fakeDist(t, "v1.0.0"))

	s := startShadowResolve(context.Background(), "v1.0.0")
	select {
	case out := <-s.ch:
		if out.err != nil {
			t.Fatalf("shadow resolve: %v", out.err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("shadow resolve never reported")
	}
}

func TestLastDisagreement(t *testing.T) {
	when := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		entry cacheEntry
		want  string // substring; "" means no note at all
	}{
		{"no cached shadow", cacheEntry{LatestVersion: "v1.7.1"}, ""},
		{"agreement", cacheEntry{LatestVersion: "1.7.1", DistkitVersion: "v1.7.1"}, ""},
		{"disagreement", cacheEntry{LatestVersion: "1.7.1", DistkitVersion: "v1.7.0"}, "resolvers disagreed"},
		{"shadow failed", cacheEntry{LatestVersion: "1.7.1", DistkitError: "boom"}, "boom"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.entry.CheckedAt = when
			data, err := json.Marshal(tc.entry)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "update-check.json"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			got := LastDisagreement(dir)
			switch {
			case tc.want == "" && got != "":
				t.Fatalf("want no note, got %q", got)
			case tc.want != "" && !strings.Contains(got, tc.want):
				t.Fatalf("note %q does not mention %q", got, tc.want)
			}
		})
	}

	if got := LastDisagreement(t.TempDir()); got != "" {
		t.Fatalf("missing cache should yield no note, got %q", got)
	}
}

// The cache is fir's, not distkit's: a CLI-oriented distribution library has
// no business owning a state file in another program's agent directory. This
// pins the shadow fields round-trip through it.
func TestCacheCarriesShadowResult(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update-check.json")
	writeCache(path, &cacheEntry{
		CheckedAt:      time.Now(),
		LatestVersion:  "1.7.1",
		DistkitVersion: "v1.7.1",
	})
	got, ok := readCache(path)
	if !ok {
		t.Fatal("cache not readable")
	}
	if got.DistkitVersion != "v1.7.1" || got.LatestVersion != "1.7.1" {
		t.Fatalf("round-trip lost data: %+v", got)
	}
}
