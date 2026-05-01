package extension

import (
	"strings"
	"testing"
)

// Direct unit tests of the verb-table builder. We bypass the filesystem
// discovery layer by testing the pure collision/reservation logic on
// hand-crafted ExtProcConfigs.
func TestCLIVerbCollisionLogic(t *testing.T) {
	tests := []struct {
		name      string
		configs   []ExtProcConfig
		reserved  []string
		wantVerbs []string // expected verbs in alphabetical order
		wantErr   string   // substring match; empty = no error expected
	}{
		{
			name: "no verbs",
			configs: []ExtProcConfig{
				{Name: "a"},
			},
		},
		{
			name: "single ext, multiple verbs",
			configs: []ExtProcConfig{
				{Name: "observe", CLIVerbs: []string{"observe", "send"}},
			},
			wantVerbs: []string{"observe", "send"},
		},
		{
			name: "two extensions, no overlap",
			configs: []ExtProcConfig{
				{Name: "a", CLIVerbs: []string{"foo"}},
				{Name: "b", CLIVerbs: []string{"bar"}},
			},
			wantVerbs: []string{"bar", "foo"},
		},
		{
			name: "collision between two extensions",
			configs: []ExtProcConfig{
				{Name: "alpha", CLIVerbs: []string{"shared"}},
				{Name: "beta", CLIVerbs: []string{"shared"}},
			},
			wantErr: `verb "shared" claimed by both extensions`,
		},
		{
			name: "claims reserved verb",
			configs: []ExtProcConfig{
				{Name: "rogue", CLIVerbs: []string{"login"}},
			},
			reserved: []string{"login", "update"},
			wantErr:  `claims reserved verb "login"`,
		},
		{
			name: "blank entries skipped",
			configs: []ExtProcConfig{
				{Name: "a", CLIVerbs: []string{"   ", "real"}},
			},
			wantVerbs: []string{"real"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := buildVerbTable(tt.configs, tt.reserved)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := make([]string, 0, len(out))
			for _, b := range out {
				got = append(got, b.Verb)
			}
			if !equalStringSlices(got, tt.wantVerbs) {
				t.Fatalf("verbs = %v, want %v", got, tt.wantVerbs)
			}
		})
	}
}

func TestLookupCLIVerb_NoMatch(t *testing.T) {
	// The on-disk discovery path returns nil for an unknown verb.
	got, err := LookupCLIVerb("definitely-not-a-real-verb-xyz", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("LookupCLIVerb error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil binding, got %+v", got)
	}
}

func equalStringSlices(a, b []string) bool {
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
