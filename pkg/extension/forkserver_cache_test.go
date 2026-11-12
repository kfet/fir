package extension

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// toolExtScript returns an extension that registers one tool named tool. The
// tool name is derived inside the script from __main__ semantics so a wrong
// module setup (not __main__, wrong __file__, dataclass resolution through
// sys.modules) surfaces as a different tool name.
func toolExtScript(tool string) string {
	return `from __future__ import annotations
import dataclasses
import sys
import fir_ext

@dataclasses.dataclass
class Box:
    v: int

ok = (
    __name__ == "__main__"
    and sys.modules["__main__"].__dict__ is globals()
    and __file__ == sys.argv[0]
    and Box(1).v == 1
)

@fir_ext.tool(
    name="` + tool + `" if ok else "bad_main",
    description="t",
    parameters={"type": "object", "properties": {}},
)
def t(params, ctx):
    return {"content": [{"text": "x"}], "is_error": False}

fir_ext.run(name="cachetest")
`
}

func spawnTools(t *testing.T, fs *ForkServer, path string) []string {
	t.Helper()
	p := NewProcess(ExtProcConfig{Name: "cachetest", Path: path, Scope: "builtin"}, nil, nil)
	if err := p.StartForked(fs); err != nil {
		t.Fatalf("StartForked: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	caps, err := Handshake(p, "/tmp", nil, 10*time.Second)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	var names []string
	for _, tl := range caps.Tools {
		names = append(names, tl.Name)
	}
	return names
}

// TestForkServer_CodeCacheFollowsSource verifies the template's compiled-code
// cache runs the extension as a proper __main__ module, serves repeat spawns,
// and recompiles when the source changes.
func TestForkServer_CodeCacheFollowsSource(t *testing.T) {
	fs, _ := forkTestSetup(t)
	path := filepath.Join(t.TempDir(), "cachetest.py")
	if err := os.WriteFile(path, []byte(toolExtScript("one")), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if got := spawnTools(t, fs, path); len(got) != 1 || got[0] != "one" {
			t.Fatalf("spawn %d tools = %v, want [one]", i, got)
		}
	}
	// Different size => cache invalidated even if mtime granularity hides it.
	if err := os.WriteFile(path, []byte(toolExtScript("second")), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := spawnTools(t, fs, path); len(got) != 1 || got[0] != "second" {
		t.Fatalf("after edit tools = %v, want [second]", got)
	}
}

// TestForkServer_CompileErrorFallsBack verifies a source that fails to compile
// in the template still spawns (via runpy) and fails in the child, not the
// template, which keeps serving later spawns.
func TestForkServer_CompileErrorFallsBack(t *testing.T) {
	fs, _ := forkTestSetup(t)
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.py")
	if err := os.WriteFile(bad, []byte("def (:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := NewProcess(ExtProcConfig{Name: "bad", Path: bad, Scope: "builtin"}, nil, nil)
	if err := p.StartForked(fs); err != nil {
		t.Fatalf("StartForked: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	if _, err := Handshake(p, "/tmp", nil, 10*time.Second); err == nil {
		t.Fatal("handshake with a syntax-error extension succeeded")
	}
	good := filepath.Join(dir, "good.py")
	if err := os.WriteFile(good, []byte(toolExtScript("alive")), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := spawnTools(t, fs, good); len(got) != 1 || got[0] != "alive" {
		t.Fatalf("tools = %v, want [alive]", got)
	}
}

// TestForkServer_ReapsSelfExitedChildren verifies a child that exits on its
// own is reaped while the template idles (SIGCHLD), not left as a zombie until
// the next control message.
func TestForkServer_ReapsSelfExitedChildren(t *testing.T) {
	fs, _ := forkTestSetup(t)
	path := filepath.Join(t.TempDir(), "quit.py")
	if err := os.WriteFile(path, []byte("import sys\nsys.exit(0)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pid, conn, err := fs.Spawn("quit", path, nil)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer conn.Close()
	// Polls kernel state we cannot subscribe to: /proc/<pid> vanishes only once
	// the template reaps the child.
	statusPath := filepath.Join("/proc", strconv.Itoa(pid), "status")
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(statusPath)
		if err != nil {
			return // reaped
		}
		if !strings.Contains(string(b), "State:\tZ") && !pidAlive(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child %d not reaped (still a zombie or alive)", pid)
}
