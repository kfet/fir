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
			want: ExtensionFrontmatter{Name: "my-ext", Description: "Does things", Builtin: true},
		},
		{
			name: "modes",
			input: `#!/usr/bin/env python3
# ---
# modes: tui, acp, json
# ---
`,
			want: ExtensionFrontmatter{Modes: []string{"tui", "acp", "json"}},
		},
		{
			name: "no shebang",
			input: `# ---
# builtin: true
# ---
`,
			want: ExtensionFrontmatter{Builtin: true},
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
			want: ExtensionFrontmatter{Name: "dev-only"},
		},
		{
			name: "demo",
			input: `#!/usr/bin/env python3
# ---
# demo: true
# ---
`,
			want: ExtensionFrontmatter{Demo: true},
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
