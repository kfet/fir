package pkg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pushCommit adds a commit to the bare repo at bareURL by cloning it into a
// scratch directory, committing, and pushing back. No network is involved.
func pushCommit(t *testing.T, bareURL, name string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "push-"+name)
	gitIn(t, t.TempDir(), "clone", "--quiet", bareURL, work)
	if err := os.WriteFile(filepath.Join(work, name+".txt"), []byte(name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "add", ".")
	gitIn(t, work, "commit", "--quiet", "-m", name)
	gitIn(t, work, "push", "--quiet", "origin", "HEAD:main")
}

// TestInstallRejectsUnparseableSource pins that a source fir cannot parse is
// rejected before anything is cloned or registered.
func TestInstallRejectsUnparseableSource(t *testing.T) {
	sm := &mockSettings{}
	m := New(t.TempDir(), t.TempDir(), sm)

	if err := m.Install("git:", false); err == nil {
		t.Fatal("expected an error for an unparseable source")
	}
	if len(sm.global)+len(sm.project) != 0 {
		t.Errorf("nothing should be registered, got %v / %v", sm.global, sm.project)
	}
}

// TestInstallMissingLocalPath pins that a local package must exist at install
// time — registering a path that is not there would produce a package that
// silently contributes nothing.
func TestInstallMissingLocalPath(t *testing.T) {
	sm := &mockSettings{}
	m := New(t.TempDir(), t.TempDir(), sm)
	missing := filepath.Join(t.TempDir(), "nope")

	err := m.Install(missing, false)
	if err == nil {
		t.Fatal("expected an error installing a missing local path")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("unexpected error: %v", err)
	}
	if len(sm.global) != 0 {
		t.Errorf("failed install must not register the package: %v", sm.global)
	}
}

// TestInstallCloneFailure pins that a clone failure aborts the install and
// leaves settings untouched.
func TestInstallCloneFailure(t *testing.T) {
	base := t.TempDir()
	initSubdirRepo(t, base)
	sm := &mockSettings{}
	m := New(t.TempDir(), t.TempDir(), sm)

	err := m.Install("github.com/testorg/no-such-repo", false)
	if err == nil {
		t.Fatal("expected an error cloning a non-existent repository")
	}
	if len(sm.global) != 0 {
		t.Errorf("failed install must not register the package: %v", sm.global)
	}
}

// TestInstallReportsScanFailure pins that a package whose fir.json is broken
// fails the install loudly instead of installing zero resources.
func TestInstallReportsScanFailure(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "fir.json"), "{oops")
	m := New(t.TempDir(), t.TempDir(), &mockSettings{})

	err := m.Install(dir, false)
	if err == nil {
		t.Fatal("expected an error for a package with a broken manifest")
	}
	if !strings.Contains(err.Error(), "scanning package at") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestInstallEmptyPackageSucceeds pins that a package contributing nothing is
// a warning, not a failure: fir installs it and says so.
func TestInstallEmptyPackageSucceeds(t *testing.T) {
	dir := t.TempDir()
	sm := &mockSettings{}
	m := New(t.TempDir(), t.TempDir(), sm)

	if err := m.Install(dir, false); err != nil {
		t.Fatalf("Install of an empty package: %v", err)
	}
	if len(sm.global) != 1 {
		t.Errorf("empty package should still be registered, got %v", sm.global)
	}
}

