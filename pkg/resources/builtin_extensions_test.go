package resources

import (
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/ai"
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
			name: "demo",
			input: `#!/usr/bin/env python3
# ---
# demo: true
# ---
`,
			want: ExtensionFrontmatter{Demo: true, Present: true},
		},
		{
			name: "events",
			input: `#!/usr/bin/env python3
# ---
# name: my-ext
# events: agent_start, agent_end, hook/tool_call
# ---
`,
			want: ExtensionFrontmatter{Name: "my-ext", Events: []string{"agent_start", "agent_end", "hook/tool_call"}, Present: true},
		},
		{
			name: "commands",
			input: `#!/usr/bin/env python3
# ---
# name: my-ext
# commands: do-thing: Run a thing, other: Something else
# ---
`,
			want: ExtensionFrontmatter{
				Name:     "my-ext",
				Commands: []ExtensionFrontmatterCommand{{Name: "do-thing", Description: "Run a thing"}, {Name: "other", Description: "Something else"}},
				Present:  true,
			},
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

// TestBuiltinExtensionFrontmatterCompleteness verifies that each builtin
// extension declares all its subscribed events, hooks, and commands in its
// comment frontmatter. This ensures lazy loading works correctly — extensions
// that omit declarations will be started eagerly (slower) or miss events.
func TestBuiltinExtensionFrontmatterCompleteness(t *testing.T) {
	entries, err := BuiltinExtensionsFS.ReadDir("builtin_extensions")
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := BuiltinExtensionsFS.ReadFile("builtin_extensions/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		src := string(data)
		fm := ParseCommentFrontmatter(src)
		if !fm.Present {
			continue // non-extension file
		}
		// Demo extensions are checked too — they should represent
		// how production extensions look.

		t.Run(fm.Name, func(t *testing.T) {
			// Extract @fir_ext.on("...") event/hook subscriptions from source.
			usedEvents := extractPythonDecorators(src, "on")
			// Extract @fir_ext.command(...) declarations from source.
			usedCommands := extractPythonCommands(src)

			// Check that all used events are declared in frontmatter.
			declaredEvents := make(map[string]bool, len(fm.Events))
			for _, ev := range fm.Events {
				declaredEvents[ev] = true
			}
			for _, ev := range usedEvents {
				if !declaredEvents[ev] {
					t.Errorf("extension %q subscribes to event %q via @fir_ext.on() but does not declare it in frontmatter events", fm.Name, ev)
				}
			}

			// Check that all declared events are actually used.
			usedEventSet := make(map[string]bool, len(usedEvents))
			for _, ev := range usedEvents {
				usedEventSet[ev] = true
			}
			for _, ev := range fm.Events {
				if !usedEventSet[ev] {
					t.Errorf("extension %q declares event %q in frontmatter but never subscribes to it via @fir_ext.on()", fm.Name, ev)
				}
			}

			// Check that all used commands are declared in frontmatter.
			declaredCmds := make(map[string]bool, len(fm.Commands))
			for _, cmd := range fm.Commands {
				declaredCmds[cmd.Name] = true
			}
			for _, cmd := range usedCommands {
				if !declaredCmds[cmd] {
					t.Errorf("extension %q registers command %q via @fir_ext.command() but does not declare it in frontmatter commands", fm.Name, cmd)
				}
			}
		})
	}
}

// extractPythonDecorators extracts string arguments from @fir_ext.<decorator>("...") calls.
func extractPythonDecorators(src, decorator string) []string {
	var results []string
	// Match @fir_ext.<decorator>("...") patterns.
	// Simple line-by-line scan is sufficient for our extensions.
	lines := strings.Split(src, "\n")
	prefix := "@fir_ext." + decorator + "("
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		// Extract the string argument: @fir_ext.on("event_name")
		rest := strings.TrimPrefix(line, prefix)
		// Find quoted string
		for _, q := range []byte{'"', '\''} {
			idx := strings.IndexByte(rest, q)
			if idx < 0 {
				continue
			}
			end := strings.IndexByte(rest[idx+1:], q)
			if end < 0 {
				continue
			}
			results = append(results, rest[idx+1:idx+1+end])
			break
		}
	}
	return results
}

