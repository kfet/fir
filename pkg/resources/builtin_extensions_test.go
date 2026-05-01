package resources

import (
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
