// Forbidden-imports guard: keep pkg/agent and pkg/agent/tools free of
// fir's product-shaped subsystems so they can eventually be extracted
// as a standalone module (see docs/design/ai-agent-extraction.md).
//
// Until Phase 3 lands, pkg/ai (the fir provider catalog/policy surface)
// and pkg/log remain allowed and are not listed here. They are added
// to forbiddenPaths once that phase begins.

package agent_test

import (
	"os/exec"
	"strings"
	"testing"
)

// forbiddenPaths is the list of fir-side packages that pkg/agent and
// pkg/agent/tools must not import. Adding to this list tightens the
// boundary; removing requires a doc update in
// docs/design/ai-agent-extraction.md.
var forbiddenPaths = []string{
	"github.com/kfet/fir/pkg/session",
	"github.com/kfet/fir/pkg/mcp",
	"github.com/kfet/fir/pkg/extension",
	"github.com/kfet/fir/pkg/tui",
	"github.com/kfet/fir/pkg/config",
	"github.com/kfet/fir/pkg/auth",
	"github.com/kfet/fir/pkg/modes",
	"github.com/kfet/fir/pkg/resources",
	"github.com/kfet/fir/pkg/models",
}

// targets lists the packages whose import sets are checked.
var targets = []string{
	"github.com/kfet/fir/pkg/agent",
	"github.com/kfet/fir/pkg/agent/tools",
}

// TestForbiddenImports asserts that the boundary on which kfet/agent
// extraction depends has not eroded. It shells out to `go list` so the
// check sees the same import graph the compiler uses.
func TestForbiddenImports(t *testing.T) {
	for _, target := range targets {
		out, err := exec.Command("go", "list",
			"-deps",
			"-f", "{{.ImportPath}}",
			target,
		).CombinedOutput()
		if err != nil {
			t.Fatalf("go list %s: %v\n%s", target, err, out)
		}
		imported := map[string]struct{}{}
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			imported[line] = struct{}{}
		}
		for _, banned := range forbiddenPaths {
			if _, ok := imported[banned]; ok {
				t.Errorf("%s transitively imports forbidden package %s", target, banned)
			}
			// Subpackages are forbidden too.
			prefix := banned + "/"
			for path := range imported {
				if strings.HasPrefix(path, prefix) {
					t.Errorf("%s transitively imports forbidden package %s", target, path)
				}
			}
		}
	}
}
