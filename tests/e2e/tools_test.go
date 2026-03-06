//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Section 4: Tool execution tests

func TestTool_Read(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "testfile.txt"), []byte("E2E_TEST_CONTENT_12345"), 0o644)
	out, code := runFirMockDir(t, dir, "READ_FILE testfile.txt", 15*time.Second, "--print", "--no-session")
	if code != 0 {
		t.Logf("exit code %d (may be ok if stdin closed)", code)
	}
	assertNoPanic(t, out)
	if !strings.Contains(out, "MOCK_TOOL_DONE") {
		t.Fatalf("expected MOCK_TOOL_DONE (indicating read tool was called) in output: %s", out)
	}
}

func TestTool_Write(t *testing.T) {
	dir := t.TempDir()
	out, code := runFirMockDir(t, dir, "WRITE_FILE output.txt WRITTEN_BY_FIR", 15*time.Second, "--print", "--no-session")
	if code != 0 {
		t.Logf("exit code %d (may be ok if stdin closed)", code)
	}
	assertNoPanic(t, out)
	content, err := os.ReadFile(filepath.Join(dir, "output.txt"))
	if err != nil {
		t.Fatalf("output.txt not created: %v\noutput: %s", err, out)
	}
	if !strings.Contains(string(content), "WRITTEN_BY_FIR") {
		t.Fatalf("expected WRITTEN_BY_FIR in file, got: %s", string(content))
	}
}

func TestTool_Bash(t *testing.T) {
	dir := t.TempDir()
	out, code := runFirMockDir(t, dir, "RUN_BASH echo BASH_E2E_OK", 15*time.Second, "--print", "--no-session")
	if code != 0 {
		t.Logf("exit code %d (may be ok if stdin closed)", code)
	}
	assertNoPanic(t, out)
	if !strings.Contains(out, "MOCK_TOOL_DONE") {
		t.Fatalf("expected MOCK_TOOL_DONE (indicating bash tool was called) in output: %s", out)
	}
}