// TestInstallProjectScopeGit pins where a project-scope git package lands
// (.fir/packages/... under the project, never the user config dir) and that a
// repeat install is idempotent in that scope.
func TestInstallProjectScopeGit(t *testing.T) {
	base := t.TempDir()
	repo := initSubdirRepo(t, base)
	agentDir := t.TempDir()
	cwd := t.TempDir()
	sm := &mockSettings{}
	m := New(agentDir, cwd, sm)

	if err := m.Install(repo+"/reminders", true); err != nil {
		t.Fatalf("Install project-scope: %v", err)
	}
	clone := filepath.Join(cwd, ".fir", "packages", "git", "github.com", "testorg", "exts")
	mustExist(t, filepath.Join(clone, "reminders", "rem", "SKILL.md"))
	if _, err := os.Stat(filepath.Join(agentDir, "packages")); !os.IsNotExist(err) {
		t.Errorf("project-scope install must not touch the agent dir, err=%v", err)
	}
	if len(sm.global) != 0 {
		t.Errorf("project-scope install must not register globally: %v", sm.global)
	}

	// Idempotent, and the peer scan runs against the project scope.
	if err := m.Install(repo+"/reminders", true); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if len(sm.project) != 1 {
		t.Errorf("repeat install should not duplicate: %v", sm.project)
	}

	// A second subdirectory of the same repo shares the project clone.
	if err := m.Install(repo+"/triage", true); err != nil {
		t.Fatalf("Install sibling subdir: %v", err)
	}
	mustExist(t, filepath.Join(clone, "triage", "tri", "SKILL.md"))

	// Uninstall in project scope shrinks the shared clone rather than
	// deleting it.
	if err := m.Uninstall(repo+"/triage", true); err != nil {
		t.Fatalf("Uninstall project-scope: %v", err)
	}
	if len(sm.project) != 1 {
		t.Errorf("want 1 project package left, got %v", sm.project)
	}
	mustExist(t, filepath.Join(clone, "reminders", "rem", "SKILL.md"))
}

// TestConflictingRefMessageNamesDefaultBranch pins the wording of the ref
// conflict when the installed peer tracks the default branch: an empty ref
// must be rendered as "default branch", not as an empty string.
func TestConflictingRefMessageNamesDefaultBranch(t *testing.T) {
	m, repo, _ := newSubdirManager(t)

	if err := m.Install(repo+"/reminders", false); err != nil {
		t.Fatal(err)
	}
	err := m.Install(repo+"/triage@v1", false)
	if err == nil {
		t.Fatal("expected a ref conflict")
	}
	if !strings.Contains(err.Error(), `"default branch"`) {
		t.Errorf("error should describe the unpinned peer, got: %v", err)
	}
}

// TestUninstallRejectsUnparseableSource pins that Uninstall validates its
// input before mutating settings.
func TestUninstallRejectsUnparseableSource(t *testing.T) {
	sm := &mockSettings{global: []any{"github.com/testorg/exts"}}
	m := New(t.TempDir(), t.TempDir(), sm)

	if err := m.Uninstall("git:", false); err == nil {
		t.Fatal("expected an error for an unparseable source")
	}
	if len(sm.global) != 1 {
		t.Errorf("settings must be untouched, got %v", sm.global)
	}
}

// TestUninstallKeepsCloneUsedByWholeRepoPeer pins that removing a
// subdirectory package never deletes a clone another package needs in full.
func TestUninstallKeepsCloneUsedByWholeRepoPeer(t *testing.T) {
	m, repo, clone := newSubdirManager(t)

	if err := m.Install(repo+"/reminders", false); err != nil {
		t.Fatal(err)
	}
	if err := m.Install(repo, false); err != nil {
		t.Fatal(err)
	}
	if err := m.Uninstall(repo+"/reminders", false); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	mustExist(t, filepath.Join(clone, "reminders", "rem", "SKILL.md"))
	mustExist(t, filepath.Join(clone, "triage", "tri", "SKILL.md"))
}

// TestUninstallReportsSparseSetFailure pins that a failure to shrink a shared
// clone is reported: continuing would leave the uninstalled package's files
// on disk while claiming success.
func TestUninstallReportsSparseSetFailure(t *testing.T) {
	m, repo, clone := newSubdirManager(t)

	if err := m.Install(repo+"/reminders", false); err != nil {
		t.Fatal(err)
	}
	if err := m.Install(repo+"/triage", false); err != nil {
		t.Fatal(err)
	}
	// A clone whose git metadata is not writable cannot take a new sparse
	// set: git fails to create its lock file.
	info := filepath.Join(clone, ".git", "info")
	if err := os.Chmod(info, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(info, 0o700) })

	err := m.Uninstall(repo+"/reminders", false)
	if err == nil {
		t.Fatal("expected the sparse shrink to fail on a read-only clone")
	}
	if !strings.Contains(err.Error(), "sparse-checkout set") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestUninstallReportsRemoveFailure pins that an undeletable clone is an
