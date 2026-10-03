package resources

import (
	"strings"
	"testing"

	"github.com/kfet/fir/pkg/ai"
)

// The stable block (before the cache boundary) must be byte-identical across
// sessions that differ only in cwd, date, host, context files and appended
// prompt — otherwise turn one of every session rewrites the whole prefix.
func TestBuildSystemPrompt_StableBlockByteIdenticalAcrossSessions(t *testing.T) {
	skills := []Skill{{Name: "testing", Description: "Testing guidelines", FilePath: "/skills/testing/SKILL.md"}}
	for _, custom := range []string{"", "Custom base prompt."} {
		a := BuildSystemPrompt(BuildSystemPromptOptions{
			CustomPrompt: custom, SelectedTools: []string{"read", "bash"}, Skills: skills,
			Cwd: "/proj/one", Date: "2026-01-01", Host: "alpha",
			ContextFiles:       []ContextFile{{Path: "/proj/one/AGENTS.md", Content: "one"}},
			AppendSystemPrompt: "append one",
		})
		b := BuildSystemPrompt(BuildSystemPromptOptions{
			CustomPrompt: custom, SelectedTools: []string{"read", "bash"}, Skills: skills,
			Cwd: "/proj/two", Date: "2027-12-31", Host: "beta",
			ContextFiles: []ContextFile{{Path: "/proj/two/AGENTS.md", Content: "two"}},
		})
		stableA, volA := ai.SplitSystemPrompt(a)
		stableB, volB := ai.SplitSystemPrompt(b)
		if stableA == "" {
			t.Fatalf("custom=%q: no cache boundary in prompt", custom)
		}
		if stableA != stableB {
			t.Errorf("custom=%q: stable block differs:\n%q\n%q", custom, stableA, stableB)
		}
		if !strings.Contains(stableA, "<available_skills>") {
			t.Errorf("custom=%q: skills list should be in the stable block", custom)
		}
		for _, v := range []string{"/proj/one", "2026-01-01", "alpha", "append one", "AGENTS.md"} {
			if strings.Contains(stableA, v) {
				t.Errorf("custom=%q: volatile %q leaked into stable block", custom, v)
			}
			if !strings.Contains(volA, v) {
				t.Errorf("custom=%q: volatile block missing %q", custom, v)
			}
		}
		if volA == volB {
			t.Errorf("custom=%q: volatile blocks should differ", custom)
		}
	}
}
