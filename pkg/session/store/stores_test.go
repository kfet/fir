package store

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// useStores registers a default root and read-only extras for one test and
// clears the registry afterwards. Tests using it must not run in parallel.
func useStores(t *testing.T, defaultRoot string, extras ...Store) {
	t.Helper()
	ConfigureStores(defaultRoot, extras)
	t.Cleanup(func() { ConfigureStores("", nil) })
}

const archiveHeaderFmt = `{"type":"session","version":3,"id":%q,"timestamp":"2026-09-01T10:00:00Z","cwd":%q}`

// archiveSession writes a transcript (+ .meta.json unless noMeta) into
// root/<encoded cwd>/ with an mtime of age ago, and returns its path.
func archiveSession(t *testing.T, root, cwd, id, name string, age time.Duration, noMeta bool) string {
	t.Helper()
	dir := filepath.Join(root, encodeCwd(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "2026-09-01T10-00-00-000000000Z_"+id+".jsonl")
	body := fmt.Sprintf(archiveHeaderFmt, id, cwd) + "\n" +
		entryLine(t, "e1", "", userMsg("hello "+id).Message) + "\n" +
		entryLine(t, "e2", "e1", asstText("hi").Message) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	mt := time.Now().Add(-age)
	if !noMeta {
		m := MetaSidecar{Name: name, FirstMessage: "hello " + id, Cwd: cwd, ID: id, MessageCount: 2, Created: mt, ModTime: mt}
		data, _ := json.Marshal(m)
		if err := os.WriteFile(metaPath(path), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(path, mt, mt); err != nil {
		t.Fatal(err)
	}
	return path
}

// snapshotTree fingerprints every file under root (names, modes, content).
func snapshotTree(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, _ := d.Info()
		line := p + " " + info.Mode().String()
		if !d.IsDir() {
			data, _ := os.ReadFile(p)
			line += fmt.Sprintf(" %x %d", sha256.Sum256(data), info.ModTime().UnixNano())
		}
		lines = append(lines, line)
		return nil
	})
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func TestCatalog_ArchiveFromMetaOnly(t *testing.T) {
	local := t.TempDir()
	zbox := t.TempDir()
	useStores(t, local, Store{Label: "zbox", Path: zbox})

	archiveSession(t, zbox, "/home/u/proj", "aaaa-1", "first", 2*time.Hour, false)
	archiveSession(t, zbox, "/home/u/proj", "bbbb-2", "", time.Hour, true)      // no meta → skipped
	archiveSession(t, zbox, "/home/u/proj", "cccc-3", "", 5*time.Second, false) // mid-write → skipped
	p := archiveSession(t, zbox, "/home/u/other", "dddd-4", "", 3*time.Hour, false)
	// Meta without cwd: decoded from the directory name.
	data, _ := json.Marshal(MetaSidecar{ID: "dddd-4", MessageCount: 2})
	os.WriteFile(metaPath(p), data, 0o644)
	// Non-transcripts in the same dir are never listed.
	dir := filepath.Dir(p)
	os.WriteFile(filepath.Join(dir, "bookmarks-dddd-4.jsonl"), []byte("{}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "ctx-dddd-4.jsonl"), []byte("{}\n"), 0o644)
	os.WriteFile(p+".state.jsonl", []byte("{}\n"), 0o644)
	// The same id in the local store: a distinct catalog entry.
	localSS := NewSessionStore("/home/u/proj", SessionDirForRoot(local, "/home/u/proj"))
	localSS.AppendAgentMessage(userMsg("local one"))
	localSS.AppendAgentMessage(asstText("ok"))
	localSS.Close()

	before := snapshotTree(t, zbox)
	all := Catalog(CatalogOptions{})
	if after := snapshotTree(t, zbox); after != before {
		t.Fatal("Catalog modified the read-only store")
	}

	got := map[string]SessionListInfo{}
	for _, s := range all {
		got[s.Key()] = s
	}
	if len(all) != 3 {
		t.Fatalf("catalog = %d entries: %v", len(all), keys(got))
	}
	a, ok := got["zbox:aaaa-1"]
	if !ok || !a.ReadOnly || a.Name != "first" || a.Cwd != "/home/u/proj" {
		t.Errorf("aaaa-1 = %+v", a)
	}
	if d := got["zbox:dddd-4"]; d.Cwd != "/home/u/other" {
		t.Errorf("dir-name cwd fallback = %q", d.Cwd)
	}
	if got["local:"+localSS.GetSessionID()].ReadOnly {
		t.Error("local session must not be read-only")
	}

	// Cwd filter scans one project dir per store.
	proj := Catalog(CatalogOptions{Cwd: "/home/u/proj", ExtrasOnly: true})
	if len(proj) != 1 || proj[0].ID != "aaaa-1" {
		t.Errorf("cwd-filtered extras = %+v", proj)
	}
	if only := Catalog(CatalogOptions{Labels: []string{"local"}}); len(only) != 1 || only[0].Store != "local" {
		t.Errorf("label filter = %+v", only)
	}
}

func TestCatalog_SameIDInTwoStores(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	useStores(t, t.TempDir(), Store{Label: "a", Path: a}, Store{Label: "b", Path: b})
	archiveSession(t, a, "/p", "same-id", "", time.Hour, false)
	archiveSession(t, b, "/p", "same-id", "", time.Hour, false)
	all := Catalog(CatalogOptions{})
	if len(all) != 2 || all[0].Key() == all[1].Key() {
		t.Fatalf("want two distinct keys, got %+v", all)
	}
}

func TestReadOnlyStoreFor(t *testing.T) {
	local := t.TempDir()
	zbox := t.TempDir()
	useStores(t, local, Store{Label: "zbox", Path: zbox})

	if _, ok := ReadOnlyStoreFor(filepath.Join(zbox, "--x--", "f.jsonl")); !ok {
		t.Error("path inside the archive must be read-only")
	}
	if _, ok := ReadOnlyStoreFor(zbox + "-sibling/f"); ok {
		t.Error("a sibling dir with a shared prefix is not inside the store")
	}
	if _, ok := ReadOnlyStoreFor(filepath.Join(local, "--x--", "f.jsonl")); ok {
		t.Error("the default store is writable")
	}
	// A symlink in the default store pointing into the archive is caught.
	link := filepath.Join(local, "--link--")
	if err := os.Symlink(filepath.Join(zbox), link); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadOnlyStoreFor(filepath.Join(link, "new.jsonl")); !ok {
		t.Error("symlink into the archive must be read-only")
	}
	if !IsWithinStore(filepath.Join(local, "--x--", "f.jsonl")) || !IsWithinStore(filepath.Join(zbox, "a")) {
		t.Error("IsWithinStore must accept both stores")
	}
	if IsWithinStore("/etc/passwd") {
		t.Error("IsWithinStore must reject paths outside every store")
	}
}

func TestReadOnlyStore_WritesRefused(t *testing.T) {
	zbox := t.TempDir()
	useStores(t, t.TempDir(), Store{Label: "zbox", Path: zbox})
	src := archiveSession(t, zbox, "/p", "ro-1", "", time.Hour, false)
	before := snapshotTree(t, zbox)

	if err := WriteState(src, map[string]string{"a": "b"}); !errors.Is(err, ErrReadOnlyStore) {
		t.Errorf("WriteState err = %v", err)
	}
	if err := AppendState(src, map[string]string{"a": "b"}, "x"); !errors.Is(err, ErrReadOnlyStore) {
		t.Errorf("AppendState err = %v", err)
	}
	if err := WriteReexecSidecar(src, &ReexecSidecar{}); !errors.Is(err, ErrReadOnlyStore) {
		t.Errorf("WriteReexecSidecar err = %v", err)
	}
	if _, err := ForkFrom(src, "/p", filepath.Dir(src)); !errors.Is(err, ErrReadOnlyStore) {
		t.Errorf("ForkFrom into the archive err = %v", err)
	}
	if _, err := ForkAt(src, "e2", "/p", filepath.Dir(src)); !errors.Is(err, ErrReadOnlyStore) {
		t.Errorf("ForkAt into the archive err = %v", err)
	}
	if _, ok := tryLockSession(src); ok {
		t.Error("tryLockSession must refuse a read-only transcript")
	}
	ss := NewSessionStore("/p", filepath.Join(zbox, "--new--"))
	ss.AppendAgentMessage(userMsg("x"))
	ss.Close()
	writeSidecar(src, &MetaSidecar{ID: "changed"})
	NewObservableStore(CardsPath(src)).Put("s", "k", "slug", "d", "")
	if after := snapshotTree(t, zbox); after != before {
		t.Fatalf("read-only store was modified:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestOpenSessionStore_ArchiveForksIntoDefault(t *testing.T) {
	local := t.TempDir()
	zbox := t.TempDir()
	useStores(t, local, Store{Label: "zbox", Path: zbox})
	src := archiveSession(t, zbox, "/home/u/proj", "ro-2", "", time.Hour, false)
	before := snapshotTree(t, zbox)

	// No sessionDir: defaults to the archive dir, so the fork must go to
	// the default store's dir for the transcript's cwd.
	ss, forked := OpenSessionStore(src)
	if !forked || ss.ForkedFromStore() != "zbox" {
		t.Fatalf("forked=%v store=%q", forked, ss.ForkedFromStore())
	}
	if !strings.Contains(ss.ForkNotice(), `read-only store "zbox"`) {
		t.Errorf("notice = %q", ss.ForkNotice())
	}
	wantDir := SessionDirForRoot(local, "/home/u/proj")
	if filepath.Dir(ss.GetSessionFile()) != wantDir {
		t.Errorf("fork at %s, want dir %s", ss.GetSessionFile(), wantDir)
	}
	if got := ss.GetHeader().ParentSession; got != src {
		t.Errorf("parentSession = %q, want %q", got, src)
	}
	if n := len(ss.BuildSessionContext().Messages); n != 2 {
		t.Errorf("fork has %d messages, want 2", n)
	}
	ss.AppendAgentMessage(userMsg("continue here"))
	ss.Close()
	if after := snapshotTree(t, zbox); after != before {
		t.Fatal("archive modified by resume")
	}

	// With a writable sessionDir (ACP), the fork goes there.
	acpDir := filepath.Join(local, "--acp--")
	ss2, forked := OpenSessionStore(src, acpDir)
	defer ss2.Close()
	if !forked || filepath.Dir(ss2.GetSessionFile()) != acpDir {
		t.Errorf("fork at %s, want dir %s", ss2.GetSessionFile(), acpDir)
	}
}

func TestSetSessionFile_SwitchIntoArchiveForks(t *testing.T) {
	local := t.TempDir()
	zbox := t.TempDir()
	useStores(t, local, Store{Label: "zbox", Path: zbox})
	src := archiveSession(t, zbox, "/p", "ro-3", "", time.Hour, false)
	dir := SessionDirForRoot(local, "/here")
	ss := NewSessionStore("/here", dir)
	defer ss.Close()
	if !ss.SetSessionFile(src) {
		t.Fatal("switching into an archive must fork")
	}
	if filepath.Dir(ss.GetSessionFile()) != dir || ss.GetCwd() != "/here" {
		t.Errorf("file=%s cwd=%s", ss.GetSessionFile(), ss.GetCwd())
	}
	// Switching to a local session clears the archive label.
	other := NewSessionStore("/here", dir)
	other.AppendAgentMessage(userMsg("x"))
	other.AppendAgentMessage(asstText("y"))
	otherPath := other.GetSessionFile()
	other.Close()
	ss.SetSessionFile(otherPath)
	if ss.ForkedFromStore() != "" {
		t.Error("ForkedFromStore must reset on switch")
	}
}

func TestResolveSessionRef_SearchesExtraStores(t *testing.T) {
	local := t.TempDir()
	a, b := t.TempDir(), t.TempDir()
	useStores(t, local, Store{Label: "a", Path: a}, Store{Label: "b", Path: b})
	pa := archiveSession(t, a, "/p", "1111-dup", "", time.Hour, false)
	pb := archiveSession(t, b, "/p", "1111-dup", "", time.Hour, false)
	archiveSession(t, b, "/q", "2222-only", "", time.Hour, false)
	// A bookmarks file with the same id must not make the ref ambiguous.
	os.WriteFile(filepath.Join(filepath.Dir(pb), "bookmarks-1111-dup.jsonl"), []byte("{}\n"), 0o644)

	cwdDir := SessionDirForRoot(local, "/p")
	if got := ResolveSessionRef("1111-dup", cwdDir, local); got != pa {
		t.Errorf("first store wins: got %q want %q", got, pa)
	}
	if got := ResolveSessionRef("b:1111-dup", cwdDir, local); got != pb {
		t.Errorf("label:id: got %q want %q", got, pb)
	}
	if got := ResolveSessionRef("2222", cwdDir, local); !strings.HasSuffix(got, "_2222-only.jsonl") {
		t.Errorf("id fragment: got %q", got)
	}
	if got := ResolveSessionRef("nope:1111", cwdDir, local); got != "" {
		t.Errorf("unknown label: got %q", got)
	}
}

func TestConfigureStores_SkipsOverlapWithDefault(t *testing.T) {
	base := t.TempDir()
	local := filepath.Join(base, "fir", "sessions")
	os.MkdirAll(local, 0o755)
	link := filepath.Join(base, "link")
	os.Symlink(filepath.Join(base, "fir"), link)
	other := t.TempDir()
	skipped := ConfigureStores(local, []Store{
		{Label: "parent", Path: filepath.Join(base, "fir")},
		{Label: "same", Path: local},
		{Label: "inside", Path: filepath.Join(local, "--p--")},
		{Label: "vialink", Path: link},
		{Label: "ok", Path: other},
	})
	t.Cleanup(func() { ConfigureStores("", nil) })
	if len(skipped) != 4 {
		t.Errorf("skipped = %+v", skipped)
	}
	if ex := ExtraStores(); len(ex) != 1 || ex[0].Label != "ok" {
		t.Errorf("extras = %+v", ex)
	}
	if err := checkWritable(filepath.Join(local, "--p--", "x.jsonl")); err != nil {
		t.Errorf("default store must stay writable: %v", err)
	}
}

func TestRemapParent_StaysInsideStore(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	proj := filepath.Join(root, "--p--")
	os.MkdirAll(proj, 0o755)
	inside := filepath.Join(proj, "parent.jsonl")
	os.WriteFile(inside, nil, 0o644)
	outside := filepath.Join(filepath.Dir(root), "secret.jsonl")
	os.WriteFile(outside, nil, 0o644)

	if got := remapParent(root, "/other/host/sessions/--p--/parent.jsonl"); got != inside {
		t.Errorf("remap = %q, want %q", got, inside)
	}
	// A recorded parent of "../secret.jsonl" must not resolve outside the store.
	if got := remapParent(root, "../secret.jsonl"); got == outside {
		t.Errorf("remap escaped the store: %q", got)
	}
}

func TestFileNameHelpers(t *testing.T) {
	ts, ok := TimestampFromFileName("/x/2026-10-08T18-15-25-584271117Z_abc-def.jsonl")
	if !ok || !ts.Equal(time.Date(2026, 10, 8, 18, 15, 25, 584271117, time.UTC)) {
		t.Errorf("ts = %v ok=%v", ts, ok)
	}
	if _, ok := TimestampFromFileName("bookmarks-abc.jsonl"); ok {
		t.Error("no timestamp in a bookmarks file name")
	}
	if id := SessionIDFromFileName("2026-10-08T18-15-25-5Z_abc-def.jsonl"); id != "abc-def" {
		t.Errorf("id = %q", id)
	}
	if got := DecodeCwdDir("--home-kfet-src-fir--"); got != "/home/kfet/src/fir" {
		t.Errorf("decode = %q", got)
	}
	if got := DecodeCwdDir("plain"); got != "" {
		t.Errorf("decode non-encoded = %q", got)
	}
	for name, want := range map[string]bool{
		"2026_x.jsonl": true, "my-session.jsonl": true,
		"bookmarks-x.jsonl": false, "ctx-x.jsonl": false,
		"2026_x.jsonl.state.jsonl": false, "x.jsonl.meta.json": false,
	} {
		if IsTranscriptName(name) != want {
			t.Errorf("IsTranscriptName(%q) != %v", name, want)
		}
	}
}

func keys(m map[string]SessionListInfo) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
