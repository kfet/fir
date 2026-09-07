package update

import (
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
// not swap, while this proves no path can, including one added tomorrow.
//
// When the swap does move to distkit — a later release, gated on this one
// running clean on the fleet — this test is what you delete, deliberately, in
// the same commit that makes the move.
func TestDistkitPathCannotSwap(t *testing.T) {
	root := moduleRoot(t)
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
		local := distkitImportName(file)
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
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s:%d references distkit.%s, which %s.\n"+
					"distkit is DORMANT in this release: it may resolve and report, never write a binary.",
					rel, fset.Position(sel.Pos()).Line, sel.Sel.Name, why)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
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
// it carries version strings, not a release handle. Nothing downstream of
// `fir update -check` can be handed something it could download from.
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
			if _, isString := f.Type.(*ast.Ident); !isString {
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
