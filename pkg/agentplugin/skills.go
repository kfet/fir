package agentplugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kfet/fir/pkg/resources"
)

// Skills discovers the plugin's skills per §6 and §7.1: each immediate child
// of skills/ holding a SKILL.md that resolves to a regular file inside the
// plugin root. Nothing deeper is searched. Skills that fail the Agent Skills
// specification are skipped and reported. The returned paths are resolved.
func (p *Plugin) Skills() ([]string, []Diagnostic) {
	dir := filepath.Join(p.Root, SkillsDir)
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		return nil, nil // missing fixed location is not an error (§6.2)
	}
	real, ok, err := contained(p.Root, dir)
	switch {
	case err != nil:
		return nil, []Diagnostic{{dir, fmt.Sprintf("skills ignored: %v", err)}}
	case !ok:
		return nil, []Diagnostic{{dir, "skills ignored: skills/ resolves outside the plugin root"}}
	}
	entries, err := os.ReadDir(real)
	if err != nil {
		return nil, []Diagnostic{{dir, "skills ignored: skills/ is not a readable directory"}}
	}

	var out []string
	var diags []Diagnostic
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		skill := filepath.Join(real, e.Name(), "SKILL.md")
		if _, err := os.Lstat(skill); err != nil {
			continue // not a skill directory (or not a directory at all)
		}
		if _, ok, _ := contained(p.Root, filepath.Dir(skill)); !ok {
			diags = append(diags, Diagnostic{skill, "skill skipped: skill directory resolves outside the plugin root"})
			continue
		}
		rs, ok, err := contained(p.Root, skill)
		switch {
		case err != nil:
			diags = append(diags, Diagnostic{skill, fmt.Sprintf("skill skipped: %v", err)})
			continue
		case !ok:
			diags = append(diags, Diagnostic{skill, "skill skipped: SKILL.md resolves outside the plugin root"})
			continue
		}
		if fi, err := os.Stat(rs); err != nil || !fi.Mode().IsRegular() {
			diags = append(diags, Diagnostic{skill, "skill skipped: SKILL.md is not a regular file"})
			continue
		}
		// Validate against the directory name the plugin declared, not the
		// symlink target's.
		if errs := resources.ValidateSkillFile(skill); len(errs) > 0 {
			diags = append(diags, Diagnostic{skill, "skill skipped: " + strings.Join(errs, "; ")})
			continue
		}
		out = append(out, skill)
	}
	return out, diags
}
