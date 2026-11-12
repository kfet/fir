package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAuthStorageMode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(AuthStorageEnv, "")
	if m, err := LoadAuthStorageMode(dir); err != nil || m != AuthStorageFile {
		t.Fatalf("default = %q, %v", m, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"auth":{"storage":"Keyring"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if m, _ := LoadAuthStorageMode(dir); m != AuthStorageKeyring {
		t.Fatalf("settings = %q", m)
	}
	t.Setenv(AuthStorageEnv, "auto")
	if m, _ := LoadAuthStorageMode(dir); m != AuthStorageAuto {
		t.Fatalf("env = %q", m)
	}
	t.Setenv(AuthStorageEnv, "nope")
	if _, err := LoadAuthStorageMode(dir); err == nil {
		t.Fatal("want error for invalid mode")
	}
}
