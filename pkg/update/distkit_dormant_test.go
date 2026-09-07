package update

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// swapEntryPoints are the distkit identifiers that write, download, or stage a
// binary. This release wires distkit in as a DORMANT resolver: it may answer
// "which version is latest", and nothing more. go-selfupdate performs every
// byte of the actual swap (SelfUpdate in update.go).
//
// distkit.Update is on the list even though it returns early under
// CheckOnly — the guarantee this release makes to the fleet is worth more
// than the printing it would save, and one boolean is too small a gap between
// a report and a binary swap.
var swapEntryPoints = map[string]string{
	"Update":         "runs the whole flow and renames a new binary over the running one",
	"Main":           "is the CLI front end for Update",
	"Download":       "fetches a release asset to disk",
	"Apply":          "renames a staged binary over the target",
	"StagingDir":     "creates the directory a swap stages into",
	"UpgradeViaBrew": "shells out to `brew upgrade`",
}

// TestDistkitPathCannotSwap walks every Go file in the fir module and fails if
// any of them so much as names a distkit entry point that could replace a
// binary. It is a source-level pin, not a behavioural one, and that is the
// point: a behavioural test can only prove that the paths exercised today do
// not swap, while this proves no path in this module can, including one added
// tomorrow. Its reach ends at the module — a dependency that itself imported
// distkit and called Update would be invisible — which is one reason fir
// imports distkit directly rather than through some wrapper library.
//
// When the swap does move to distkit — a later release, gated on this one
// running clean on the fleet — this test is what you delete, deliberately, in
// the same commit that makes the move.
func TestDistkitPathCannotSwap(t *testing.T) {
	violations, err := scanForSwapEntryPoints(moduleRoot(t))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, v := range violations {
		t.Errorf("%s\ndistkit is DORMANT in this release: it may resolve and report, never write a binary.", v)
	}
}

// TestScanForSwapEntryPointsCatchesViolations pins the pin. A guard that
// silently stopped catching anything would be worse than no guard at all, so
// the scanner is pointed at a synthetic tree containing each way one could
// reach a swap.
func TestScanForSwapEntryPointsCatchesViolations(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "plain call",
			src:  "package p\nimport \"github.com/kfet/distkit\"\nfunc f() { _, _ = distkit.Update(nil, distkit.Config{}) }\n",
			want: "distkit.Update",
		},
		{
			name: "aliased import",
			src:  "package p\nimport dk \"github.com/kfet/distkit\"\nfunc f() { _ = dk.Apply(\"a\", \"b\") }\n",
			want: "distkit.Apply",
		},
		{
			name: "dot import hides the selector entirely",
			src:  "package p\nimport . \"github.com/kfet/distkit\"\nfunc f() { _ = Apply(\"a\", \"b\") }\n",
			want: "dot-imports distkit",
		},
		{
			name: "download",
			src:  "package p\nimport \"github.com/kfet/distkit\"\nfunc f() { _, _ = distkit.Download(nil, distkit.Config{}, nil, \"\") }\n",
			want: "distkit.Download",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(tc.src), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := scanForSwapEntryPoints(dir)
			if err != nil {
				t.Fatalf("scan: %v", err)
			}
			if len(got) != 1 || !strings.Contains(got[0], tc.want) {
				t.Fatalf("scan reported %v, want one violation mentioning %q", got, tc.want)
			}
		})
	}

	// And it must not cry wolf over the resolve-and-report calls the bridge
	// is actually built on.
	dir := t.TempDir()
	ok := "package p\nimport \"github.com/kfet/distkit\"\nfunc f() { _, _ = distkit.Check(nil, distkit.Config{}); _ = distkit.EnsureV(\"1\") }\n"
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(ok), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := scanForSwapEntryPoints(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("resolve-only code flagged: %v", got)
	}
}

// scanForSwapEntryPoints returns a human-readable violation line for every
// reference to a swapping distkit entry point under root.
func scanForSwapEntryPoints(root string) ([]string, error) {
	var violations []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// This file names the forbidden identifiers in strings, on purpose.
		if filepath.Base(path) == "distkit_dormant_test.go" {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			// A file that does not parse cannot be calling anything.
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		local := distkitImportName(file)
		if local == "." {
			// A dot-import would make Update(...) a bare identifier and
			// slip past the selector scan entirely.
			violations = append(violations,
				rel+" dot-imports distkit; the dormancy check cannot see through that")
			return nil
		}
		if local == "" {
			return nil
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || ident.Name != local {
				return true
			}
			if why, forbidden := swapEntryPoints[sel.Sel.Name]; forbidden {
				violations = append(violations, fmt.Sprintf("%s:%d references distkit.%s, which %s",
					rel, fset.Position(sel.Pos()).Line, sel.Sel.Name, why))
			}
			return true
		})
		return nil
	})
	return violations, err
}

// distkitImportName returns the local name distkit is imported under in file,
// or "" when it is not imported. An aliased import is honoured, so renaming
// the package is not a way around the check.
func distkitImportName(file *ast.File) string {
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != "github.com/kfet/distkit" {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return "distkit"
	}
	return ""
}

// TestDistkitCheckReportHasNoRelease pins the shape of the report-only result:
// it carries version strings and flags, not a release handle. Nothing
// downstream of `fir update -check` can be handed something it could download
// from.
func TestDistkitCheckReportHasNoRelease(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(root, "pkg", "update", "distkit.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var found bool
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "CheckReport" {
			return true
		}
		found = true
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			t.Fatalf("CheckReport is not a struct")
		}
		for _, f := range st.Fields.List {
			if _, scalar := f.Type.(*ast.Ident); !scalar {
				t.Errorf("CheckReport has a non-scalar field %v; it must not carry anything downloadable", f.Names)
			}
		}
		return false
	})
	if !found {
		t.Fatal("CheckReport not found in pkg/update/distkit.go")
	}
}

// moduleRoot walks up from the test's directory to the directory holding
// go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test directory")
		}
		dir = parent
	}
}
