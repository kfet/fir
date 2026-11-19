package extension

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLooksDisabled(t *testing.T) {
	cases := map[string]bool{
		"gcal.py": false, "gcal": false, "my.tool.py": false, "foo.test.sh": false, "gcal.v2": false,
		"gcal.py.migrated-to-mcp": true, "foo.sh.bak": true, "a.ts.old": true, "x.PY.bak": true,
		"gcal.disabled": true, "gcal.bak": true, "gcal.orig": true, "gcal.old": true,
		"gcal.py~": true, "gcal~": true, "foo.disabled": true, "x.mjs.off": true,
	}
	for name, want := range cases {
		if got := looksDisabled(name); got != want {
			t.Errorf("looksDisabled(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestSetExtraDisableSuffixes(t *testing.T) {
	t.Cleanup(func() { SetExtraDisableSuffixes(nil) })
	if looksDisabled("gcal.off") {
		t.Fatal("gcal.off disabled before config")
	}
	SetExtraDisableSuffixes([]string{" .OFF ", ""})
	if !looksDisabled("gcal.off") || !looksDisabled("gcal.bak") {
		t.Fatal("extra suffix or default lost")
	}
	SetExtraDisableSuffixes(nil)
	if looksDisabled("gcal.off") {
		t.Fatal("extras not replaced")
	}
}

func TestScanExtDir_SkipsDisabled(t *testing.T) {
	dir := t.TempDir()
	body := []byte("#!/usr/bin/env python3\n# ---\n# name: x\n# ---\n")
	for _, n := range []string{"gcal.py", "gcal.py.migrated-to-mcp", "old.disabled"} {
		if err := os.WriteFile(filepath.Join(dir, n), body, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sub := filepath.Join(dir, "foo.disabled")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "main.py"), body, 0o755); err != nil {
		t.Fatal(err)
	}
	byName := map[string]ExtProcConfig{}
	if err := scanExtDir(dir, "global", byName); err != nil {
		t.Fatal(err)
	}
	if len(byName) != 1 {
		t.Fatalf("want only gcal loaded, got %v", byName)
	}
	if _, ok := byName["gcal"]; !ok {
		t.Fatalf("gcal not loaded: %v", byName)
	}
}
