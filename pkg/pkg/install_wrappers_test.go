// Coverage for the install-time wrapper-generation reporting in Manager.Install.
//
// Like jswrapper_errors_test.go, the failure case here is forced with a 0500
// directory and therefore requires an unprivileged test run.
package pkg

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStdout runs fn with os.Stdout redirected and returns what it printed.
// Manager.Install reports to the user with fmt.Printf, so the message is the
// only observable for these branches — and the message IS the contract: an
// install that silently generated nothing, or silently failed to, is the bug.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	fn()

	os.Stdout = orig
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out := <-done
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestInstallReportsGeneratedWrappers covers the success arm: installing a
// package that ships a JS/TS entry point creates its `main → run.sh` wrapper
// and says so. Without the wrapper the package installs but contributes zero
// extensions, which is the silent failure this whole feature exists to fix —
// so the count being reported is what tells a user it actually worked.
func TestInstallReportsGeneratedWrappers(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	pkgDir := t.TempDir()
	writePkgFile(t, filepath.Join(pkgDir, "index.ts"), "export default function(pi){}\n", 0o644)

	mgr := New(t.TempDir(), t.TempDir(), &mockSettings{})
	var installErr error
	out := captureStdout(t, func() { installErr = mgr.Install(pkgDir, true) })
	if installErr != nil {
		t.Fatalf("Install: %v", installErr)
	}

	if !strings.Contains(out, "Created 1 runtime wrapper(s)") {
		t.Errorf("install did not report the generated wrapper; output:\n%s", out)
	}
	link := filepath.Join(pkgDir, "main")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("no main wrapper was created: %v", err)
	}
	if filepath.Base(target) != "run.sh" {
		t.Errorf("wrapper target = %q, want a run.sh", target)
	}
	// The whole point of the wrapper: discovery now finds an extension.
	res, err := ScanPackageResources(pkgDir)
	if err != nil {
		t.Fatalf("ScanPackageResources: %v", err)
	}
	if len(res.Extensions) != 1 {
		t.Errorf("Extensions = %v, want exactly the generated wrapper", res.Extensions)
	}
}

// TestInstallWarnsOnWrapperFailure covers the failure arm: wrapper generation
// is best-effort, so a package directory fir cannot write must still install
// (its skills remain usable) while telling the user why no extension appeared.
// Aborting the install here would be worse; swallowing the error would leave
// the user with a package that inexplicably contributes nothing.
func TestInstallWarnsOnWrapperFailure(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	pkgDir := t.TempDir()
	writePkgFile(t, filepath.Join(pkgDir, "index.ts"), "export default function(pi){}\n", 0o644)
	writePkgFile(t, filepath.Join(pkgDir, "askill", "SKILL.md"), "# hi\n", 0o644)
	if err := os.Chmod(pkgDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(pkgDir, 0o755) })

	sm := &mockSettings{}
	mgr := New(t.TempDir(), t.TempDir(), sm)
	var installErr error
	out := captureStdout(t, func() { installErr = mgr.Install(pkgDir, true) })

	if installErr != nil {
		t.Fatalf("Install must succeed despite wrapper generation failing: %v", installErr)
	}
	if !strings.Contains(out, "Warning: JS/TS wrapper generation:") {
		t.Errorf("install did not warn about the failure; output:\n%s", out)
	}
	if strings.Contains(out, "Created") {
		t.Errorf("install reported creating wrappers it did not create; output:\n%s", out)
	}
	if len(sm.project) != 1 {
		t.Errorf("package should still be registered; project packages = %v", sm.project)
	}
	if !strings.Contains(out, "1 skill(s)") {
		t.Errorf("the package's other resources should still be reported; output:\n%s", out)
	}
}
