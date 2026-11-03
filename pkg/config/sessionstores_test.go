package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for in, want := range map[string]string{
		"~":        home,
		"~/a/b":    filepath.Join(home, "a/b"),
		"/abs":     "/abs",
		"rel":      "rel",
		"~other/x": "~other/x",
	} {
		if got := ExpandHome(in); got != want {
			t.Errorf("ExpandHome(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseSessionStoresEnv(t *testing.T) {
	got := ParseSessionStoresEnv(" zbox=/a/zbox , /b/bare,,x = ~/c ")
	want := []SessionStoreSettings{
		{Label: "zbox", Path: "/a/zbox"},
		{Label: "", Path: "/b/bare"},
		{Label: "x", Path: "~/c"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i].Label != want[i].Label || got[i].Path != want[i].Path {
			t.Errorf("[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestResolveSessionStores(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	f := false
	stores, warnings := ResolveSessionStores([]SessionStoreSettings{
		{Path: "~/traces/zbox"},                     // label from base name, ~ expanded
		{Path: "relative/dir", Label: "rel"},        // not absolute → skipped
		{Path: "/x/local", Label: "local"},          // reserved label
		{Path: "/x/other", Label: "zbox"},           // duplicate label
		{Path: home + "/traces/zbox/", Label: "z2"}, // duplicate path (cleaned)
		{Path: "/x/bad", Label: "bad label"},        // bad label
		{Path: "/x/rw", Label: "rw", ReadOnly: &f},  // kept, warned
	})
	if len(stores) != 2 {
		t.Fatalf("stores = %+v", stores)
	}
	if stores[0].Label != "zbox" || stores[0].Path != filepath.Join(home, "traces/zbox") {
		t.Errorf("stores[0] = %+v", stores[0])
	}
	if stores[1].Label != "rw" {
		t.Errorf("stores[1] = %+v", stores[1])
	}
	if len(warnings) != 6 {
		t.Errorf("warnings = %d: %q", len(warnings), warnings)
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "always read-only") {
		t.Errorf("no readOnly warning: %q", warnings)
	}
}

func TestLoadSessionStores(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	agentDir := t.TempDir()
	cwd := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(agentDir, "settings.json"), `{"sessionStores":[{"path":"~/fir-traces/zbox","label":"zbox"}]}`)
	write(filepath.Join(cwd, ConfigDirName, "settings.json"), `{"sessionStores":[{"path":"/evil","label":"evil"}]}`)

	t.Setenv(SessionStoresEnv, "") // restored on cleanup
	os.Unsetenv(SessionStoresEnv)
	stores, warnings := LoadSessionStores(cwd, agentDir)
	if len(stores) != 1 || stores[0].Label != "zbox" || stores[0].Path != filepath.Join(home, "fir-traces/zbox") {
		t.Fatalf("stores = %+v", stores)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "global-only") {
		t.Errorf("warnings = %q", warnings)
	}

	t.Setenv(SessionStoresEnv, "a=/tmp/a")
	stores, _ = LoadSessionStores(cwd, agentDir)
	if len(stores) != 1 || stores[0].Label != "a" {
		t.Errorf("env did not replace settings: %+v", stores)
	}

	t.Setenv(SessionStoresEnv, "")
	if stores, _ = LoadSessionStores(cwd, agentDir); len(stores) != 0 {
		t.Errorf("empty env should mean no stores: %+v", stores)
	}
}

func TestPathSettingsExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sm := NewInMemorySettingsManager(Settings{
		Skills:         []string{"~/skills", "rel"},
		ExtensionPaths: []string{"~/ext"},
		Themes:         []string{"~/themes"},
	})
	if got := sm.GetSkillPaths(); got[0] != filepath.Join(home, "skills") || got[1] != "rel" {
		t.Errorf("skills = %q", got)
	}
	if got := sm.GetExtensionPaths(); got[0] != filepath.Join(home, "ext") {
		t.Errorf("extensionPaths = %q", got)
	}
	if got := sm.GetThemePaths(); got[0] != filepath.Join(home, "themes") {
		t.Errorf("themes = %q", got)
	}
}
