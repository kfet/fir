//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCLI_SessionsSearchArchive runs the real binary against a read-only
// store given by FIR_SESSION_STORES and checks `fir sessions` sees it
// without writing to it.
func TestCLI_SessionsSearchArchive(t *testing.T) {
	agentDir := t.TempDir()
	zbox := t.TempDir()
	dir := filepath.Join(zbox, "--home-u-proj--")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	tr := filepath.Join(dir, "2026-09-01T10-00-00-0Z_arch-1.jsonl")
	body := `{"type":"session","version":3,"id":"arch-1","timestamp":"2026-09-01T10:00:00Z","cwd":"/home/u/proj"}` + "\n" +
		`{"type":"message","id":"e1","message":{"role":"user","content":"find the e2e-needle"}}` + "\n"
	if err := os.WriteFile(tr, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := `{"id":"arch-1","cwd":"/home/u/proj","name":"archived","message_count":1}`
	if err := os.WriteFile(tr+".meta.json", []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(tr, old, old)
	os.WriteFile(filepath.Join(dir, "bookmarks-arch-1.jsonl"), []byte(`{"_bookmark_note":"e2e-needle note"}`+"\n"), 0o644)
	os.Chmod(dir, 0o555) // any write attempt would fail loudly
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	env := map[string]string{"FIR_AGENT_DIR": agentDir, "FIR_SESSION_STORES": "zbox=" + zbox}

	out, code := runFir(t, "", 20*time.Second, env, "sessions", "dirs")
	if code != 0 || !strings.Contains(out, "zbox\t"+zbox+"\ttrue\ttrue") {
		t.Fatalf("dirs (exit %d):\n%s", code, out)
	}
	out, code = runFir(t, "", 20*time.Second, env, "sessions", "list", "--all")
	if code != 0 || !strings.HasPrefix(out, "zbox\t") || !strings.Contains(out, "\tarch-1\t") {
		t.Fatalf("list (exit %d):\n%s", code, out)
	}
	out, code = runFir(t, "", 20*time.Second, env, "sessions", "search", "e2e-needle")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || len(lines) != 2 || !strings.HasPrefix(lines[0], "zbox\tbookmark\tarch-1\t") ||
		!strings.HasPrefix(lines[1], "zbox\ttranscript\tarch-1\t"+tr+":2\t") {
		t.Fatalf("search (exit %d):\n%s", code, out)
	}
}
