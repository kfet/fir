package update

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func fakeServer(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	plat := runtime.GOOS + "-" + runtime.GOARCH
	mux.HandleFunc("/repos/kfet/fir-dist/releases", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[
{"tag_name":"v9.9.9","prerelease":true,"assets":[{"name":"fir-%[2]s","browser_download_url":"%[1]s/dl/pre"}]},
{"tag_name":"v0.9.0","assets":[{"name":"fir-other-os","browser_download_url":"%[1]s/dl/x"},{"name":"fir-%[2]s","browser_download_url":"%[1]s/dl/old"}]},
{"tag_name":"v1.2.3","assets":[{"name":"checksums.txt","browser_download_url":"%[1]s/dl/c"},{"name":"fir-%[2]s","browser_download_url":"%[1]s/dl/new"}]},
{"tag_name":"v2.0.0","draft":true,"assets":[{"name":"fir-%[2]s","browser_download_url":"%[1]s/dl/d"}]}
]`, srv.URL, plat)
	})
	mux.HandleFunc("/dl/new", func(w http.ResponseWriter, r *http.Request) { w.Write(body) })
	mux.HandleFunc("/dl/tampered", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("NEWBI")) })
	mux.HandleFunc("/dl/c", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%x  fir-other-os\n%x  fir-%s\n", sha256.Sum256(nil), sha256.Sum256(body), plat)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestDetectLatestPicksNewestStable(t *testing.T) {
	srv := fakeServer(t, nil)
	a, v, ok, err := detectLatest(context.Background(), srv.URL, "kfet/fir-dist")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if v != "1.2.3" || a.URL != srv.URL+"/dl/new" {
		t.Fatalf("got %q %q", v, a.URL)
	}
}

func TestDetectLatestNotFound(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	_, _, ok, err := detectLatest(context.Background(), srv.URL, "a/b")
	if ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer srv2.Close()
	if _, _, _, err := detectLatest(context.Background(), srv2.URL, "a/b"); err == nil {
		t.Fatal("want error on 500")
	}
}

func TestPickReleaseArch(t *testing.T) {
	rels := []ghRelease{{Tag: "v1.0.0", Assets: []asset{{Name: "fir-linux-armv6"}, {Name: "fir-linux-armv7"}}}}
	a, _, ok := pickRelease(rels, "linux", "arm", 7)
	if !ok || a.Name != "fir-linux-armv7" {
		t.Fatalf("%v %v", a, ok)
	}
	a, _, ok = pickRelease(rels, "linux", "arm", 6)
	if !ok || a.Name != "fir-linux-armv6" {
		t.Fatalf("%v %v", a, ok)
	}
	if _, _, ok = pickRelease(rels, "linux", "amd64", 0); ok {
		t.Fatal("amd64 should not match")
	}
	rels = []ghRelease{{Tag: "fir-1.0.0", Assets: []asset{{Name: "fir_darwin_x86_64"}}}}
	if _, v, ok := pickRelease(rels, "darwin", "amd64", 0); !ok || v != "1.0.0" {
		t.Fatalf("%v %v", v, ok)
	}
}

func TestUpdateToReplacesBinary(t *testing.T) {
	srv := fakeServer(t, []byte("NEWBIN"))
	a, _, _, err := detectLatest(context.Background(), srv.URL, "kfet/fir-dist")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "fir")
	os.WriteFile(target, []byte("OLD"), 0o755)
	if err := updateTo(context.Background(), a, target); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "NEWBIN" {
		t.Fatalf("got %q", got)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("leftover files: %v", ents)
	}
	if fi, _ := os.Stat(target); fi.Mode()&0o111 == 0 {
		t.Fatal("not executable")
	}
}

func TestUpdateToHTTPErrorKeepsOld(t *testing.T) {
	srv := fakeServer(t, nil)
	dir := t.TempDir()
	target := filepath.Join(dir, "fir")
	os.WriteFile(target, []byte("OLD"), 0o755)
	plat := "fir-" + runtime.GOOS + "-" + runtime.GOARCH
	for name, a := range map[string]*asset{
		"asset 404":     {Name: plat, URL: srv.URL + "/missing", sums: srv.URL + "/dl/c"},
		"no sums":       {Name: plat, URL: srv.URL + "/dl/new"},
		"sums 404":      {Name: plat, URL: srv.URL + "/dl/new", sums: srv.URL + "/missing"},
		"sums no entry": {Name: "fir-nowhere", URL: srv.URL + "/dl/new", sums: srv.URL + "/dl/c"},
		"bad url":       {Name: plat, URL: srv.URL + "/dl/new", sums: "::bad"},
	} {
		if err := updateTo(context.Background(), a, target); err == nil {
			t.Fatalf("%s: want error", name)
		}
		if got, _ := os.ReadFile(target); string(got) != "OLD" {
			t.Fatalf("%s: got %q", name, got)
		}
	}
}

// TestUpdateToChecksumMismatchKeepsOld serves a body that does not match
// checksums.txt (a truncated or tampered download): nothing is installed.
func TestUpdateToChecksumMismatchKeepsOld(t *testing.T) {
	srv := fakeServer(t, []byte("NEWBIN"))
	a, _, _, err := detectLatest(context.Background(), srv.URL, "kfet/fir-dist")
	if err != nil {
		t.Fatal(err)
	}
	a.URL = srv.URL + "/dl/tampered"
	dir := t.TempDir()
	target := filepath.Join(dir, "fir")
	os.WriteFile(target, []byte("OLD"), 0o755)
	if err := updateTo(context.Background(), a, target); err == nil {
		t.Fatal("want checksum error")
	}
	if got, _ := os.ReadFile(target); string(got) != "OLD" {
		t.Fatalf("got %q", got)
	}
}

func TestReplaceFileRollback(t *testing.T) {
	dir := t.TempDir()
	// target missing: first rename fails, nothing left behind.
	if err := replaceFile(filepath.Join(dir, "nope"), []byte("x")); err == nil {
		t.Fatal("want error")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("leftovers: %v", ents)
	}
}

func TestFetchLatestFrom(t *testing.T) {
	srv := fakeServer(t, nil)
	rel, err := fetchLatestFrom(context.Background(), srv.URL)
	if err != nil || rel.Version != "1.2.3" || rel.inner == nil {
		t.Fatalf("%v %v", rel, err)
	}
}

func TestFetchRefusesOversize(t *testing.T) {
	srv := fakeServer(t, []byte("NEWBIN"))
	old := maxDownload
	maxDownload = 3
	t.Cleanup(func() { maxDownload = old })
	if _, err := fetch(context.Background(), srv.URL+"/dl/new"); err == nil {
		t.Fatal("want size error")
	}
	maxDownload = 6
	if b, err := fetch(context.Background(), srv.URL+"/dl/new"); err != nil || string(b) != "NEWBIN" {
		t.Fatalf("%q %v", b, err)
	}
}

func TestFetchTimesOutOnStall(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	old := httpClient
	httpClient = &http.Client{Timeout: 50 * time.Millisecond}
	t.Cleanup(func() { httpClient = old })
	if _, err := fetch(context.Background(), srv.URL); err == nil {
		t.Fatal("want timeout error")
	}
}
