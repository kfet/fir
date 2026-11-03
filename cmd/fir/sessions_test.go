package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kfet/fir/pkg/session/store"
)

// sessionsFixture: local store with one session in /p (bookmark + transcript
// mention "needle"), archive "zbox" with an older /p session mentioning it.
func sessionsFixture(t *testing.T) (local, zbox string) {
	t.Helper()
	local, zbox = t.TempDir(), t.TempDir()
	store.ConfigureStores(local, []store.Store{{Label: "zbox", Path: zbox}})
	t.Cleanup(func() { store.ConfigureStores("", nil) })

	write := func(root, ts, id, text string, age time.Duration, meta bool) string {
		dir := store.SessionDirForRoot(root, "/p")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, ts+"_"+id+".jsonl")
		body := fmt.Sprintf(`{"type":"session","version":3,"id":%q,"timestamp":"2026-01-01T00:00:00Z","cwd":"/p"}`+"\n"+
			`{"type":"message","id":"e1","message":{"role":"user","content":%q}}`+"\n", id, text)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := time.Now().Add(-age)
		if meta {
			m, _ := json.Marshal(store.MetaSidecar{ID: id, Cwd: "/p", Name: "n-" + id, FirstMessage: text, MessageCount: 1, ModTime: mt})
			os.WriteFile(path+".meta.json", m, 0o644)
		}
		os.Chtimes(path, mt, mt)
		return path
	}
	lp := write(local, "2026-10-02T00-00-00-0Z", "local-1", "the needle here", 0, false)
	os.WriteFile(filepath.Join(filepath.Dir(lp), "bookmarks-local-1.jsonl"), []byte(`{"_bookmark_note":"orient: needle map"}`+"\n"), 0o644)
	os.WriteFile(filepath.Join(filepath.Dir(lp), "ctx-local-1.jsonl"), []byte(`{"needle":1}`+"\n"), 0o644)
	write(zbox, "2026-09-01T00-00-00-0Z", "zbox-1", "older NEEDLE there", 2*time.Hour, true)
	return local, zbox
}

func runSessionsOut(t *testing.T, args ...string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := runSessionsArgs(args, &buf); err != nil {
		t.Fatalf("fir sessions %v: %v", args, err)
	}
	return buf.String()
}

func TestSessionsList(t *testing.T) {
	_, zbox := sessionsFixture(t)
	out := runSessionsOut(t, "list", "--cwd", "/p")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("list:\n%s", out)
	}
	f := strings.Split(lines[1], "\t")
	if len(f) != 8 || f[0] != "zbox" || f[2] != "zbox-1" || !strings.HasPrefix(f[4], zbox) || f[5] != "/p" {
		t.Errorf("archive row = %q", f)
	}
	if !strings.HasPrefix(lines[0], "local\t") {
		t.Errorf("newest first: %q", lines[0])
	}

	out = runSessionsOut(t, "list", "--all", "--read-only", "--json")
	var row map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &row); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if row["store"] != "zbox" || row["read_only"] != true || row["name"] != "n-zbox-1" {
		t.Errorf("json row = %v", row)
	}
	var buf bytes.Buffer
	if err := runSessionsArgs([]string{"list", "--store", "nope"}, &buf); err == nil {
		t.Error("unknown store label must be an error")
	}
}

func TestSessionsRelativeCwd(t *testing.T) {
	sessionsFixture(t)
	t.Chdir("/")
	if out := runSessionsOut(t, "list", "--cwd", "./p"); strings.Count(out, "\n") != 2 {
		t.Errorf("list --cwd ./p from /:\n%s", out)
	}
	if out := runSessionsOut(t, "search", "--cwd", "./p", "-i", "needle"); strings.Count(out, "\n") != 3 {
		t.Errorf("search --cwd ./p from /:\n%s", out)
	}
}

func TestSessionsDirs(t *testing.T) {
	local, zbox := sessionsFixture(t)
	want := fmt.Sprintf("local\t%s\tfalse\ttrue\nzbox\t%s\ttrue\ttrue\n", local, zbox)
	if got := runSessionsOut(t, "dirs"); got != want {
		t.Errorf("dirs = %q, want %q", got, want)
	}
}

func TestSessionsSearch(t *testing.T) {
	sessionsFixture(t)
	out := runSessionsOut(t, "search", "-i", "needle")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("search:\n%s", out)
	}
	// Bookmarks first, then transcripts newest first; ctx logs never.
	wantKinds := []string{"local\tbookmark\tlocal-1", "local\ttranscript\tlocal-1", "zbox\ttranscript\tzbox-1"}
	for i, w := range wantKinds {
		if !strings.HasPrefix(lines[i], w+"\t") {
			t.Errorf("line %d = %q, want prefix %q", i, lines[i], w)
		}
	}
	if strings.Contains(out, "ctx-") {
		t.Error("ctx logs must be excluded")
	}
	if !strings.Contains(lines[2], ".jsonl:2\t") {
		t.Errorf("hit must carry path:line: %q", lines[2])
	}

	// Case-sensitive, flags after the pattern, limit and store filter.
	if out := runSessionsOut(t, "search", "NEEDLE", "--store", "zbox"); strings.Count(out, "\n") != 1 {
		t.Errorf("store-filtered search:\n%s", out)
	}
	if out := runSessionsOut(t, "search", "-i", "needle", "--limit", "1"); strings.Count(out, "\n") != 1 {
		t.Errorf("limit 1:\n%s", out)
	}
	if out := runSessionsOut(t, "search", "-i", "needle", "--kind", "transcript", "--json"); strings.Count(out, "\n") != 2 || !strings.Contains(out, `"kind":"transcript"`) {
		t.Errorf("kind+json:\n%s", out)
	}
	var buf bytes.Buffer
	if err := runSessionsArgs([]string{"search"}, &buf); err == nil {
		t.Error("search without a pattern must fail")
	}
}

func TestSnippet(t *testing.T) {
	line := []byte(strings.Repeat("a", 300) + "MATCH\t" + strings.Repeat("b", 300) + "\n")
	s := snippet(line, 300, 305)
	if !strings.HasPrefix(s, "…") || !strings.HasSuffix(s, "…") || !strings.Contains(s, "MATCH") || strings.ContainsAny(s, "\t\n") {
		t.Errorf("snippet = %q", s)
	}
}
