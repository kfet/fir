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

// expectedBaseTools are the tools that must always be present regardless of
// MCP servers, extensions, or other mutations.
var expectedBaseTools = []string{"read", "bash", "edit", "write", "plan"}

func TestTool_AllBaseToolsPresent(t *testing.T) {
	dir := t.TempDir()
	out, code := runFirMockDir(t, dir, "LIST_TOOLS", 15*time.Second, "--print", "--no-session")
	if code != 0 {
		t.Logf("exit code %d (may be ok if stdin closed)", code)
	}
	assertNoPanic(t, out)
	assertAllToolsPresent(t, out, expectedBaseTools)
}

// TestTool_AllBaseToolsSurviveMCP verifies that loading MCP servers (which
// triggers OnToolsChanged and rebuilds the tool set) does not drop any
// base tools. This is the regression test for the UpdateTools refactor.
func TestTool_AllBaseToolsSurviveMCP(t *testing.T) {
	dir := setupMCPProject(t)
	out, code := runFirMockDir(t, dir, "LIST_TOOLS", 15*time.Second, "--print", "--no-session")
	if code != 0 {
		t.Logf("exit code %d (may be ok if stdin closed)", code)
	}
	assertNoPanic(t, out)
	// All base tools must survive, plus MCP tools should be added.
	assertAllToolsPresent(t, out, expectedBaseTools)
	if !strings.Contains(out, "mcp__") {
		t.Log("warning: no MCP tools found — MCP server may not have started in time")
	}
}

// assertAllToolsPresent checks that the LIST_TOOLS output contains all expected tool names.
func assertAllToolsPresent(t *testing.T, output string, expected []string) {
	t.Helper()
	// Find the TOOLS: line
	var toolLine string
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "TOOLS: ") {
			idx := strings.Index(line, "TOOLS: ")
			toolLine = line[idx+len("TOOLS: "):]
			break
		}
	}
	if toolLine == "" {
		t.Fatalf("no TOOLS: line found in output:\n%s", output)
	}
	tools := make(map[string]bool)
	for _, name := range strings.Split(toolLine, ",") {
		tools[strings.TrimSpace(name)] = true
	}
	for _, want := range expected {
		if !tools[want] {
			t.Errorf("tool %q missing from tool set: %v", want, tools)
		}
	}
}
