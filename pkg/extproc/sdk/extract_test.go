package sdk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureExtracted(t *testing.T) {
	tmp := t.TempDir()

	// Override cacheDir so we don't touch ~/.cache.
	orig := cacheDir
	cacheDir = func() (string, error) { return tmp, nil }
	t.Cleanup(func() { cacheDir = orig })

	// First call: extracts files.
	base, err := EnsureExtracted()
	if err != nil {
		t.Fatalf("EnsureExtracted: %v", err)
	}
	wantBase := filepath.Join(tmp, SDKVersion)
	if base != wantBase {
		t.Fatalf("base = %q, want %q", base, wantBase)
	}

	// fir_ext.py must exist.
	pyFile := filepath.Join(base, "python", "fir_ext.py")
	data, err := os.ReadFile(pyFile)
	if err != nil {
		t.Fatalf("read fir_ext.py: %v", err)
	}
	if !strings.Contains(string(data), "def run(") {
		t.Error("fir_ext.py does not contain expected content")
	}

	// Marker must exist.
	marker := filepath.Join(base, ".extracted")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("marker missing: %v", err)
	}
}

func TestEnsureExtractedIdempotent(t *testing.T) {
	tmp := t.TempDir()
	orig := cacheDir
	cacheDir = func() (string, error) { return tmp, nil }
	t.Cleanup(func() { cacheDir = orig })

	p1, err := EnsureExtracted()
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	p2, err := EnsureExtracted()
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if p1 != p2 {
		t.Fatalf("paths differ: %q vs %q", p1, p2)
	}
}

func TestSDKEnv(t *testing.T) {
	env := SDKEnv("/base/path")
	if len(env) != 3 {
		t.Fatalf("len = %d, want 3", len(env))
	}
	want := []string{
		"PYTHONPATH=/base/path/python",
		"NODE_PATH=/base/path/node",
		"RUBYLIB=/base/path/ruby",
	}
	for i, w := range want {
		if env[i] != w {
			t.Errorf("env[%d] = %q, want %q", i, env[i], w)
		}
	}
}
