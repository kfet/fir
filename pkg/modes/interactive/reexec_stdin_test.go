package interactive

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
)

// ==========================================================================
// Regression tests: restoreStdinBlocking must be called in critical paths
//
// Background: Go's runtime sets stdin to O_NONBLOCK for its I/O poller.
// syscall.Exec (used by /reexec) replaces the process without runtime
// cleanup, so stdin stays non-blocking.  The new process's eventual exit
// leaves the parent shell's stdin non-blocking — the shell reads EAGAIN,
// interprets it as EOF, and exits (closing the terminal).
//
// The fix is to call restoreStdinBlocking() in two places:
//   1. ReexecIfRequested() — before syscall.Exec
//   2. Cleanup()           — on normal exit (belt-and-suspenders)
//
// This bug has regressed TWICE.  The tests below parse the Go source and
// assert that the calls are present, so any refactor that drops them will
// immediately fail CI.
// ==========================================================================

// TestRestoreStdinBlocking_CalledInReexecIfRequested parses commands.go and
// asserts that ReexecIfRequested contains a call to restoreStdinBlocking.
func TestRestoreStdinBlocking_CalledInReexecIfRequested(t *testing.T) {
	assertFuncCallsHelper(t, "commands.go", "ReexecIfRequested", "restoreStdinBlocking")
}

// TestRestoreStdinBlocking_CalledInCleanup parses mode.go and asserts that
// Cleanup contains a call to restoreStdinBlocking.
func TestRestoreStdinBlocking_CalledInCleanup(t *testing.T) {
	assertFuncCallsHelper(t, "mode.go", "Cleanup", "restoreStdinBlocking")
}

// assertFuncCallsHelper parses filename in the current package directory and
// fails the test if outerFunc does not contain a call to callee.
func assertFuncCallsHelper(t *testing.T, filename, outerFunc, callee string) {
	t.Helper()

	src, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("read %s: %v", filename, err)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}

	// Find the function (or method) declaration by name.
	var funcDecl *ast.FuncDecl
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fd.Name.Name == outerFunc {
			funcDecl = fd
			break
		}
	}
	if funcDecl == nil {
		t.Fatalf("function %s not found in %s", outerFunc, filename)
	}

	// Walk the function body looking for a call to callee.
	found := false
	ast.Inspect(funcDecl.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if ok && ident.Name == callee {
			found = true
			return false
		}
		return true
	})

	if !found {
		t.Errorf(
			"REGRESSION: %s() in %s must call %s() to prevent "+
				"stdin O_NONBLOCK from leaking to the parent shell "+
				"(causes terminal to close after /reexec). "+
				"See commit 4dc2f21 and 24d50f9 for context.",
			outerFunc, filename, callee,
		)
	}
}
