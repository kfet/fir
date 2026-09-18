package resources

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kfet/fir/pkg/mcp"
)

// The self skill serves the MCP configuration contract as a companion
// resource file whose body is injected at load time from pkg/mcp. This test
// covers the wiring — that the placeholder is expanded in a non-SKILL.md file
// and lands on disk in the extracted tree — not the contract's contents, which
// pkg/mcp guards structurally.
const mcpContractResource = "builtin_skills/self/mcp-config-contract.md"

func TestMCPConfigContractResourceIsExpanded(t *testing.T) {
	raw, err := BuiltinSkillsFS.ReadFile(mcpContractResource)
	require.NoError(t, err)
	require.Contains(t, string(raw), "{{FIR_MCP_CONFIG_CONTRACT}}",
		"resource file should carry the placeholder unexpanded in the embedded FS")

	expanded := string(expandSkillPlaceholders(raw))
	require.NotContains(t, expanded, "{{FIR_MCP_CONFIG_CONTRACT}}")
	require.Contains(t, expanded, "type ServerConfig struct")
	require.Contains(t, expanded, "type AuthConfig struct")
}

// TestSelfSkillPointsAtContractResource keeps the pointer honest: the skill
// must name the resource file, since the field enumeration it replaced is gone.
func TestSelfSkillPointsAtContractResource(t *testing.T) {
	raw, err := BuiltinSkillsFS.ReadFile("builtin_skills/self/SKILL.md")
	require.NoError(t, err)
	require.Contains(t, string(raw), filepath.Base(mcpContractResource))
}

// TestExtractedTreeExpandsResourceFiles verifies extraction (not just hashing)
// writes the expanded body, so the path handed to the model is readable.
func TestExtractedTreeExpandsResourceFiles(t *testing.T) {
	tmp := t.TempDir()
	orig := builtinSkillsCacheDir
	builtinSkillsCacheDir = func() (string, error) { return tmp, nil }
	t.Cleanup(func() { builtinSkillsCacheDir = orig })

	dir, err := extractBuiltinSkillsTo()
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, "self", "mcp-config-contract.md"))
	require.NoError(t, err)
	require.False(t, strings.Contains(string(data), "{{FIR_MCP_CONFIG_CONTRACT}}"))
	require.Contains(t, string(data), "AllowPrivateNetwork")
}

// The MCP region of the skill, delimited by the bullets opening and closing it.
const (
	mcpSectionStart = "- **MCP configuration**"
	mcpSectionEnd   = "- **MCP inspection**"
)

// nonConfigKeys are lower_snake_case identifiers that legitimately appear in
// the MCP region without being fir config keys.
var nonConfigKeys = map[string]bool{
	"scopes_supported": true, // RFC 8414 authorization-server metadata field
}

// jsonKeyLike matches backtick-quoted lower_snake_case identifiers. The
// underscore requirement is the "looks like a config key" heuristic; it keeps
// value words such as `bearer` and `none` out.
var jsonKeyLike = regexp.MustCompile("`([a-z][a-z0-9]*(?:_[a-z0-9]+)+)`")

// TestSelfSkillHasNoStaleMCPConfigKeys survives from the old drift guard, and
// only this direction does. Embedding the contract killed the forward check —
// an undocumented field is now impossible — but the skill still names keys in
// prose and in its JSON examples, so a field deleted from Go would leave the
// skill telling users to set something dead.
func TestSelfSkillHasNoStaleMCPConfigKeys(t *testing.T) {
	raw, err := BuiltinSkillsFS.ReadFile("builtin_skills/self/SKILL.md")
	require.NoError(t, err)
	doc := string(raw)

	start := strings.Index(doc, mcpSectionStart)
	require.GreaterOrEqual(t, start, 0, "self skill no longer contains %q — update the delimiter", mcpSectionStart)
	end := strings.Index(doc[start:], mcpSectionEnd)
	require.GreaterOrEqual(t, end, 0, "self skill no longer contains %q — update the delimiter", mcpSectionEnd)
	section := doc[start : start+end]

	tags := map[string]bool{}
	for _, typ := range []reflect.Type{reflect.TypeOf(mcp.ServerConfig{}), reflect.TypeOf(mcp.AuthConfig{})} {
		for i := 0; i < typ.NumField(); i++ {
			name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
			if name != "" {
				tags[name] = true
			}
		}
	}
	require.NotEmpty(t, tags, "collected no json tags — reflection walk is broken")

	for _, m := range jsonKeyLike.FindAllStringSubmatch(section, -1) {
		key := m[1]
		if tags[key] || nonConfigKeys[key] {
			continue
		}
		t.Errorf("self skill documents MCP config key %q, which is no longer a json tag on "+
			"mcp.ServerConfig/mcp.AuthConfig. Either the skill is stale, or %q belongs in nonConfigKeys.", key, key)
	}
}
