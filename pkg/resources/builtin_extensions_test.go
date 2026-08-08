package resources

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseCommentFrontmatter(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  ExtensionFrontmatter
	}{
		{
			name: "basic",
			input: `#!/usr/bin/env python3
# ---
# name: my-ext
# description: Does things
# builtin: true
# ---
import sys
`,
			want: ExtensionFrontmatter{Name: "my-ext", Description: "Does things", Builtin: true, Present: true},
		},
		{
			name: "modes",
			input: `#!/usr/bin/env python3
# ---
# modes: tui, acp, json
# ---
`,
			want: ExtensionFrontmatter{Modes: []string{"tui", "acp", "json"}, Present: true},
		},
		{
			name: "no shebang",
			input: `# ---
# builtin: true
# ---
`,
			want: ExtensionFrontmatter{Builtin: true, Present: true},
		},
		{
			name: "no frontmatter",
			input: `#!/usr/bin/env python3
import sys
`,
			want: ExtensionFrontmatter{},
		},
		{
			name: "no closing delimiter",
			input: `# ---
# builtin: true
import sys
`,
			want: ExtensionFrontmatter{},
		},
		{
			name: "not builtin",
			input: `#!/usr/bin/env python3
# ---
# name: dev-only
# ---
`,
			want: ExtensionFrontmatter{Name: "dev-only", Present: true},
		},
		{
			name: "cli_verbs (plural)",
			input: `# ---
# cli_verbs: observe, send
# ---
`,
			want: ExtensionFrontmatter{CLIVerbs: []string{"observe", "send"}, Present: true},
		},
		{
			name: "cli_verb (singular alias)",
			input: `# ---
# cli_verb: deploy
# ---
`,
			want: ExtensionFrontmatter{CLIVerbs: []string{"deploy"}, Present: true},
		},
		{
			name: "cli_verbs bracketed and quoted",
			input: `# ---
# cli_verbs: ["foo", 'bar']
# ---
`,
			want: ExtensionFrontmatter{CLIVerbs: []string{"foo", "bar"}, Present: true},
		},
		{
			name: "explicit",
			input: `#!/usr/bin/env python3
# ---
# name: demo
# explicit: true
# ---
`,
			want: ExtensionFrontmatter{Name: "demo", Explicit: true, Present: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseCommentFrontmatter(tt.input)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
func TestBuiltinExtensionsHash_Stable(t *testing.T) {
	h1 := BuiltinExtensionsHash()
	h2 := BuiltinExtensionsHash()
	if h1 == "" {
		t.Fatal("BuiltinExtensionsHash() returned empty")
	}
	if h1 != h2 {
		t.Fatalf("BuiltinExtensionsHash() not stable: %q vs %q", h1, h2)
	}
	if len(h1) != 16 {
		t.Fatalf("BuiltinExtensionsHash() length = %d, want 16", len(h1))
	}
}

func TestExtractBuiltinExtensionsTo_ReExtractsWhenIncomplete(t *testing.T) {
	base := t.TempDir()

	dir, err := extractBuiltinExtensionsTo(base)
	if err != nil {
		t.Fatalf("first extract: %v", err)
	}
	demo := filepath.Join(dir, "demo.py")
	if _, err := os.Stat(demo); err != nil {
		t.Fatalf("demo.py missing after extract: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".complete")); err != nil {
		t.Fatalf(".complete sentinel missing after extract: %v", err)
	}

	// Second call must reuse the cache (sentinel present) and not error.
	dir2, err := extractBuiltinExtensionsTo(base)
	if err != nil || dir2 != dir {
		t.Fatalf("reuse failed: dir=%q dir2=%q err=%v", dir, dir2, err)
	}

	// Simulate macOS partial temp purge: wipe extension files and the
	// sentinel, leave the directory itself intact (this is exactly the
	// state that produced the original 'no such file or directory'
	// startup failures).
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			t.Fatalf("simulate purge: %v", err)
		}
	}

	dir3, err := extractBuiltinExtensionsTo(base)
	if err != nil {
		t.Fatalf("re-extract after purge: %v", err)
	}
	if dir3 != dir {
		t.Fatalf("expected same hashed dir, got %q vs %q", dir3, dir)
	}
	if _, err := os.Stat(filepath.Join(dir3, "demo.py")); err != nil {
		t.Fatalf("demo.py missing after re-extract: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir3, ".complete")); err != nil {
		t.Fatalf(".complete missing after re-extract: %v", err)
	}
}

// forge.py repairs extensions it writes by injecting exactly this header.
// Its Python-side parser mirrors ParseCommentFrontmatter; if the two ever
// drift, forge would happily write files that fir silently skips — the very
// bug this shape exists to prevent. Keep this literal in sync with
// _inject_frontmatter in pkg/resources/builtin_extensions/forge.py.
func TestParseCommentFrontmatter_ForgeInjectedHeader(t *testing.T) {
	src := "#!/usr/bin/env python3\n" +
		"# ---\n" +
		"# name: skill_judge\n" +
		"# description: skill_judge extension (forged in-session)\n" +
		"# ---\n" +
		"import fir_ext\n"

	fm := ParseCommentFrontmatter(src)
	if !fm.Present {
		t.Fatal("forge-injected frontmatter must parse as present")
	}
	if fm.Name != "skill_judge" {
		t.Fatalf("name = %q, want skill_judge", fm.Name)
	}
	if fm.Builtin {
		t.Fatal("forge-injected frontmatter must not be builtin")
	}
	if len(fm.Modes) != 0 {
		t.Fatalf("forge injects no modes (all modes), got %v", fm.Modes)
	}
}