// extractPythonCommands extracts command names from @fir_ext.command(...) calls.
// Handles both single-line and multi-line forms:
//
//	@fir_ext.command(name="foo", ...)
//	@fir_ext.command(
//	    name="foo",
//	    ...
//	)
func extractPythonCommands(src string) []string {
	var results []string
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "@fir_ext.command(") {
			continue
		}
		// Collect the decorator text (may span multiple lines until closing paren).
		block := trimmed
		if !strings.Contains(block, ")") {
			for j := i + 1; j < len(lines) && j < i+10; j++ {
				block += " " + strings.TrimSpace(lines[j])
				if strings.Contains(lines[j], ")") {
					break
				}
			}
		}
		// Find name="..." or name='...'
		nameIdx := strings.Index(block, "name=")
		if nameIdx < 0 {
			continue
		}
		rest := block[nameIdx+5:]
		for _, q := range []byte{'"', '\''} {
			idx := strings.IndexByte(rest, q)
			if idx < 0 {
				continue
			}
			end := strings.IndexByte(rest[idx+1:], q)
			if end < 0 {
				continue
			}
			results = append(results, rest[idx+1:idx+1+end])
			break
		}
	}
	return results
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

// TestAsideDefaultAdvisor_TracksHighestAnthropicOpus guards the aside
// extension's default advisor.  When no aside.json exists the extension
// falls back to a hard-coded model spec — that spec must always point at
// the strongest Anthropic Opus baked into fir's bundled model registry.
//
// The test reads _DEFAULT_ADVISOR_SPEC from aside.py and compares it to
// the highest claude-opus-<major>-<minor> id registered under the
// "anthropic" provider.  When this fails, bump the constant in aside.py
// to the value the test reports.
//
// We intentionally ignore date-suffixed aliases (e.g. claude-opus-4-1-20250805,
// claude-opus-4-20250514) — those are short-lived; the bare X-Y form is the
// long-lived alias users pin to.
func TestAsideDefaultAdvisor_TracksHighestAnthropicOpus(t *testing.T) {
	// Read the constant from the embedded aside.py.
	data, err := BuiltinExtensionsFS.ReadFile("builtin_extensions/aside.py")
	if err != nil {
		t.Fatalf("read embedded aside.py: %v", err)
	}
	specRe := regexp.MustCompile(`_DEFAULT_ADVISOR_SPEC\s*=\s*"([^"]+)"`)
	m := specRe.FindStringSubmatch(string(data))
	if m == nil {
		t.Fatal("aside.py: _DEFAULT_ADVISOR_SPEC literal not found")
	}
	got := m[1]

	// Find the highest claude-opus-<major>-<minor> in the live registry.
	// Bare X-Y form only — minor must be 1-2 digits so date-stamped ids
	// like claude-opus-4-1-20250805 are filtered out by the missing third
	// dash component, and claude-opus-4-20250514 fails the 1-2 digit cap.
	want := highestAnthropicOpusInRegistry(t)
	if want == "" {
		t.Fatal("no claude-opus-<major>-<minor> models registered under anthropic provider")
	}
	wantSpec := "anthropic/" + want

	if got != wantSpec {
		t.Fatalf(
			"aside.py _DEFAULT_ADVISOR_SPEC out of sync with model registry:\n"+
				"  got:  %s\n"+
				"  want: %s\n"+
				"\nFix: edit pkg/resources/builtin_extensions/aside.py and update _DEFAULT_ADVISOR_SPEC to %q.",
			got, wantSpec, wantSpec,
		)
	}
}

// highestAnthropicOpusInRegistry finds the largest claude-opus-<major>-<minor>
// id registered under the "anthropic" provider in the live ai model registry.
// Returns "" when no such model exists.
func highestAnthropicOpusInRegistry(t *testing.T) string {
	t.Helper()
	// Bare X-Y form; minor capped at 2 digits to reject date stamps.
	idRe := regexp.MustCompile(`^claude-opus-(\d+)-(\d{1,2})$`)
	bestMajor, bestMinor := -1, -1
	bestID := ""
	for _, m := range ai.GetModels(ai.ProviderAnthropic) {
		match := idRe.FindStringSubmatch(m.ID)
		if match == nil {
			continue
		}
		major, _ := strconv.Atoi(match[1])
		minor, _ := strconv.Atoi(match[2])
		if major > bestMajor || (major == bestMajor && minor > bestMinor) {
			bestMajor, bestMinor, bestID = major, minor, m.ID
		}
	}
	return bestID
}