// error, not a silent leak of disk space.
func TestUninstallReportsRemoveFailure(t *testing.T) {
	m, repo, clone := newSubdirManager(t)

	if err := m.Install(repo+"/reminders", false); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(clone)
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	err := m.Uninstall(repo+"/reminders", false)
	if err == nil {
		t.Fatal("expected removing an undeletable clone to fail")
	}
	if !strings.Contains(err.Error(), "removing clone at") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestUpdateOnlyNamedPackage pins the source filter: naming one package
// updates that package and leaves its siblings alone.
func TestUpdateOnlyNamedPackage(t *testing.T) {
	base := t.TempDir()
	repo := initSubdirRepo(t, base)
	agentDir := t.TempDir()
	m := New(agentDir, t.TempDir(), &mockSettings{})
	clone := filepath.Join(agentDir, "packages", "git", "github.com", "testorg", "exts")

	if err := m.Install(repo+"/reminders", false); err != nil {
		t.Fatal(err)
	}
	pushCommit(t, filepath.Join(base, "exts"), "later")

	// An alternate spelling of the same package still matches.
	if err := m.Update("https://" + repo + "/reminders"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	mustExist(t, filepath.Join(clone, "later.txt"))
}

// TestUpdateSkipsUnrelatedPackage pins the other side of the filter.
func TestUpdateSkipsUnrelatedPackage(t *testing.T) {
	base := t.TempDir()
	repo := initSubdirRepo(t, base)
	agentDir := t.TempDir()
	m := New(agentDir, t.TempDir(), &mockSettings{})
	clone := filepath.Join(agentDir, "packages", "git", "github.com", "testorg", "exts")

	if err := m.Install(repo+"/reminders", false); err != nil {
		t.Fatal(err)
	}
	pushCommit(t, filepath.Join(base, "exts"), "later")

	if err := m.Update("github.com/testorg/other"); err != nil {
		t.Fatalf("Update of an unrelated source: %v", err)
	}
	if _, err := os.Stat(filepath.Join(clone, "later.txt")); !os.IsNotExist(err) {
		t.Errorf("unrelated package was updated, err=%v", err)
	}
}

// TestUpdateRejectsUnparseableSource pins input validation on Update.
func TestUpdateRejectsUnparseableSource(t *testing.T) {
	m := New(t.TempDir(), t.TempDir(), &mockSettings{})
	if err := m.Update("git:"); err == nil {
		t.Fatal("expected an error for an unparseable source")
	}
}

// TestUpdateReportsPullFailure pins that a package which cannot fast-forward
// reports an error, and that Update keeps going through the rest of the list.
func TestUpdateReportsPullFailure(t *testing.T) {
	base := t.TempDir()
	repo := initSubdirRepo(t, base)
	agentDir := t.TempDir()
	sm := &mockSettings{}
	m := New(agentDir, t.TempDir(), sm)
	clone := filepath.Join(agentDir, "packages", "git", "github.com", "testorg", "exts")

	if err := m.Install(repo+"/reminders", false); err != nil {
		t.Fatal(err)
	}
	// A second, healthy package must still be updated after the failure.
	other := t.TempDir()
	writeFile(t, filepath.Join(other, "skill", "SKILL.md"), "")
	if err := m.Install(other, false); err != nil {
		t.Fatal(err)
	}

	// Diverge the clone from origin so "merge --ff-only" cannot succeed.
	if err := os.WriteFile(filepath.Join(clone, "local.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, clone, "add", ".")
	gitIn(t, clone, "commit", "--quiet", "-m", "local work")
	pushCommit(t, filepath.Join(base, "exts"), "upstream")

	err := m.Update("")
	if err == nil {
		t.Fatal("expected Update to report the failed fast-forward")
	}
	if !strings.Contains(err.Error(), "merge --ff-only") {
		t.Errorf("unexpected error: %v", err)
	}
	if len(sm.global) != 2 {
		t.Errorf("a failed update must not deregister anything: %v", sm.global)
	}
}

// TestUpdatePinnedPackageReportsFetchFailure pins the pinned (detached-HEAD)
// refresh path: a pinned package refreshes by fetch + checkout, and a dead
// remote is reported.
func TestUpdatePinnedPackageReportsFetchFailure(t *testing.T) {
	base := t.TempDir()
	repo := initSubdirRepo(t, base)
	m := New(t.TempDir(), t.TempDir(), &mockSettings{})

	if err := m.Install(repo+"/reminders@v1", false); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(base, "exts")); err != nil {
		t.Fatal(err)
	}

	err := m.Update("")
	if err == nil {
		t.Fatal("expected the fetch of a pinned package to fail")
	}
	if !strings.Contains(err.Error(), "fetch") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestUpdatePinnedPackageReportsCheckoutFailure pins that a pin which no
// longer resolves upstream is reported rather than silently leaving the old
// checkout in place.
func TestUpdatePinnedPackageReportsCheckoutFailure(t *testing.T) {
	base := t.TempDir()
	repo := initSubdirRepo(t, base)
	agentDir := t.TempDir()
	m := New(agentDir, t.TempDir(), &mockSettings{})
	clone := filepath.Join(agentDir, "packages", "git", "github.com", "testorg", "exts")

	if err := m.Install(repo+"/reminders@v1", false); err != nil {
		t.Fatal(err)
	}
	// The tag disappears upstream and locally: the next refresh cannot
	// resolve the pin.
	gitIn(t, filepath.Join(base, "exts"), "tag", "-d", "v1")
	gitIn(t, clone, "update-ref", "-d", "refs/tags/v1")

	err := m.Update("")
	if err == nil {
		t.Fatal("expected the checkout of a vanished pin to fail")
	}
	if !strings.Contains(err.Error(), "checkout") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestUpdateReportsMissingSubdir pins that an update whose git work succeeds
// but whose package directory is absent is still an error, naming the
// subdirectory.
func TestUpdateReportsMissingSubdir(t *testing.T) {
	base := t.TempDir()
	repo := initSubdirRepo(t, base)
	agentDir := t.TempDir()
	m := New(agentDir, t.TempDir(), &mockSettings{})
	clone := filepath.Join(agentDir, "packages", "git", "github.com", "testorg", "exts")

	if err := m.Install(repo+"/reminders", false); err != nil {
		t.Fatal(err)
	}
	// Simulate the subdirectory disappearing from the working tree (a
	// half-finished checkout, a manual delete).
	if err := os.RemoveAll(filepath.Join(clone, "reminders")); err != nil {
		t.Fatal(err)
	}

	err := m.Update("")
	if err == nil {
		t.Fatal("expected Update to report the missing subdirectory")
	}
	if !strings.Contains(err.Error(), `subdirectory "reminders" not found`) {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestCheckInstallPathMessages pins the two distinct diagnostics, which are
// the user's only clue about what went wrong.
func TestCheckInstallPathMessages(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")

	subErr := checkInstallPath(&Source{URL: "https://example.com/a/b", SubDir: "sub"}, missing)
	if subErr == nil || !strings.Contains(subErr.Error(), `subdirectory "sub" not found`) {
		t.Errorf("subdir case: %v", subErr)
	}
	wholeErr := checkInstallPath(&Source{URL: "https://example.com/a/b"}, missing)
	if wholeErr == nil || !strings.Contains(wholeErr.Error(), "package directory") {
		t.Errorf("whole-repo case: %v", wholeErr)
	}
	if err := checkInstallPath(&Source{SubDir: "sub"}, t.TempDir()); err != nil {
		t.Errorf("existing path should be accepted: %v", err)
	}
}

// TestListRejectsBadEntries pins that a malformed settings entry surfaces as
// an error naming the entry, in both scopes — silently dropping it would make
// a package vanish with no explanation.
func TestListRejectsBadEntries(t *testing.T) {
	tests := []struct {
		name    string
		entry   any
		wantErr string
	}{
		{"unreadable", 42, "unreadable package entry"},
		{"unparseable", "git:", `package "git:"`},
	}
	for _, tc := range tests {
		t.Run(tc.name+"/global", func(t *testing.T) {
			m := New(t.TempDir(), t.TempDir(), &mockSettings{global: []any{tc.entry}})
			_, err := m.List()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("List: %v, want %q", err, tc.wantErr)
			}
			if _, err := m.Resolve(); err == nil {
				t.Error("Resolve should propagate the List error")
			}
			if err := m.Update(""); err == nil {
				t.Error("Update should propagate the List error")
			}
			if _, _, _, err := m.ResolvePackageResources(); err == nil {
				t.Error("ResolvePackageResources should propagate the List error")
			}
			if _, err := m.ResolvePackageContributions(); err == nil {
				t.Error("ResolvePackageContributions should propagate the List error")
			}
		})
		t.Run(tc.name+"/project", func(t *testing.T) {
			m := New(t.TempDir(), t.TempDir(), &mockSettings{project: []any{tc.entry}})
			_, err := m.List()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("List: %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// TestResolveSkipsUninstalledPackage pins that a registered package whose
// files are not on disk contributes nothing instead of failing the whole
// resolve — a half-installed package must not break session startup.
func TestResolveSkipsUninstalledPackage(t *testing.T) {
	present := t.TempDir()
	writeFile(t, filepath.Join(present, "skill", "SKILL.md"), "")
	absent := filepath.Join(t.TempDir(), "not-installed")

	m := New(t.TempDir(), t.TempDir(), &mockSettings{global: []any{present, absent}})

	rr, err := m.Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(rr.Skills) != 1 {
		t.Fatalf("want 1 skill, got %v", rr.Skills)
	}

	pkgs, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if pkgs[1].Resources != nil {
		t.Errorf("missing package should have no resources, got %+v", pkgs[1].Resources)
	}
}

// TestResolvePackageResourcesAggregates pins the resources.ResourcePackageResolver
// implementation: the flat aggregate and the per-package attribution must
// agree, and attribution must carry scope and install path.
func TestResolvePackageResourcesAggregates(t *testing.T) {
	userPkg := t.TempDir()
	writeFile(t, filepath.Join(userPkg, "skill", "SKILL.md"), "")
	writeFile(t, filepath.Join(userPkg, "hook.py"), "")
	projPkg := t.TempDir()
	writeFile(t, filepath.Join(projPkg, "themes", "dark.json"), "{}")
	missing := filepath.Join(t.TempDir(), "gone")

	sm := &mockSettings{global: []any{userPkg, missing}, project: []any{projPkg}}
	m := New(t.TempDir(), t.TempDir(), sm)

	exts, skills, themes, err := m.ResolvePackageResources()
	if err != nil {
		t.Fatalf("ResolvePackageResources: %v", err)
	}
	if len(exts) != 1 || len(skills) != 1 || len(themes) != 1 {
		t.Fatalf("aggregate = %v / %v / %v", exts, skills, themes)
	}

	contribs, err := m.ResolvePackageContributions()
	if err != nil {
		t.Fatalf("ResolvePackageContributions: %v", err)
	}
	if len(contribs) != 3 {
		t.Fatalf("want one contribution per registered package, got %d", len(contribs))
	}
	if contribs[0].Source != userPkg || contribs[0].Scope != "user" || contribs[0].InstallPath != userPkg {
		t.Errorf("user contribution = %+v", contribs[0])
	}
	if len(contribs[0].Skills) != 1 || len(contribs[0].Extensions) != 1 {
		t.Errorf("user contribution resources = %+v", contribs[0])
	}
	if len(contribs[1].Skills)+len(contribs[1].Extensions)+len(contribs[1].Themes) != 0 {
		t.Errorf("uninstalled package should contribute nothing: %+v", contribs[1])
	}
	if contribs[2].Scope != "project" || len(contribs[2].Themes) != 1 {
		t.Errorf("project contribution = %+v", contribs[2])
	}
}

// jsonSource is a settings entry that only decodes through the JSON
// round-trip fallback — a struct, not a map, exactly as a typed settings
// decoder would hand it over.
type jsonSource struct {
	Source string `json:"source"`
	Ref    string `json:"ref,omitempty"`
}

// stringSource marshals to a bare JSON string, covering the second fallback.
type stringSource string

// TestEntrySource pins how a packages entry is read out of settings. Settings
// files in the wild hold plain strings, objects, and (after a typed decode)
// Go structs; all three must resolve to the same source.
func TestEntrySource(t *testing.T) {
	tests := []struct {
		name  string
		entry any
		want  string
	}{
		{"plain string", "github.com/a/b", "github.com/a/b"},
		{"object", map[string]any{"source": "github.com/a/b", "ref": "v1"}, "github.com/a/b"},
		{"object without source", map[string]any{"ref": "v1"}, ""},
		{"object with non-string source", map[string]any{"source": 7}, ""},
		{"struct", jsonSource{Source: "github.com/a/b", Ref: "v1"}, "github.com/a/b"},
		{"json string type", stringSource("github.com/a/b"), "github.com/a/b"},
		{"raw json object", json.RawMessage(`{"source":"github.com/a/b"}`), "github.com/a/b"},
		{"number", 42, ""},
		{"unmarshalable", make(chan int), ""},
		{"nil", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := entrySource(tc.entry); got != tc.want {
				t.Errorf("entrySource(%#v) = %q, want %q", tc.entry, got, tc.want)
			}
		})
	}
}

// TestContainsPackageIdentity pins deduplication by canonical identity: the
// same repo spelled three ways is one package, and unreadable or unrelated
// entries never match.
func TestContainsPackageIdentity(t *testing.T) {
	installed := []any{
		"",                   // unreadable entry, skipped
		"git:",               // registered but unparseable, skipped
		42,                   // not a source at all, skipped
		"github.com/a/b/sub", // the real entry
		map[string]any{"source": "github.com/other/repo"},
	}

	for _, spelling := range []string{
		"github.com/a/b/sub",
		"https://github.com/a/b/sub",
		"git@github.com:a/b/sub",
		"github.com/a/b/sub@v2", // same identity: the ref is not part of it
	} {
		if !containsPackage(installed, spelling) {
			t.Errorf("containsPackage(%q) = false, want true", spelling)
		}
	}

	for _, other := range []string{
		"github.com/a/b",       // whole repo is a different package to a subdir
		"github.com/a/b/other", // different subdir
		"gitlab.com/a/b/sub",   // different host
	} {
		if containsPackage(installed, other) {
			t.Errorf("containsPackage(%q) = true, want false", other)
		}
	}

	// An entry fir can no longer parse is still recognised verbatim, so a
	// repeat install does not duplicate it. This mirrors filterPackage,
	// which removes such an entry verbatim.
	if !containsPackage(installed, "git:") {
		t.Error(`containsPackage("git:") = false; a verbatim match must count as installed`)
	}
	if containsPackage(installed, "git:other/thing") {
		t.Error("a different unparseable source must not match")
	}
}

// TestFilterPackageRemovesByIdentity pins removal by canonical identity,
// including entries that only match verbatim.
func TestFilterPackageRemovesByIdentity(t *testing.T) {
	pkgs := []any{
		"github.com/a/b",
		map[string]any{"source": "https://github.com/a/b"},
		"github.com/keep/me",
		"git:", // unparseable, kept
	}

	got := filterPackage(pkgs, "git@github.com:a/b")
	if len(got) != 2 {
		t.Fatalf("want 2 entries left, got %v", got)
	}
	if entrySource(got[0]) != "github.com/keep/me" || entrySource(got[1]) != "git:" {
		t.Errorf("wrong survivors: %v", got)
	}

	// An unparseable target still removes a verbatim match.
	if got := filterPackage(pkgs, "git:"); len(got) != 3 {
		t.Errorf("verbatim removal: want 3 left, got %v", got)
	}
}

// TestClonePeersIgnoresUnusableEntries pins that junk in the packages list
// cannot break the shared-clone bookkeeping: a ref conflict is still detected
// past an unreadable entry, a local package, and an unparseable one.
func TestClonePeersIgnoresUnusableEntries(t *testing.T) {
	base := t.TempDir()
	repo := initSubdirRepo(t, base)
	sm := &mockSettings{global: []any{
		"",                     // unreadable
		"git:",                 // unparseable
		t.TempDir(),            // local package, not a clone peer
		repo + "/reminders@v1", // the peer that matters
	}}
	m := New(t.TempDir(), t.TempDir(), sm)

	err := m.Install(repo+"/triage@v2", false)
	if err == nil {
		t.Fatal("expected the ref conflict to be detected past the junk entries")
	}
	if !strings.Contains(err.Error(), "already installed at ref") {
		t.Errorf("unexpected error: %v", err)
	}
}
