package extension

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/sections"
)

func writeExt(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name+".py")
	src := "#!/usr/bin/env python3\n# ---\n# name: " + name + "\n# ---\n"
	if err := os.WriteFile(p, []byte(src), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func newSectionsManager(t *testing.T) (*Manager, string) {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	m := NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.SetTrustStore(NewTrustStoreWithPath(filepath.Join(xdg, "trust.json")))
	return m, xdg
}

func TestSectionPolicy_TrustAndEnablement(t *testing.T) {
	m, xdg := newSectionsManager(t)
	project := t.TempDir()
	writeExt(t, filepath.Join(xdg, "fir", "extensions"), "globalext")
	writeExt(t, filepath.Join(xdg, "fir", "extensions"), "offext")
	trusted := writeExt(t, filepath.Join(project, ".fir", "extensions"), "trustedproj")
	writeExt(t, filepath.Join(project, ".fir", "extensions"), "untrustedproj")
	hash, _ := ComputeHash(trusted)
	if err := m.trust.RecordTrust(project, "trustedproj", hash); err != nil {
		t.Fatal(err)
	}
	m.SetDisabledNames([]string{"offext"})

	pol, err := m.SectionPolicy(project)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{
		"mood":          true, // builtin
		"globalext":     true,
		"trustedproj":   true,
		"untrustedproj": false,
		"offext":        false, // disabled
		"nosuchext":     false,
	} {
		if got := pol.Allowed(name); got != want {
			t.Errorf("Allowed(%s) = %v, want %v", name, got, want)
		}
	}
	if !pol.Installed["offext"] || !pol.Installed["untrustedproj"] {
		t.Error("disabled/untrusted extensions are still installed")
	}
}

func TestPruneSections_Lifecycle(t *testing.T) {
	m, xdg := newSectionsManager(t)
	project := t.TempDir()
	writeExt(t, filepath.Join(xdg, "fir", "extensions"), "offext")
	m.SetDisabledNames([]string{"offext"})
	// Trusted project extension of a different project.
	if err := m.trust.RecordTrust("/elsewhere", "otherproj", "h"); err != nil {
		t.Fatal(err)
	}
	store := sections.Default()
	for _, n := range []string{"mood", "offext", "gone", "otherproj"} {
		if err := store.Set(n, n+" text"); err != nil {
			t.Fatal(err)
		}
	}
	pol, err := m.SectionPolicy(project)
	if err != nil {
		t.Fatal(err)
	}
	m.PruneSections(store, pol)
	all, _ := store.ReadAll()
	var names []string
	for _, s := range all {
		names = append(names, s.Name)
	}
	if got := strings.Join(names, ","); got != "mood,offext,otherproj" {
		t.Fatalf("after prune: %s (uninstalled 'gone' must be deleted, disabled kept)", got)
	}
}

func TestBridge_SetClearSection_RPC(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	b, extCodec := pipePair(&InitResult{})
	b.proc.cfg.Name = "mood"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = b.Run(ctx, newMockAPI()) }()

	call := func(id int, method, params string) *Response {
		t.Helper()
		raw := json.RawMessage(params)
		_ = extCodec.WriteRequest(id, method, &raw)
		msg, err := extCodec.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		return msg.(*Response)
	}

	if r := call(1, "set_section", `{"text":"[SYS_EXT]lessons"}`); r.Error != nil {
		t.Fatalf("set_section: %v", r.Error)
	}
	all, _ := sections.Default().ReadAll()
	if len(all) != 1 || all[0].Name != "mood" || all[0].Text != "lessons" {
		t.Fatalf("got %+v", all)
	}

	big, _ := json.Marshal(map[string]string{"text": strings.Repeat("x", 5000)})
	if r := call(2, "set_section", string(big)); r.Error == nil {
		t.Fatal("expected over-cap error")
	}
	all, _ = sections.Default().ReadAll()
	if len(all) != 1 || all[0].Text != "lessons" {
		t.Fatalf("previous version not kept: %+v", all)
	}

	if r := call(3, "clear_section", `{}`); r.Error != nil {
		t.Fatalf("clear_section: %v", r.Error)
	}
	if all, _ = sections.Default().ReadAll(); len(all) != 0 {
		t.Fatalf("expected cleared, got %+v", all)
	}
}
