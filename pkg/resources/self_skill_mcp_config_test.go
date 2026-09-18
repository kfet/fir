package resources

// Why this guard exists.
//
// v1.14.0 added mcp.AuthConfig.AllowPrivateNetwork and the self skill's
// paragraph enumerating every AuthConfig field silently lost a member; nobody
// noticed until it was fixed by hand in v1.14.1. These two tests make that
// drift a build failure, in both directions: a Go field with no mention in the
// skill, and a config key in the skill that no longer exists in Go.
//
// A GENERATED skill was considered and deliberately rejected — do not
// "improve" this into a generator, a go:generate directive, or BEGIN/END
// markers. The prose being guarded is dense, hand-written and agent-facing:
// it carries a named vendor bug (Okta), an RFC section reference, and a
// string-equality footgun (`localhost` != `127.0.0.1`) that no AST walker can
// synthesise from developer-facing doc comments. And because the skill is
// go:embed-ed, a generator would have to run before `go build` reads it — the
// first person to run `go build` directly instead of `make` would ship a
// stale skill silently. A test fails loudly instead.
//
// Scope is deliberately narrow: mcp.ServerConfig and mcp.AuthConfig only.
// Widening it to fir's broader settings.json surface would light up many
// pre-existing documentation gaps and force an exemption allowlist, which
// would become permanent and turn the guard into theatre.

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/mcp"
)

// guardedConfigTypes is the set of structs the self skill must document.
// Widening later is a one-line change here — but read the scope note above first.
var guardedConfigTypes = []reflect.Type{
	reflect.TypeOf(mcp.ServerConfig{}),
	reflect.TypeOf(mcp.AuthConfig{}),
}

// The MCP config/auth region of the skill, delimited by the bullets that open
// and close it. Restricting the inverse scan to this region — rather than
// growing an ignore list — keeps the false-positive surface tiny.
const (
	mcpSectionStart = "- **MCP configuration**"
	mcpSectionEnd   = "- **MCP inspection**"
)

// nonConfigKeys are lower_snake_case identifiers that legitimately appear in
// the MCP region without being fir config keys.
var nonConfigKeys = map[string]bool{
	"scopes_supported": true, // RFC 8414 authorization-server metadata field
}

// jsonKeyLike matches backtick-quoted lower_snake_case identifiers: at least
// one underscore, so `bearer`, `none` and `oauth` (values, not keys) are out.
// The underscore requirement is the "looks like a config key" heuristic, and
// it makes the inverse guard deliberately partial: single-word keys such as
// `roots` or `token` are not checked for staleness, because too much prose
// legitimately contains bare words. The forward test covers every key.
var jsonKeyLike = regexp.MustCompile("`([a-z][a-z0-9]*(?:_[a-z0-9]+)+)`")

// collectJSONTags walks t, recursing into nested structs and map[string]Struct
// values, and returns every json tag name.
func collectJSONTags(t reflect.Type, seen map[reflect.Type]bool, out map[string]bool) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		t = t.Elem()
	}
	if t.Kind() == reflect.Map {
		collectJSONTags(t.Elem(), seen, out)
		return
	}
	if t.Kind() != reflect.Struct || seen[t] {
		return
	}
	seen[t] = true
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" && !f.Anonymous {
			name = f.Name // encoding/json falls back to the field name
		}
		if name != "" {
			out[name] = true
		}
		collectJSONTags(f.Type, seen, out)
	}
}

// guardedTags returns every json tag across guardedConfigTypes.
func guardedTags(t *testing.T) map[string]bool {
	t.Helper()
	tags := map[string]bool{}
	seen := map[reflect.Type]bool{}
	for _, typ := range guardedConfigTypes {
		collectJSONTags(typ, seen, tags)
	}
	if len(tags) == 0 {
		t.Fatal("collected no json tags — reflection walk is broken")
	}
	return tags
}

func mcpSection(t *testing.T) string {
	t.Helper()
	raw, err := BuiltinSkillsFS.ReadFile("builtin_skills/self/SKILL.md")
	if err != nil {
		t.Fatalf("read embedded self skill: %v", err)
	}
	doc := string(raw)
	start := strings.Index(doc, mcpSectionStart)
	if start < 0 {
		t.Fatalf("self skill no longer contains %q — update the delimiters in this test", mcpSectionStart)
	}
	end := strings.Index(doc[start:], mcpSectionEnd)
	if end < 0 {
		t.Fatalf("self skill no longer contains %q — update the delimiters in this test", mcpSectionEnd)
	}
	return doc[start : start+end]
}

// TestSelfSkillDocumentsAllMCPConfigFields is the forward direction: every
// json tag on the guarded structs must be mentioned in the skill's MCP
// section, either backtick-quoted in prose or as a key in a JSON example.
// Requiring the quoting means prose that merely happens to contain the word
// does not count.
func TestSelfSkillDocumentsAllMCPConfigFields(t *testing.T) {
	section := mcpSection(t)
	tags := guardedTags(t)
	for tag := range tags {
		if strings.Contains(section, "`"+tag+"`") || strings.Contains(section, `"`+tag+`":`) {
			continue
		}
		t.Errorf("MCP config field %q is not documented in the self skill's MCP section.\n"+
			"Add prose for it (house style: `%s` …) — do not delete this test.", tag, tag)
	}
}

// TestSelfSkillHasNoStaleMCPConfigKeys is the inverse direction: every
// backtick-quoted lower_snake_case identifier in the MCP section must still
// exist as a json tag somewhere in the guarded structs. This catches a field
// deleted from Go while the skill still tells users to set a dead key.
func TestSelfSkillHasNoStaleMCPConfigKeys(t *testing.T) {
	section := mcpSection(t)
	tags := guardedTags(t)
	for _, m := range jsonKeyLike.FindAllStringSubmatch(section, -1) {
		key := m[1]
		if tags[key] || nonConfigKeys[key] {
			continue
		}
		t.Errorf("self skill documents MCP config key %q, which is no longer a json tag on %v.\n"+
			"Either the skill is stale, or %q belongs in nonConfigKeys.", key, guardedConfigTypes, key)
	}
}
