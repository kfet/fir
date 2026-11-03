package sdk

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestEnsureExtracted(t *testing.T) {
	tmp := t.TempDir()

	orig := cacheDir
	cacheDir = func() (string, error) { return tmp, nil }
	t.Cleanup(func() { cacheDir = orig })

	base, err := EnsureExtracted()
	if err != nil {
		t.Fatalf("EnsureExtracted: %v", err)
	}

	// Should be a content-addressed subdirectory.
	if filepath.Dir(base) != tmp {
		t.Fatalf("base parent = %q, want %q", filepath.Dir(base), tmp)
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

	// Only one directory (plus no stale temp dirs).
	entries, _ := os.ReadDir(tmp)
	if len(entries) != 1 {
		t.Errorf("expected 1 cache entry, got %d", len(entries))
	}
}

func TestEmbeddedHash(t *testing.T) {
	h1, err := embeddedHash()
	if err != nil {
		t.Fatal(err)
	}
	h2, err := embeddedHash()
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("hash not deterministic: %q vs %q", h1, h2)
	}
	if len(h1) != 16 {
		t.Fatalf("hash length = %d, want 16", len(h1))
	}
}

func TestSDKEnv(t *testing.T) {
	env := SDKEnv("/base/path")
	// Under `go test` the host is a .test binary, so no FIR_BIN is set.
	if len(env) != 4 {
		t.Fatalf("len = %d, want 4: %v", len(env), env)
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
	// FIR_HOST_PID carries the fir host pid to extensions (see SDKEnv doc).
	wantHostPid := "FIR_HOST_PID=" + strconv.Itoa(os.Getpid())
	if env[3] != wantHostPid {
		t.Errorf("env[3] = %q, want %q", env[3], wantHostPid)
	}
}

func TestSDKEnvFirBin(t *testing.T) {
	orig := executable
	t.Cleanup(func() { executable = orig })
	t.Setenv("FIR_BIN", "")

	executable = func() (string, error) { return "/usr/local/bin/fir", nil }
	env := SDKEnv("/b")
	if got := env[len(env)-1]; got != "FIR_BIN=/usr/local/bin/fir" {
		t.Errorf("last env = %q, want FIR_BIN", got)
	}

	executable = func() (string, error) { return "/tmp/go-build/sdk.test", nil }
	if env := SDKEnv("/b"); len(env) != 4 {
		t.Errorf("a .test host must not set FIR_BIN: %v", env)
	}

	executable = func() (string, error) { return "", os.ErrNotExist }
	if env := SDKEnv("/b"); len(env) != 4 {
		t.Errorf("unknown executable must not set FIR_BIN: %v", env)
	}

	t.Setenv("FIR_BIN", "/custom/fir")
	executable = func() (string, error) { return "/usr/local/bin/fir", nil }
	if env := SDKEnv("/b"); len(env) != 4 {
		t.Errorf("inherited FIR_BIN must be left alone: %v", env)
	}
}
