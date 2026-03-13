package extension

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/resources"
)

func TestCheckFrontmatter(t *testing.T) {
	tests := []struct {
		name       string
		cfg        ExtProcConfig
		caps       *InitResult
		wantEmpty  bool
		wantMissE  []string
		wantMissC  []string
		wantExtraE []string
	}{
		{
			name: "perfect match",
			cfg: ExtProcConfig{
				Name:   "test",
				Events: []string{"agent_end", "turn_end"},
				Commands: []resources.ExtensionFrontmatterCommand{
					{Name: "foo", Description: "Do foo"},
				},
			},
			caps: &InitResult{
				Events:   []string{"agent_end", "turn_end"},
				Commands: []CommandSpec{{Name: "foo", Description: "Do foo"}},
			},
			wantEmpty: true,
		},
		{
			name: "missing events",
			cfg: ExtProcConfig{
				Name:   "test",
				Events: []string{"agent_end"},
			},
			caps: &InitResult{
				Events: []string{"agent_end", "turn_end", "hook/tool_call"},
			},
			wantMissE: []string{"hook/tool_call", "turn_end"},
		},
		{
			name: "extra events in frontmatter",
			cfg: ExtProcConfig{
				Name:   "test",
				Events: []string{"agent_end", "session_start", "turn_end"},
			},
			caps: &InitResult{
				Events: []string{"agent_end"},
			},
			wantExtraE: []string{"session_start", "turn_end"},
		},
		{
			name: "missing commands",
			cfg: ExtProcConfig{
				Name: "test",
			},
			caps: &InitResult{
				Commands: []CommandSpec{{Name: "do-thing", Description: "Does thing"}},
			},
			wantMissC: []string{"do-thing"},
		},
		{
			name:      "no frontmatter no caps",
			cfg:       ExtProcConfig{Name: "test"},
			caps:      &InitResult{},
			wantEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mm := CheckFrontmatter(tt.cfg, tt.caps)
			if tt.wantEmpty && !mm.Empty() {
				t.Errorf("expected empty mismatch, got: %s", mm.Summary())
				return
			}
			if !tt.wantEmpty && mm.Empty() {
				t.Error("expected mismatch, got empty")
				return
			}
			if !sliceEqual(mm.MissingEvents, tt.wantMissE) {
				t.Errorf("MissingEvents = %v, want %v", mm.MissingEvents, tt.wantMissE)
			}
			if !sliceEqual(mm.MissingCommands, tt.wantMissC) {
				t.Errorf("MissingCommands = %v, want %v", mm.MissingCommands, tt.wantMissC)
			}
			if !sliceEqual(mm.ExtraEvents, tt.wantExtraE) {
				t.Errorf("ExtraEvents = %v, want %v", mm.ExtraEvents, tt.wantExtraE)
			}
		})
	}
}

func TestFixFrontmatter(t *testing.T) {
	original := `#!/usr/bin/env python3
# ---
# name: my-ext
# description: Does things
# modes: tui
# ---
"""My extension."""

import fir_ext

fir_ext.run(name="my-ext")
`
	dir := t.TempDir()
	path := filepath.Join(dir, "my-ext.py")
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	caps := &InitResult{
		Events:   []string{"agent_end", "agent_start"},
		Commands: []CommandSpec{{Name: "do-thing", Description: "Run a thing"}},
	}

	if err := FixFrontmatter(path, caps); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	// Verify the frontmatter was updated.
	fm := resources.ParseCommentFrontmatter(content)
	if !fm.Present {
		t.Fatal("frontmatter not present after fix")
	}
	if fm.Name != "my-ext" {
		t.Errorf("name = %q, want %q", fm.Name, "my-ext")
	}
	if fm.Description != "Does things" {
		t.Errorf("description = %q, want %q", fm.Description, "Does things")
	}
	if !sliceEqual(fm.Events, []string{"agent_end", "agent_start"}) {
		t.Errorf("events = %v, want [agent_end, agent_start]", fm.Events)
	}
	if len(fm.Commands) != 1 || fm.Commands[0].Name != "do-thing" {
		t.Errorf("commands = %v, want [{do-thing Run a thing}]", fm.Commands)
	}

	// Verify rest of file is preserved.
	if !strings.Contains(content, `fir_ext.run(name="my-ext")`) {
		t.Error("file body was corrupted")
	}
	if !strings.Contains(content, "modes: tui") {
		t.Error("existing modes field was lost")
	}
}

func TestFixFrontmatterUpdatesExisting(t *testing.T) {
	original := `#!/usr/bin/env python3
# ---
# name: my-ext
# events: old_event
# commands: old-cmd: Old command
# ---
code here
`
	dir := t.TempDir()
	path := filepath.Join(dir, "ext.py")
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	caps := &InitResult{
		Events:   []string{"new_event"},
		Commands: []CommandSpec{{Name: "new-cmd", Description: "New command"}},
	}

	if err := FixFrontmatter(path, caps); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	fm := resources.ParseCommentFrontmatter(string(data))
	if !sliceEqual(fm.Events, []string{"new_event"}) {
		t.Errorf("events = %v, want [new_event]", fm.Events)
	}
	if len(fm.Commands) != 1 || fm.Commands[0].Name != "new-cmd" {
		t.Errorf("commands = %v, want [{new-cmd New command}]", fm.Commands)
	}
	if !strings.Contains(string(data), "code here") {
		t.Error("file body was corrupted")
	}
}

func sliceEqual(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
