package resources

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// TestAnthropicAuth_ToolNameMap pins the exact fir → Claude Code tool-name
// mapping shipped by the anthropic-auth builtin extension.
//
// The map is consumed by Anthropic OAuth (Claude Pro/Max) sessions: every
// fir tool name in our request body is rewritten to its Claude Code
// counterpart so the OAuth backend recognises us as Claude-Code-shaped.
//
// Two regressions in particular this test guards against:
//
//  1. Mapping a fir tool to a name Claude Code does not actually have
//     (e.g. the historical "Monitor"/"TaskStop" placeholders that were
//     never part of CC's surface).
//  2. Drift between this map and the canonical-CC-surface comment in the
//     same file, which itself is the documentation contract.
//
// Because the source is a small embedded Python file with a flat dict
// literal, we extract the mapping with a narrow regex rather than running
// Python. This keeps the test hermetic.
func TestAnthropicAuth_ToolNameMap(t *testing.T) {
	src := readBuiltinExtensionSource(t, "anthropic_auth.py")
	got := extractToolNameMap(t, src)

	want := map[string]string{
		"read":        "Read",
		"write":       "Write",
		"edit":        "Edit",
		"bash":        "Bash",
		"grep":        "Grep",
		"find":        "Glob",
		"bash_output": "BashOutput",
		"bash_kill":   "KillShell",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("anthropic_auth.py register_tool_name_map mismatch.\n got: %v\nwant: %v", got, want)
	}

	// Defence-in-depth: explicitly forbid the historical wrong values that
	// previously shipped (bash_output → "Monitor", bash_kill → "TaskStop").
	// Even after a future intentional rename of the canonical map (which
	// would update `want` above), these two names must never reappear.
	for k, v := range got {
		switch v {
		case "Monitor", "TaskStop":
			t.Errorf("tool %q maps to %q, which is not part of Claude Code's tool surface", k, v)
		}
	}
}

// TestAnthropicAuth_BackgroundBashTools_Pin sharply pins the two values that
// have actually changed and were the subject of the regression. Even if
// future edits expand the map (e.g. a new fir tool is added), these two
// must keep mapping to the canonical Claude Code names.
func TestAnthropicAuth_BackgroundBashTools_Pin(t *testing.T) {
	src := readBuiltinExtensionSource(t, "anthropic_auth.py")
	got := extractToolNameMap(t, src)

	cases := []struct {
		fir, cc string
	}{
		{"bash_output", "BashOutput"},
		{"bash_kill", "KillShell"},
	}
	for _, c := range cases {
		if got[c.fir] != c.cc {
			t.Errorf("anthropic_auth.py: tool %q must map to %q (the Claude Code name); got %q",
				c.fir, c.cc, got[c.fir])
		}
	}
}

// TestAnthropicAuth_HeaderCommentMentionsCanonicalNames keeps the header
// comment that documents the CC tool surface from drifting away from the
// values in the map. We require the comment to mention every value the
// map produces, so a renamed value can't silently outpace the docs.
func TestAnthropicAuth_HeaderCommentMentionsCanonicalNames(t *testing.T) {
	src := readBuiltinExtensionSource(t, "anthropic_auth.py")

	// Take everything from the start of the file up to the
	// `register_tool_name_map(` call. This is the documentation block.
	idx := strings.Index(src, "register_tool_name_map(")
	if idx < 0 {
		t.Fatal("could not locate register_tool_name_map( in anthropic_auth.py")
	}
	header := src[:idx]

	got := extractToolNameMap(t, src)
	for fir, cc := range got {
		if !strings.Contains(header, cc) {
			t.Errorf("CC tool name %q (mapped from fir %q) is not mentioned in the file's header comment block; update the canonical-surface comment to match the map",
				cc, fir)
		}
	}
}

// readBuiltinExtensionSource reads a builtin extension source file via the
// embedded FS. Failures are fatal because they indicate the test setup
// (not the file under test) is broken.
func readBuiltinExtensionSource(t *testing.T, name string) string {
	t.Helper()
	data, err := BuiltinExtensionsFS.ReadFile("builtin_extensions/" + name)
	if err != nil {
		t.Fatalf("read builtin_extensions/%s: %v", name, err)
	}
	return string(data)
}

// toolMapEntryRe matches a single key/value pair (formatted as
//
//	"key": "value"
//
// possibly followed by a comma) inside the register_tool_name_map dict
// literal. The Python source uses double quotes everywhere and contains
// no escaped quotes inside keys or values, so a narrow regex is enough
// — and far less invasive than embedding a Python parser into Go tests.
var toolMapEntryRe = regexp.MustCompile(`"([A-Za-z_][A-Za-z0-9_]*)"\s*:\s*"([^"]+)"`)

// extractToolNameMap parses the dict literal passed to
// register_tool_name_map(...) in the given source. Returns an empty map
// (and fails the test) if the call cannot be located.
func extractToolNameMap(t *testing.T, src string) map[string]string {
	t.Helper()

	start := strings.Index(src, "register_tool_name_map(")
	if start < 0 {
		t.Fatal("register_tool_name_map( call not found in source")
	}

	// Find the matching closing parenthesis of the call by tracking
	// paren depth from the opening one. The dict literal lives between.
	openParen := start + strings.Index(src[start:], "(")
	depth := 0
	end := -1
	for i := openParen; i < len(src); i++ {
		switch src[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		t.Fatal("could not find matching close paren for register_tool_name_map(")
	}

	body := src[openParen+1 : end]
	matches := toolMapEntryRe.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		t.Fatalf("no entries found in register_tool_name_map call body:\n%s", body)
	}

	m := make(map[string]string, len(matches))
	for _, mm := range matches {
		k, v := mm[1], mm[2]
		if _, dup := m[k]; dup {
			t.Errorf("duplicate key %q in register_tool_name_map", k)
		}
		m[k] = v
	}
	return m
}
